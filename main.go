package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const appVersion = "1.5.1"

var errNodeNotFound = errors.New("node not found")

var startedAt = time.Now()

//go:embed all:web
var webFS embed.FS

type Server struct {
	store   *Store
	limiter *loginLimiter
	ops     *opLimiter
	results *resultLog
	dataDir string
	// traffic 缓冲节点承载的测速流量，定期落盘（见 traffic.go）
	traffic *trafficMeter
	// live 记录当前正在测速的会话，仅内存，用于首页人数展示
	live *liveTracker
}

func main() {
	log.SetFlags(log.LstdFlags)
	initGarbage()

	// -v / --version：安装脚本与排障用，打印后直接退出，不碰任何配置与端口
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-v", "--version", "version":
			fmt.Printf("speedtest %s\n", appVersion)
			return
		}
	}

	// 节点模式：完全独立的纯测速服务，不创建数据目录、不加载 config.json、
	// 不提供任何后台/web 接口，配置只来自环境变量且在启动时一次性固化。
	if isNodeMode() {
		runNode()
		return
	}

	dataDir := env("DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("无法创建数据目录: %v", err)
	}
	store, err := NewStore(filepath.Join(dataDir, "config.json"))
	if err != nil {
		log.Fatalf("配置初始化失败: %v", err)
	}
	if os.Getenv("ADMIN_PASSWORD") != "" && os.Getenv("ADMIN_FORCE_RESET") == "1" {
		if err := store.Update(func(c *Config) error {
			nextVersion := c.Admin.SessionVersion + 1
			c.Admin = newAdmin(env("ADMIN_USER", c.Admin.Username), os.Getenv("ADMIN_PASSWORD"))
			c.Admin.SessionVersion = nextVersion
			return nil
		}); err != nil {
			log.Printf("重置管理员密码失败: %v", err)
		} else {
			log.Printf("管理员密码已按环境变量重置")
		}
	}
	// 站点名、节点名等env只在首次创建配置时生效（见 NewStore），避免覆盖后台修改
	srv := &Server{
		store:   store,
		limiter: newLoginLimiter(),
		ops:     newOpLimiter(),
		results: newResultLog(filepath.Join(dataDir, "results.jsonl")),
		dataDir: dataDir,
		traffic: newTrafficMeter(),
		live:    newLiveTracker(),
	}

	// 流量统计：内存累加 → 定期落盘；每天检查一次「每月重置日」。
	// 两者都是纯后台任务，失败只记日志，不影响测速服务本身。
	go func() {
		t := time.NewTicker(trafficFlushInterval)
		defer t.Stop()
		for range t.C {
			srv.flushTraffic()
		}
	}()
	go func() {
		srv.maybeResetTraffic() // 启动先跑一次，避免刚好跨过重置日的那次重启漏掉
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for range t.C {
			srv.maybeResetTraffic()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/", srv.apiRouter)
	mux.HandleFunc("/healthz", srv.handleHealthz)
	mux.HandleFunc("/routes.js", srv.handleRoutesJS)
	mux.HandleFunc("/site-icon", srv.handleSiteIcon)
	mux.HandleFunc("/", srv.handleStatic)

	// 默认只监听本机回环，由 Nginx / Caddy 等反向代理对外提供 HTTPS。
	// 需要直接对外时可用 HTTP_HOST=0.0.0.0 覆盖，但必须先确认已处理好 TLS 与 IP 头信任。
	addr := net.JoinHostPort(env("HTTP_HOST", "127.0.0.1"), env("HTTP_PORT", "3333"))
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.logRequests(mux),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout 必须为 0，否则会截断长时间下载流
	}

	go func() {
		cfgNow := store.Get()
		log.Printf("测速服务已启动 %s (版本 %s, 站点 %s)", addr, appVersion, cfgNow.Settings.SiteName)
		// 后台是隐藏路径，装完很容易忘。启动时明确打出来（日志只有服务器能看）。
		adminDir := normalizeAdminPath(cfgNow.Settings.AdminPath)
		if adminDir == "" {
			adminDir = defaultAdminPath()
		}
		log.Printf("后台入口：/%s   （账号 %s；路径可在后台「站点设置 → 后台入口」里改）",
			adminDir, cfgNow.Admin.Username)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
	// 把最后一段还没落盘的流量计数写进去，避免重启丢数据
	srv.flushTraffic()
	log.Println("服务已停止")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.setSecurityHeaders(w, r)
		p := r.URL.Path
		// 测速端点频率高，不记录日志
		if strings.HasPrefix(p, "/api/download") || strings.HasPrefix(p, "/api/upload") || strings.HasPrefix(p, "/api/ping") {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s %s", r.Method, p, sw.status, time.Since(start).Round(time.Millisecond), clientIP(r))
	})
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if isSecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// contentSecurityPolicy 只允许本站脚本，以及后台已配置的测速节点作为跨源连接目标。
func (s *Server) contentSecurityPolicy(r *http.Request) string {
	origins := map[string]bool{requestOrigin(r): true}
	// 对外域名（自动识别或 PUBLIC_BASE_URL 显式指定）也要放行：
	// 换域名过渡期或反代改写 Host 时，前端补探地址可能和 Host 不一致。
	if base := requestPublicBase(r); base != "" {
		origins[base] = true
	}
	cfg := s.store.Get()
	for _, n := range cfg.Nodes {
		for _, raw := range []string{n.URL, n.URL6} {
			u, err := url.Parse(strings.TrimSpace(raw))
			if err == nil && u.Scheme != "" && u.Host != "" {
				origins[u.Scheme+"://"+u.Host] = true
			}
		}
	}
	// 单栈补探域名也要放行：前端切协议时会跨源请求它们的 /api/isp。
	// 用与下发 addrs 相同的规范化结果，保证 CSP 白名单和实际请求地址完全一致。
	for _, raw := range []string{cfg.Settings.ProbeV4, cfg.Settings.ProbeV6} {
		if v := normalizeProbeURL(raw); v != "" {
			origins[v] = true
		}
	}
	list := make([]string, 0, len(origins))
	// CSP 的 host-source 语法【不支持 IPv6 字面量】：写成 https://[2607:...]:8445 时
	// 浏览器会判定为非法源并整条忽略（控制台报 invalid source），请求直接被 CSP 拦掉。
	// 所以只要节点里有 IPv6 地址，connect-src 就退化成按协议放行（https:/http:），
	// 其余指令（script-src、frame-ancestors 等）保持严格不变。
	needSchemeWide := false
	for o := range origins {
		if strings.HasPrefix(o, "https://[") || strings.HasPrefix(o, "http://[") {
			needSchemeWide = true
			continue
		}
		list = append(list, o)
	}
	sort.Strings(list)
	if needSchemeWide {
		list = append(list, "https:", "http:")
	}
	// 下载测速用的是 Blob Worker，CSP 必须显式放行 blob:，否则浏览器直接拒绝创建 Worker。
	return "default-src 'self'; script-src 'self' blob:; worker-src blob:; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; connect-src " + strings.Join(list, " ") +
		"; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
}

func (s *Server) setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Content-Security-Policy", s.contentSecurityPolicy(r))
	if isSecureRequest(r) {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "application/javascript; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".ico":         "image/x-icon",
	".webmanifest": "application/manifest+json",
}

// routePaths 是一组生效后的前台路径段，由后台配置决定（带默认值回落）。
type routePaths struct {
	langZh, langEn, v4, v6, result string
}

func pathsFromSettings(st Settings) routePaths {
	a, b, c, d, e := effectivePaths(st)
	return routePaths{langZh: a, langEn: b, v4: c, v6: d, result: e}
}

// stripLang 去掉语言前缀（/zh-Hans、/en 或后台自定义段），返回始终以 / 开头的剩余路径
func stripLang(p string, rp routePaths) string {
	for _, seg := range []string{rp.langZh, rp.langEn} {
		pre := "/" + seg
		if p == pre {
			return "/"
		}
		if strings.HasPrefix(p, pre+"/") {
			return p[len(pre):]
		}
	}
	return p
}

// isResultPath：/result/<id>、/<v4>/result/<id>、/<v6>/result/<id>（均可带语言前缀）
func isResultPath(p string, rp routePaths) bool {
	q := stripLang(p, rp)
	r := "/" + rp.result
	return strings.HasPrefix(q, r+"/") ||
		strings.HasPrefix(q, "/"+rp.v4+r+"/") ||
		strings.HasPrefix(q, "/"+rp.v6+r+"/")
}

// isIndexPath：首页 / 以及协议专用入口 /<v4>、/<v6>（均可带语言前缀）
func isIndexPath(p string, rp routePaths) bool {
	q := strings.TrimSuffix(stripLang(p, rp), "/")
	return q == "" || q == "/"+rp.v4 || q == "/"+rp.v6
}

// handleRoutesJS 把后台自定义的前台路径段以同源脚本形式下发给浏览器。
// 走独立 .js 而不是内联脚本，是为了兼容 CSP 的 script-src 'self'（不允许 inline）。
// 用 json.Marshal 生成字面量，路径段已限制为字母数字/连字符/下划线，不存在注入风险。
func (s *Server) handleRoutesJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rp := pathsFromSettings(s.store.Get().Settings)
	cfg := s.store.Get().Settings
	adminPath := normalizeAdminPath(cfg.AdminPath)
	if adminPath == "" {
		adminPath = defaultAdminPath()
	}
	payload := map[string]string{
		"langZh": rp.langZh,
		"langEn": rp.langEn,
		"v4":     rp.v4,
		"v6":     rp.v6,
		"result": rp.result,
		"admin":  adminPath,
	}
	b, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte("window.ST_ROUTES=" + string(b) + ";"))
}

