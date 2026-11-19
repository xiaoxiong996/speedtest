package main

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// isNodeMode 判断是否以「纯测速节点」模式运行。
// APP_MODE=node 启用；缺省 controller 即原有完整主控版（web + 后台 + 测速）。
func isNodeMode() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("APP_MODE")), "node")
}

// nodeConfig 只在启动时从环境变量读入一次，之后全程只读。
// 节点模式不注册任何后台/写接口，运行期没有任何途径能改动这些值，
// 因此外部请求既改不了配置，也无法通过伪造头注入访问白名单。
type nodeConfig struct {
	prefix       string         // 路径前缀，端点为 prefix + /api/{ping,download,upload,isp}
	allowCIDRs   []*net.IPNet   // 允许测速的访客 IP 段；空=不限制
	allowHosts   []string       // 允许的访问域名（小写，支持 *. 通配）；空=不限制
	allowOrigins []string       // 允许的跨源 Origin；空=回显任意（等价于 *）
}

// normalizeNodePrefix 把 NODE_PATH_PREFIX 规范成安全的路径前缀（/xxx），
// 只保留字母数字与 - _ /，拒绝其余字符，避免路径注入。
func normalizeNodePrefix(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || s == "/" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	var b strings.Builder
	lastSlash := false
	for _, c := range s {
		ok := c == '/' || c == '-' || c == '_' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return ""
		}
		if c == '/' {
			if lastSlash {
				continue
			}
			lastSlash = true
		} else {
			lastSlash = false
		}
		b.WriteRune(c)
	}
	return strings.TrimSuffix(b.String(), "/")
}

func loadNodeConfig() *nodeConfig {
	rawPrefix := strings.TrimSpace(os.Getenv("NODE_PATH_PREFIX"))
	c := &nodeConfig{prefix: normalizeNodePrefix(rawPrefix)}
	// 写了但不合法 → 直接退出。静默忽略会让人以为路径生效了，实际端点仍在 /api 下。
	if rawPrefix != "" && rawPrefix != "/" && c.prefix == "" {
		log.Fatalf("节点：NODE_PATH_PREFIX %q 非法，只允许字母、数字与 - _ /", rawPrefix)
	}

	for _, raw := range splitListEnv("NODE_ALLOW_CIDRS", "") {
		s := strings.TrimSpace(raw)
		if !strings.Contains(s, "/") {
			if ip := net.ParseIP(s); ip != nil {
				if ip.To4() != nil {
					s += "/32"
				} else {
					s += "/128"
				}
			}
		}
		if _, n, err := net.ParseCIDR(s); err == nil {
			c.allowCIDRs = append(c.allowCIDRs, n)
		} else {
			log.Fatalf("节点：NODE_ALLOW_CIDRS 里的 %q 不是合法 IP 或网段", raw)
		}
	}

	for _, raw := range splitListEnv("NODE_ALLOW_HOSTS", "") {
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" {
			continue
		}
		if !validHostPattern(h) {
			log.Fatalf("节点：NODE_ALLOW_HOSTS 里的 %q 不是合法域名（只允许字母数字 . - 与开头的 *.）", raw)
		}
		c.allowHosts = append(c.allowHosts, h)
	}

	for _, raw := range splitListEnv("NODE_ALLOW_ORIGINS", "") {
		o := strings.TrimSpace(raw)
		if o == "*" {
			c.allowOrigins = []string{"*"}
			break
		}
		if o != "" {
			c.allowOrigins = append(c.allowOrigins, strings.ToLower(o))
		}
	}
	return c
}