// siteIconFile 返回后台上传图标在 data 目录里的物理路径（固定文件名，覆盖式上传）。
func (s *Server) siteIconFile() string {
	return filepath.Join(s.dataDir, "site-icon")
}

// handleSiteIcon 对外提供后台上传的站点图标。图标本体不随前端静态资源打包，
// 存到可写的 data 目录；用 ?v= 版本号绕开浏览器与反代缓存。
func (s *Server) handleSiteIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := s.siteIconFile()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// 类型：优先用上传时记录的扩展名（SVG 等内容探测不准），否则按内容探测。
	ct := ""
	if b, err := os.ReadFile(s.siteIconFile() + ".ext"); err == nil {
		switch strings.TrimSpace(string(b)) {
		case ".svg":
			ct = "image/svg+xml"
		case ".png":
			ct = "image/png"
		case ".jpg", ".jpeg":
			ct = "image/jpeg"
		case ".gif":
			ct = "image/gif"
		case ".webp":
			ct = "image/webp"
		case ".ico":
			ct = "image/x-icon"
		case ".avif":
			ct = "image/avif"
		}
	}
	if ct == "" {
		head := make([]byte, 512)
		n, _ := f.Read(head)
		detected := http.DetectContentType(head[:n])
		f.Seek(0, io.SeekStart)
		if strings.HasPrefix(detected, "image/") {
			ct = detected
		}
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// SVG 是同源资源，里面若带 <script> 就能在本站执行 —— 用 CSP 掐掉脚本与外部加载，
	// 只留下图形本身需要的样式。
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	io.Copy(w, f)
}

// injectBrand 把 HTML 里写死的品牌名替换成后台配置值，避免首屏/标签页闪现 "SpeedTest"。
// 只在服务端渲染时做，前端 JS 仍会按 settings 再设一次，二者一致。
func (s *Server) injectBrand(doc string, r *http.Request) string {
	st := s.store.Get().Settings
	name := strings.TrimSpace(st.SiteName)
	if name == "" {
		name = "SpeedTest"
	}
	// 标签页标题后缀：后台填了就用它，否则按当前路径语言给默认文案。
	suffix := strings.TrimSpace(st.TitleSuffix)
	if suffix == "" {
		suffix = "网络测速"
		rp := pathsFromSettings(st)
		if r != nil && (r.URL.Path == "/"+rp.langEn || strings.HasPrefix(r.URL.Path, "/"+rp.langEn+"/")) {
			suffix = "Speed Test"
		}
	}
	esc := html.EscapeString(name)
	fullTitle := esc + " · " + html.EscapeString(suffix)
	// <title>SpeedTest</title> 与 <title>测速报告 · SpeedTest</title>
	doc = strings.ReplaceAll(doc, "<title>SpeedTest</title>", "<title>"+fullTitle+"</title>")
	doc = strings.ReplaceAll(doc, "<title>测速报告 · SpeedTest</title>", "<title>测速报告 · "+esc+"</title>")
	// 首页 logo：<span class="logo-text" id="siteName">SPEEDTEST</span>
	doc = strings.ReplaceAll(doc, `>SPEEDTEST</span>`, `>`+esc+`</span>`)
	// 站点图标：后台上传后把默认 favicon 换成带版本号的自定义图标（首页与报告页通用）。
	// 不写死 type，让浏览器按响应 Content-Type 识别（SVG/PNG 等都可能）。
	if icon := strings.TrimSpace(st.IconURL); icon != "" {
		safe := html.EscapeString(icon)
		doc = strings.ReplaceAll(doc, `<link rel="icon" href="/favicon.svg" type="image/svg+xml">`,
			`<link rel="icon" href="`+safe+`">`)
	}
	// 后台（admin.html）的品牌区结构不同：<div class="a-brand"><svg .../><span id="headSite">SpeedTest</span>
	// 名称与图标同样在服务端一次定好，避免进后台先闪默认名字 + 默认圆环图标。
	doc = strings.ReplaceAll(doc, `<span id="headSite">SpeedTest</span>`, `<span id="headSite">`+esc+`</span>`)
	if adminBrandRe.MatchString(doc) {
		head := adminBrandRe.FindStringSubmatch(doc)[1]
		repl := head
		if !st.HideLogoIcon {
			if icon := strings.TrimSpace(st.IconURL); icon != "" {
				// 尺寸直接写在标签上：后台样式表版本不一致时也不会把整页撑爆
				repl += `<img class="a-brand-icon" src="` + html.EscapeString(icon) +
					`" alt="" width="24" height="24" style="width:24px;height:24px;object-fit:contain;border-radius:5px">`
			} else {
				repl += adminBrandRe.FindStringSubmatch(doc)[2]
			}
		}
		doc = adminBrandRe.ReplaceAllString(doc, repl)
	}
	// 顶部 logo 的图标：全部在服务端一次定型，首帧就是最终样子。
	//   · 勾了「不显示图标」 → 连默认圆环一起删掉，只留文字
	//   · 有自定义图标       → 默认 <svg> 换成 <img>（以前靠 JS 插，刷新会先闪一下默认圆环）
	//   · 都没有             → 保留默认圆环
	if svgLogoRe.MatchString(doc) {
		if st.HideLogoIcon {
			doc = svgLogoRe.ReplaceAllString(doc, "")
		} else if icon := strings.TrimSpace(st.IconURL); icon != "" {
			doc = svgLogoRe.ReplaceAllString(doc,
				`<img id="logoIcon" class="logo-mark" src="`+html.EscapeString(icon)+
					`" alt="" width="20" height="20" style="width:20px;height:20px;object-fit:contain;border-radius:4px">`)
		}
	}
	return doc
}