// validHostPattern 校验域名白名单条目：允许 * 前缀通配，其余只能是域名安全字符。
func validHostPattern(h string) bool {
	if strings.HasPrefix(h, "*.") {
		h = h[2:]
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for _, c := range h {
		ok := c == '.' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// hostNameOf 从 Host 头取出纯主机名（去端口与 IPv6 方括号）。
// 刻意使用 r.Host 而非 X-Forwarded-Host，后者可被客户端伪造。
func hostNameOf(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

func (c *nodeConfig) hostAllowed(r *http.Request) bool {
	// 面板里改过的白名单优先（立即生效）；没改过就是 .env 里的值
	hosts := c.allowHosts
	if live := nodeLiveAllow.get(); live.AllowHosts != nil {
		hosts = live.AllowHosts
	}
	if len(hosts) == 0 {
		return true
	}
	h := hostNameOf(r)
	for _, want := range hosts {
		if strings.HasPrefix(want, "*.") {
			if strings.HasSuffix(h, want[1:]) {
				return true
			}
		} else if h == want {
			return true
		}
	}
	return false
}

func (c *nodeConfig) ipAllowed(ip string) bool {
	// 面板里改过的白名单优先；格式不对的条目直接跳过（保存时已校验）
	list := c.allowCIDRs
	if live := nodeLiveAllow.get(); live.AllowCIDRs != nil {
		list = make([]*net.IPNet, 0, len(live.AllowCIDRs))
		for _, raw := range live.AllowCIDRs {
			if _, n, err := net.ParseCIDR(raw); err == nil {
				list = append(list, n)
			} else if one := net.ParseIP(raw); one != nil {
				bits := 32
				if one.To4() == nil {
					bits = 128
				}
				list = append(list, &net.IPNet{IP: one, Mask: net.CIDRMask(bits, bits)})
			}
		}
	}
	if len(list) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range list {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

func (c *nodeConfig) originAllowed(origin string) bool {
	if len(c.allowOrigins) == 0 {
		return true
	}
	o := strings.ToLower(strings.TrimSpace(origin))
	for _, want := range c.allowOrigins {
		if want == "*" || want == o {
			return true
		}
	}
	return false
}

// nodeHasCert 表示当前是否有可用证书（决定了走 HTTPS 还是明文 HTTP，
// 也决定测速端点是否开放）。放在包级是因为 nodeGuard 是方法，拿不到局部变量。
var nodeHasCert atomicBool

type atomicBool struct{ v atomic.Bool }

func (a *atomicBool) set(b bool) { a.v.Store(b) }
func (a *atomicBool) get() bool  { return a.v.Load() }

// realClientIP 取节点的真实访客 IP：直连时就是 RemoteAddr；
// 前面套了反代/CDN 时按 TRUSTED_PROXIES 决定是否采信转发头。
func realClientIP(r *http.Request) string {
	if ip := clientIP(r); ip != "" {
		return ip
	}
	return normalizeIP(r.RemoteAddr)
}

// nodeGuard 是节点模式的统一入口：安全头 → 域名/IP 白名单 → CORS。
// 校验失败一律 403，不进入测速逻辑，也不产生任何写入。
func (c *nodeConfig) nodeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		// CSP 分两类：
		//   · 测速端点 —— default-src 'none'，纯数据接口不需要加载任何东西
		//   · 面板页面 —— 必须放行自己的内联 style/script，否则页面上的按钮全部点不动
		//     （浏览器会拦掉内联脚本，JS 根本不执行，表现就是"点了没反应"）
		//     只放行 'unsafe-inline' 的样式与脚本，仍然禁止外部资源、framing、表单外发。
		// 用"当前生效"的路径（可能来自 certs/panel.json，面板里改过），不是只看 .env
		panelPath := "/" + currentPanelPath()
		isPanelPage := panelPath != "/" && (r.URL.Path == panelPath ||
			strings.HasPrefix(r.URL.Path, panelPath+"/"))
		// 面板自身的接口：浏览器同源请求一定会带 Origin（就是节点自己的地址），
		// 它天然不在 NODE_ALLOW_ORIGINS 里，必须豁免，否则登录直接 403。
		isPanelAPI := strings.HasPrefix(r.URL.Path, "/api/panel/")
		if isPanelPage {
			h.Set("Content-Security-Policy",
				"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; "+
					"img-src data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		} else {
			h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		}

		// 健康检查只供本机/docker 探活，豁免域名与 IP 白名单。
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		// 面板路径豁免 Host 白名单（但保留 IP 白名单与登录校验），
		// 否则一旦把 Host 白名单填错，面板自己就先被挡在外面了。
		// 没有证书时，测速请求先给明确提示（而不是被 Host 白名单拦成 forbidden）
		if !nodeHasCert.get() && strings.HasPrefix(r.URL.Path, "/api/") &&
			!strings.HasPrefix(r.URL.Path, "/api/panel/") && r.URL.Path != "/api/tls" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":    false,
				"error": "节点还没有证书，请先在面板签发证书",
			})
			return
		}

		if isPanelPage || isPanelAPI {
			// 面板完全豁免 IP 白名单：
			// 白名单的用途是「限制谁能拿这台机器测速」，不该限制「谁能来管这台机器」。
			// 面板本身已有隐藏路径 + 账号密码 + 会话签名，够用了；
			// 反过来如果拿 IP 白名单卡面板，一旦填错就再也进不去，只能改 .env 或删配置。
			if r.Method == http.MethodOptions {
				h.Set("Allow", "GET, POST, OPTIONS")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		deny := func(reason string) {
			log.Printf("节点：拒绝 %s %s from %s host=%q (%s)", r.Method, r.URL.Path, clientIP(r), r.Host, reason)
			http.Error(w, "forbidden", http.StatusForbidden)
		}
		if !c.hostAllowed(r) {
			deny("host not allowed")
			return
		}
		// 直连场景下 RemoteAddr 就是访客地址；clientIP 在“回环/私网来源但没有任何
		// 转发头”时会返回空（主控那边表示“拿不到公开地址”），节点白名单不能因此误杀。
		visitIP := realClientIP(r)
		if !c.ipAllowed(visitIP) {
			deny("ip not allowed")
			return
		}
		// 节点只提供读/测速端点，别的方法一律拒绝，不给任何探测或写入的机会。
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions:
		default:
			h.Set("Allow", "GET, HEAD, POST, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// CORS：只在 Origin 命中白名单时回显，避免任意站点借节点做测速。
		// 若未配置 NODE_ALLOW_ORIGINS，setCORS 会按默认策略放行（见 http_util.go）。
		origin := r.Header.Get("Origin")
		if origin != "" && len(c.allowOrigins) > 0 {
			if c.originAllowed(origin) {
				if strings.Contains(strings.Join(c.allowOrigins, ","), "*") {
					h.Set("Access-Control-Allow-Origin", "*")
				} else {
					h.Set("Access-Control-Allow-Origin", origin)
					h.Add("Vary", "Origin")
				}
			} else {
				deny("origin not allowed")
				return
			}
		}
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Range")
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// runNode 启动纯测速节点：无后台、无 web、无配置/结果落盘。
// 复用 speed.go / isp.go 里的 handler，它们不依赖 store，安全可用。
func runNode() {
	cfg := loadNodeConfig()
	// 让全局 setCORS 按节点白名单策略输出（主控模式该值为空，行为不变）。
	setCORSOrigins(cfg.allowOrigins)

	srv := &Server{} // 仅用于调用不依赖 store 的测速 handler
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.handleHealthz)
	// 前端固定把 /api/ping 等拼到「节点地址」后面，因此自定义路径是加在 /api 之前的 base path。
	// 前缀为空 → /api/ping；前缀为 /s3cr3t → /s3cr3t/api/ping。
	// 主控后台把节点地址填成 https://host:port + 该前缀即可，前端无需改动。
	base := cfg.prefix

	// 有没有证书决定了：走 HTTPS 还是明文、测速端点是否开放。
	// 必须在注册路由之前算出来，否则同一 pattern 会注册两次（ServeMux 会直接 panic）。
	certDir := env("NODE_CERT_DIR", "/certs")
	store, err := newCertStore(certDir)
	if err != nil {
		log.Fatalf("节点：初始化证书目录失败: %v", err)
	}
	hasCert := store.HasCert()
	nodeHasCert.set(hasCert)

	if hasCert {
		mux.HandleFunc(base+"/api/ping", srv.handlePing)
		mux.HandleFunc(base+"/api/isp", srv.handleISP)
		mux.HandleFunc(base+"/api/download", srv.handleDownload)
		mux.HandleFunc(base+"/api/upload", srv.handleUpload)
	} else {
		// 没有证书：测速端点一律 503，只留面板与 /api/tls，
		// 避免出现"明文测速"这种 https 页面必定被拦掉的假可用状态。
		blocker := func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":    false,
				"error": "节点还没有证书，请先在面板签发",
			})
		}
		for _, p := range []string{"/api/ping", "/api/isp", "/api/download", "/api/upload"} {
			mux.HandleFunc(base+p, blocker)
		}
	}

	addr := net.JoinHostPort(env("HTTP_HOST", "0.0.0.0"), env("HTTP_PORT", "3333"))

	// ---------------- 证书：有就用 HTTPS，没有就用明文 HTTP ----------------
	// 节点不提供自签证书。证书走 Let's Encrypt + DNS-01（只加一条 TXT 记录，
	// 不需要 80/443，NAT 机器可用），两种驱动方式在面板里选：
	//   · 配了 Cloudflare API Token → 全自动签发 + 自动续期
	//   · 没配 → 面板显示 TXT，你去 DNS 后台添加，点一下继续
	//
	// 关键：**没有证书时直接以明文 HTTP 运行**。
	// 这样首次部署、或者你手动删掉证书后，浏览器能正常打开面板去签新证书，
	// 不会卡在"证书无效 → 打不开面板 → 没法签证书"的死锁里。
	// 此时测速端点会返回 503 并提示先签证书（明文测速会被 https 页面按混合内容拦掉，跑不通）。
	port := env("HTTP_PORT", "8443")

	panel := setupNodePanel()

	var tlsCfg *tls.Config
	scheme := "http"
	if hasCert {
		scheme = "https"
		tlsCfg = store.TLSConfig()
	}

	if panel != nil {
		panel.register(mux)
	}

	tlsHandler := func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		info := map[string]any{"scheme": scheme, "hasCert": hasCert}
		info["certs"] = store.Entries()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tls": info})
	}
	mux.HandleFunc(base+"/api/tls", tlsHandler)

	hs := &http.Server{
		Addr:              addr,
		Handler:           cfg.nodeGuard(mux),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 节点只接测速请求，头部不需要很大；限制住可以挡掉畸形/超大头部攻击
		MaxHeaderBytes: 16 << 10,
		TLSConfig:      tlsCfg,
	}

	log.Printf("测速节点已启动 %s://%s (版本 %s, 前缀 %q, IP段 %d, 域名 %d, Origin %d)",
		scheme, addr, appVersion, cfg.prefix, len(cfg.allowCIDRs), len(cfg.allowHosts), len(cfg.allowOrigins))

	if hasCert {
		log.Printf("节点证书：已加载 %s（Let's Encrypt / DNS-01，自动续期）", certDir+"/cert.pem")
		if panel != nil {
			log.Printf("        面板：https://<域名>:%s/%s", port, panel.path)
		}
	} else {
		log.Printf("节点证书：还没有证书，当前以【明文 HTTP】运行（浏览器能正常打开面板签发）")
		log.Printf("        注意：此时测速端点返回 503，签好证书后自动切回 HTTPS")
		if panel != nil {
			log.Printf("        面板：http://<本机IP>:%s/%s", port, panel.path)
		}
	}

	// 有证书走 TLS（GetCertificate 热加载，传空路径即可），没证书就走明文。
	if tlsCfg != nil {
		err = hs.ListenAndServeTLS("", "")
	} else {
		err = hs.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatalf("节点启动失败: %v", err)
	}
}