// 首页与报告页里写死的默认 logo（一段 <svg class="logo-mark">…</svg>）。
// 只在自定义图标存在时才替换，没配图标就保持原样。
var svgLogoRe = regexp.MustCompile(`(?s)<svg class="logo-mark".*?</svg>`)

// 后台品牌区里那段默认的 <svg viewBox="0 0 32 32">…</svg>（第 2 段是 svg 本身）。
var adminBrandRe = regexp.MustCompile(`(?s)(<div class="a-brand">\s*)(<svg.*?</svg>)`)

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawPath := r.URL.Path
	p := strings.TrimPrefix(rawPath, "/")
	rp := pathsFromSettings(s.store.Get().Settings)
	// 结果分享页：语言前缀 + 可选协议段 + 报告段，例如 /zh-Hans/ipv4/result/<id>；
	// 各段来自后台配置，同时兼容没有前缀的 /result/<id>。
	if isResultPath(rawPath, rp) {
		p = "result.html"
	} else if isIndexPath(rawPath, rp) {
		p = "index.html"
	}

	// 后台入口只在隐藏路径上开放：/admin、/admin.html 一律 404，首页也不放真链接。
	// 直接访问物理文件名会在这里被挡住，只有下面按配置映射过来的请求能读到 admin.html。
	if p == "admin" || p == "admin/" || p == "admin.html" {
		http.NotFound(w, r)
		return
	}
	if ap := normalizeAdminPath(s.store.Get().Settings.AdminPath); ap != "" &&
		(rawPath == "/"+ap || rawPath == "/"+ap+"/") {
		p = "admin.html"
	}

	switch p {
	case "", "index.html":
		p = "index.html"
	case "favicon.ico":
		p = "favicon.svg"
	}
	if strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	if strings.Contains(p, "..") {
		http.NotFound(w, r)
		return
	}
	body, err := webFS.ReadFile("web/" + p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(p))
	// 给脚本/样式带上版本号，升级后浏览器一定会重新拉取，不会拿着旧文件跑新页面
	if ext == ".html" {
		html := strings.ReplaceAll(string(body), ".js\"", ".js?v="+appVersion+"\"")
		html = strings.ReplaceAll(html, ".css\"", ".css?v="+appVersion+"\"")
		// 首屏就把后台配置的品牌名写进 title 与 logo，不再闪现写死的 SpeedTest
		html = s.injectBrand(html, r)
		body = []byte(html)
	}
	if ct, ok := contentTypes[ext]; ok {
		w.Header().Set("Content-Type", ct)
	}
	// 页面与脚本都要求每次回来问一遍：升级后前端改动立即生效，不会卡在旧缓存上
	switch ext {
	case ".html", ".js", ".css":
		w.Header().Set("Cache-Control", "no-cache")
	default:
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(body)
}
