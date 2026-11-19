package main

// 节点版自带的面板：隐藏路径 + 账号密码，只做三件事
//   1. 申请/续期证书（DNS-01，不需要 80/443，NAT 机器可用）
//        · 配了 Cloudflare API Token → 全自动：自动加 TXT、自动校验、自动删、自动续期
//        · 没配 → 手动：面板显示 TXT，你去 DNS 后台添加，点一下继续
//   2. 改访问限制（允许的 Host / 访客 IP 段 / 允许的 Origin），立即生效
//   3. 看当前证书剩余天数
//
// 面板与测速端点共用同一个端口，页面在 /<NODE_PANEL_PATH>，接口在 /api/panel/*。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const nodePanelCookie = "st_node_panel"

// 新安装的默认面板入口与账号密码：装上就能进，登录后可在面板里改，
// 改动写进 certs/panel.json，重启后仍然生效。
const (
	defaultPanelPath = "st-node-panel"
	defaultPanelUser = "admin"
	defaultPanelPass = "Node@2026"
)

// 当前生效的面板路径（可能来自 panel.json），nodeGuard 判断"这是不是面板请求"时要用它。
var panelPathLive atomic.Value

func currentPanelPath() string {
	if v, ok := panelPathLive.Load().(string); ok {
		return v
	}
	return defaultPanelPath
}

func setPanelPathLive(v string) {
	if v != "" {
		panelPathLive.Store(v)
	}
}

// 全局：nodeGuard 读它判断白名单（面板改了立即生效）
var nodeLiveAllow atomicState

type atomicState struct {
	mu sync.RWMutex
	v  nodeState
}

type nodeState struct {
	AllowHosts   []string `json:"allowHosts"`
	AllowCIDRs   []string `json:"allowCIDRs"`
	AllowOrigins []string `json:"allowOrigins"`
}

func (a *atomicState) get() nodeState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.v
}

func (a *atomicState) set(s nodeState) {
	a.mu.Lock()
	a.v = s
	a.mu.Unlock()
}

type certTask struct {
	Running   bool   `json:"running"`
	Stage     string `json:"stage"`    // idle / challenge / verifying / done / error
	Domain    string `json:"domain"`   //
	TXTName   string `json:"txtName"`  // _acme-challenge.xxx
	TXTValue  string `json:"txtValue"` // 要添加到 DNS 的值
	Dir       string `json:"dir"`      // 目标证书目录（多证书时区分）
	Manual    bool   `json:"manual"`   // true=手动模式，等你点继续
	Auto      bool   `json:"auto"`     // true=到期自动触发
	Message   string `json:"message"`  //
	Error     string `json:"error"`    //
	UpdatedAt int64  `json:"updatedAt"`
}

type nodePanel struct {
	path    string
	user    string
	passSalt string
	passHash string
	passIter int
	mustChange bool // 仍在用默认密码
	needSetup  bool   // 首次安装安全锁：还没配过，登录要安装码
	setupCode  string // 一次性安装码（打印在容器日志里）
	setupDone  bool
	secret  string
	certDir string
	staging bool

	mu         sync.Mutex
	task       *certTask
	cfToken    string
	autoDomain string
	renewDays  int
	verifyCh   chan struct{} // 手动模式：用户点「继续」时放行
	stop       chan struct{}
	ddns       *ddnsManager
	gate       *panelLimiter
	savedCfg   *panelConfig // 磁盘上已保存的配置（用于合并）
}

// setupNodePanel 初始化面板；未配置路径或账号密码时返回 nil（不启用）。
func setupNodePanel() *nodePanel {
	// 面板永远启用：路径与账号都有内置默认值，装上就能进（不然新用户无从下手）。
	path := normalizePanelPath(os.Getenv("NODE_PANEL_PATH"))
	if path == "" {
		path = defaultPanelPath
	}
	user := strings.TrimSpace(os.Getenv("NODE_PANEL_USER"))
	if user == "" {
		user = defaultPanelUser
	}
	pass := os.Getenv("NODE_PANEL_PASS")
	mustChange := false
	if pass == "" {
		pass = defaultPanelPass
		mustChange = true
	}
	salt := randomHex(16)
	iter := 120000

	certDir := strings.TrimSpace(os.Getenv("NODE_CERT_DIR"))
	if certDir == "" {
		certDir = "/certs"
	}
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)

	renewDays := 30
	if v := strings.TrimSpace(os.Getenv("NODE_CERT_RENEW_DAYS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 80 {
			renewDays = n
		}
	}

	p := &nodePanel{
		path:       path,
		user:       user,
		passSalt:   salt,
		passHash:   hashPassword(pass, salt, iter),
		passIter:   iter,
		mustChange: mustChange,
		secret:     hex.EncodeToString(buf),
		certDir:   certDir,
		staging:   envBool("NODE_ACME_STAGING"),
		cfToken:   strings.TrimSpace(os.Getenv("CF_DNS_API_TOKEN")),
		renewDays: renewDays,
		task:      &certTask{Stage: "idle"},
		stop:      make(chan struct{}),
		gate:      newPanelLimiter(),
	}

	// 先按 .env 的默认值铺底
	st := nodeState{
		AllowHosts:   splitCSV(os.Getenv("NODE_ALLOW_HOSTS")),
		AllowCIDRs:   splitCSV(os.Getenv("NODE_ALLOW_CIDRS")),
		AllowOrigins: splitCSV(os.Getenv("NODE_ALLOW_ORIGINS")),
	}

	// 面板保存过的配置优先（存在卷里，重启不丢）
	if pc := loadPanelConfig(certDir); pc != nil {
		if pc.CFToken != "" {
			p.cfToken = pc.CFToken
		}
		if pc.AllowHosts != nil {
			st.AllowHosts = pc.AllowHosts
		}
		if pc.AllowCIDRs != nil {
			st.AllowCIDRs = pc.AllowCIDRs
		}
		if pc.AllowOrigins != nil {
			st.AllowOrigins = pc.AllowOrigins
		}
		// 面板里改过账号 / 入口的话以文件为准（重启后依然生效）
		if v := normalizePanelPath(pc.PanelPath); v != "" {
			p.path = v
		}
		if pc.PanelUser != "" && pc.PanelHash != "" {
			p.user = pc.PanelUser
			p.passSalt = pc.PanelSalt
			p.passHash = pc.PanelHash
			p.passIter = pc.PanelIter
			p.mustChange = pc.PanelMustChg
		}
		if pc.PanelSecret != "" {
			p.secret = pc.PanelSecret
		}
		p.savedCfg = pc
		log.Printf("面板配置：已从 %s 恢复（含 %d 条 DDNS 规则）", panelConfigPath(certDir), len(pc.DDNS))
	}
	setPanelPathLive(p.path)

	// 首次安装安全锁：目录里还没有 panel.json（=从没配过）时，
	// 光靠"默认密码 + 公开路径"等于把面板交给全网，所以再要一个一次性安装码。
	// 码打印在 docker logs 里，只有能看服务器日志的人才拿得到；
	// 配过并保存后自动失效，之后只用账号密码。
	p.setupDone = p.savedCfg != nil && p.savedCfg.SetupDone
	if !p.setupDone && p.savedCfg != nil {
		p.setupDone = true // 老版本留下的 panel.json，视为已配置，别把人锁在外面
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NODE_SETUP_LOCK"))) {
	case "0", "off", "false", "no":
		p.setupDone = true
	}
	if !p.setupDone {
		p.needSetup = true
		p.setupCode = strings.TrimSpace(os.Getenv("NODE_SETUP_CODE"))
		if p.setupCode == "" {
			p.setupCode = randomHex(4) // 8 位十六进制
		}
		log.Printf("节点面板：首次安装，面板已上锁。安装码：%s", p.setupCode)
		log.Printf("            登录时把安装码填进去，配置保存后自动失效（NODE_SETUP_LOCK=off 可关闭此锁）")
	}
	nodeLiveAllow.set(st)

	// 续期域名：优先 .env，其次读当前证书里的 CN
	if ds := splitCSV(os.Getenv("NODE_ACME_DOMAINS")); len(ds) > 0 {
		p.autoDomain = strings.ToLower(ds[0])
	}
	if p.autoDomain == "" {
		if list := p.certList(); len(list) > 0 {
			p.autoDomain = strings.ToLower(list[0].Domain)
		}
	}

	// DDNS 复用同一个 CF Token（面板改 Token 后它立刻生效）
	p.ddns = setupDDNS(func() string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.cfToken
	})

	go p.renewLoop()
	return p
}

func splitCSV(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func normalizePanelPath(raw string) string {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	if p == "" {
		return ""
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			log.Fatalf("节点面板：NODE_PANEL_PATH 只允许字母数字与 - _")
		}
	}
	return p
}

/* ---------------- 登录限速 ---------------- */

// 同一 IP 连续失败 5 次锁 5 分钟，挡掉在线字典爆破（默认密码在文档里公开，必须挡）。
type panelAttempt struct {
	fails int
	until time.Time
}

type panelLimiter struct {
	mu sync.Mutex
	m  map[string]*panelAttempt
}

func newPanelLimiter() *panelLimiter {
	return &panelLimiter{m: make(map[string]*panelAttempt)}
}

func (l *panelLimiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a, ok := l.m[ip]; ok && time.Now().Before(a.until) {
		return time.Until(a.until)
	}
	return 0
}

func (l *panelLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[ip]
	if a == nil {
		a = &panelAttempt{}
		l.m[ip] = a
	}
	a.fails++
	if a.fails >= 5 {
		a.until = time.Now().Add(5 * time.Minute)
		a.fails = 0
	}
	// 顺手清掉过期条目，避免 map 无限增长
	if len(l.m) > 2048 {
		now := time.Now()
		for k, v := range l.m {
			if now.After(v.until) && v.fails == 0 {
				delete(l.m, k)
			}
		}
	}
}

func (l *panelLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.m, ip)
	l.mu.Unlock()
}

/* ---------------- 会话 ---------------- */

func (p *nodePanel) sign(exp int64) string {
	body := p.user + "|" + time.Unix(exp, 0).Format(time.RFC3339)
	mac := hmac.New(sha256.New, []byte(p.secret))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *nodePanel) verify(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(p.secret))
	mac.Write(raw)
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return false
	}
	body := string(raw)
	i := strings.LastIndex(body, "|")
	if i < 0 {
		return false
	}
	exp, err := time.Parse(time.RFC3339, body[i+1:])
	if err != nil || time.Now().After(exp) {
		return false
	}
	return body[:i] == p.user
}

func (p *nodePanel) authed(r *http.Request) bool {
	c, err := r.Cookie(nodePanelCookie)
	if err != nil {
		return false
	}
	return p.verify(c.Value)
}

/* ---------------- 路由 ---------------- */

func (p *nodePanel) register(mux *http.ServeMux) {
	root := "/" + p.path
	mux.HandleFunc(root, p.handleIndex)
	mux.HandleFunc(root+"/", p.handleIndex)
	mux.HandleFunc("/api/panel/setup", p.handleSetupInfo)
	mux.HandleFunc("/api/panel/login", p.handleLogin)
	mux.HandleFunc("/api/panel/logout", p.handleLogout)
	mux.HandleFunc("/api/panel/state", p.requireLogin(p.handleState))
	mux.HandleFunc("/api/panel/settings", p.requireLogin(p.handleSettings))
	mux.HandleFunc("/api/panel/cert/start", p.requireLogin(p.handleCertStart))
	mux.HandleFunc("/api/panel/cert/verify", p.requireLogin(p.handleCertVerify))
	mux.HandleFunc("/api/panel/cert/delete", p.requireLogin(p.handleCertDelete))
	mux.HandleFunc("/api/panel/ddns", p.requireLogin(p.handleDDNS))
	mux.HandleFunc("/api/panel/cf", p.requireLogin(p.handleCFToken))
	mux.HandleFunc("/api/panel/account", p.requireLogin(p.handleAccount))
	log.Printf("节点面板：/%s（账号 %s）", p.path, p.user)
	if p.mustChange {
		log.Printf("节点面板：仍在使用默认密码 %s，请登录后到「账号与入口」修改（改完重启仍生效）", defaultPanelPass)
	}
}

func (p *nodePanel) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if b, err := webFS.ReadFile("web/node_panel.html"); err == nil {
		w.Write(b)
		return
	}
	http.Error(w, "panel not found", http.StatusInternalServerError)
}

// handleSetupInfo 公开接口：告诉登录页要不要显示安装码输入框。
func (p *nodePanel) handleSetupInfo(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	need := p.needSetup
	p.mu.Unlock()
	nodeJSON(w, 200, map[string]any{"ok": true, "required": need})
}

func (p *nodePanel) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var body struct {
		User string `json:"user"`
		Pass string `json:"pass"`
		Code string `json:"code"` // 首次安装的安装码
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	ip := realClientIP(r)
	if wait := p.gate.blocked(ip); wait > 0 {
		nodeJSON(w, 429, map[string]any{
			"ok":    false,
			"error": "尝试次数过多，请 " + wait.Round(time.Second).String() + " 后重试",
		})
		return
	}
	if p.needSetup {
		got := strings.TrimSpace(body.Code)
		if got == "" {
			nodeJSON(w, 401, map[string]any{
				"ok":    false,
				"error": "首次安装需要安装码：运行 docker logs <容器名> 查看「安装码」那一行",
				"needSetup": true,
			})
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(p.setupCode)) != 1 {
			p.gate.fail(ip)
			time.Sleep(500 * time.Millisecond)
			nodeJSON(w, 401, map[string]any{"ok": false, "error": "安装码不正确", "needSetup": true})
			return
		}
	}
	okUser := subtle.ConstantTimeCompare([]byte(body.User), []byte(p.user)) == 1
	okPass := subtle.ConstantTimeCompare(
		[]byte(hashPassword(body.Pass, p.passSalt, p.passIter)), []byte(p.passHash)) == 1
	if !okUser || !okPass {
		p.gate.fail(ip)
		time.Sleep(600 * time.Millisecond) // 降低暴力尝试速度
		nodeJSON(w, 401, map[string]any{"ok": false, "error": "账号或密码不正确"})
		return
	}
	p.gate.reset(ip)
	if p.needSetup {
		// 第一次带正确安装码登录成功 → 解锁并立刻落盘，重启后不再要求安装码
		p.mu.Lock()
		p.needSetup = false
		p.setupDone = true
		p.setupCode = ""
		p.mu.Unlock()
		p.persist()
		log.Printf("节点面板：安装码已验证，安全锁已解除（后续只用账号密码）")
	}
	exp := time.Now().Add(12 * time.Hour)
	// Secure 必须跟着当前协议走：没证书时节点是明文 HTTP，
	// 此时种 Secure Cookie 浏览器不会回传，表现就是"登录成功但立刻又未登录"。
	http.SetCookie(w, &http.Cookie{
		Name: nodePanelCookie, Value: p.sign(exp.Unix()),
		Path: "/", HttpOnly: true, Secure: r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge: 12 * 3600,
	})
	nodeJSON(w, 200, map[string]any{"ok": true})
}

func (p *nodePanel) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: nodePanelCookie, Value: "", Path: "/", MaxAge: -1})
	nodeJSON(w, 200, map[string]any{"ok": true})
}

func (p *nodePanel) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.authed(r) {
			nodeJSON(w, 401, map[string]any{"ok": false, "error": "未登录"})
			return
		}
		next(w, r)
	}
}

func (p *nodePanel) handleState(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	task := *p.task
	tokenSet := p.cfToken != ""
	domain := p.autoDomain
	accUser := p.user
	accPath := p.path
	accMust := p.mustChange
	p.mu.Unlock()

	nodeJSON(w, 200, map[string]any{
		"ok":          true,
		"state":       nodeLiveAllow.get(),
		"task":        task,
		"version":     appVersion,
		"certs":       p.certList(),
		"cert":        p.certSummaryFirst(),
		"certDir":     p.certDir,
		"renewDays":   p.renewDays,
		"configFile":  panelConfigPath(p.certDir),
		"autoDomain":  domain,
		"cfTokenSet":  tokenSet,
		"autoRenewOK": tokenSet, // 有 CF Token 才是真正无人值守
		"panelPath":   "/" + accPath,
		"account": map[string]any{
			"user":       accUser,
			"path":       "/" + accPath,
			"mustChange": accMust,
			"defaultPwd": defaultPanelPass, // 仅用于首装提示，改完密码后不再下发
		},
		"hasCert":     len(p.certList()) > 0,
		"ddns":        p.ddns.snapshot(),
	})
}

func (p *nodePanel) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		nodeJSON(w, 405, map[string]any{"ok": false, "error": "方法不允许"})
		return
	}
	var in nodeState
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	for _, c := range in.AllowCIDRs {
		if _, _, err := net.ParseCIDR(c); err != nil && net.ParseIP(c) == nil {
			nodeJSON(w, 400, map[string]any{"ok": false, "error": "IP 段格式不对：" + c})
			return
		}
	}
	nodeLiveAllow.set(nodeState{AllowHosts: in.AllowHosts, AllowCIDRs: in.AllowCIDRs, AllowOrigins: in.AllowOrigins})
	p.persist()
	nodeJSON(w, 200, map[string]any{"ok": true, "note": "已保存并立即生效（重启后仍然保留）"})
}

// handleCFToken 保存/清除 Cloudflare API Token（仅内存，不写盘）。
func (p *nodePanel) handleCFToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		nodeJSON(w, 405, map[string]any{"ok": false, "error": "方法不允许"})
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	token := strings.TrimSpace(in.Token)
	if token != "" {
		// 立刻验证 token 是否可用，避免存进去才发现是坏的
		prov := newCFProvider(token)
		if _, err := prov.do("GET", "/zones?per_page=1", nil); err != nil {
			nodeJSON(w, 400, map[string]any{"ok": false, "error": "Token 不可用：" + err.Error()})
			return
		}
	}
	p.mu.Lock()
	p.cfToken = token
	p.mu.Unlock()
	p.persist()
	msg := "已保存并持久化，之后签发与续期都会自动改 DNS 记录"
	if token == "" {
		msg = "已清除，改回手动模式"
	}
	nodeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

/* ---------------- 证书 ---------------- */

func (p *nodePanel) setTask(fn func(t *certTask)) {
	p.mu.Lock()
	fn(p.task)
	p.task.UpdatedAt = time.Now().Unix()
	p.mu.Unlock()
}

// certSummaryFirst 取第一张证书的摘要（给旧字段 cert 用，保持前端兼容）。
func (p *nodePanel) certSummaryFirst() map[string]any {
	list := p.certList()
	if len(list) == 0 {
		return map[string]any{"state": "none", "hint": "还没有证书，请在下面签发"}
	}
	e := list[0]
	return map[string]any{
		"state":    "ready",
		"domain":   e.Domain,
		"issuer":   e.Issuer,
		"notAfter": e.NotAfter,
		"daysLeft": e.DaysLeft,
		"sans":     e.SANs,
	}
}

// certList 返回当前全部证书（多张并存，按域名分别管理）。
func (p *nodePanel) certList() []certEntry {
	if v, ok := activeCertStore.Load().(*certStore); ok && v != nil {
		return v.Entries()
	}
	return nil
}

// handleCertStart 签发一张新证书。
// 请求体：{"domains":["a.com","b.com"], "dir":"a.com"}，dir 为空则用第一个域名做目录名。
// 一张证书可以覆盖多个域名（SAN），也可以分多次给不同域名各签一张 —— 互不影响。
func (p *nodePanel) handleCertStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		nodeJSON(w, 405, map[string]any{"ok": false, "error": "方法不允许"})
		return
	}
	var in struct {
		Domains []string `json:"domains"`
		Domain  string   `json:"domain"` // 兼容单域名写法
		Dir     string   `json:"dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	raw := in.Domains
	if len(raw) == 0 && in.Domain != "" {
		raw = strings.Split(in.Domain, ",")
	}
	var domains []string
	for _, d := range raw {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if !strings.Contains(d, ".") {
			nodeJSON(w, 400, map[string]any{"ok": false, "error": "域名格式不对：" + d})
			return
		}
		domains = append(domains, d)
	}
	if len(domains) == 0 {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "请填写域名，例如 node1.example.com"})
		return
	}
	dir := strings.TrimSpace(in.Dir)
	if dir == "" {
		dir = domains[0]
	}

	p.mu.Lock()
	if p.task.Running {
		p.mu.Unlock()
		nodeJSON(w, 409, map[string]any{"ok": false, "error": "已有签发任务在进行"})
		return
	}
	hasToken := p.cfToken != ""
	p.task = &certTask{Running: true, Stage: "challenge", Domain: strings.Join(domains, ","), Dir: dir,
		Message: "正在创建订单…", UpdatedAt: time.Now().Unix()}
	p.verifyCh = make(chan struct{})
	p.mu.Unlock()

	go p.issue(domains, dir, hasToken, false)
	nodeJSON(w, 202, map[string]any{"ok": true, "auto": hasToken, "domains": domains, "dir": dir})
}

// handleCertVerify 手动模式：用户已添加 TXT，放行等待中的 Present()。
func (p *nodePanel) handleCertVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		nodeJSON(w, 405, map[string]any{"ok": false, "error": "方法不允许"})
		return
	}
	p.mu.Lock()
	ch := p.verifyCh
	p.mu.Unlock()
	if ch == nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "没有待确认的签发任务"})
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
	p.setTask(func(t *certTask) {
		t.Stage = "verifying"
		t.Manual = false
		t.Message = "已收到，正在校验 DNS…"
	})
	nodeJSON(w, 202, map[string]any{"ok": true})
}

// handleCertDelete 删除指定证书（dir 参数），只影响这一张。
func (p *nodePanel) handleCertDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		nodeJSON(w, 405, map[string]any{"ok": false, "error": "方法不允许"})
		return
	}
	var in struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	dir := strings.TrimSpace(in.Dir)
	if dir == "" {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "请指定要删除哪张证书"})
		return
	}

	p.mu.Lock()
	busy := p.task.Running
	p.mu.Unlock()
	if busy {
		nodeJSON(w, 409, map[string]any{"ok": false, "error": "有签发任务在进行，先等它结束"})
		return
	}

	cs, _ := activeCertStore.Load().(*certStore)
	if cs == nil {
		nodeJSON(w, 500, map[string]any{"ok": false, "error": "证书存储未初始化"})
		return
	}
	if err := cs.Remove(dir); err != nil {
		nodeJSON(w, 500, map[string]any{"ok": false, "error": "删除失败: " + err.Error()})
		return
	}
	log.Printf("节点证书：已删除证书目录 %s（其余证书不受影响）", dir)

	// 还有别的证书就继续用；一张都不剩就得重启成明文 HTTP 才能进面板重签
	left := cs.Entries()
	if len(left) == 0 {
		nodeHasCert.set(false)
		_ = os.WriteFile(filepath.Join(p.certDir, ".no-autorenew"),
			[]byte(time.Now().Format(time.RFC3339)), 0o600)
		log.Printf("节点证书：已没有可用证书，2 秒后重启为明文 HTTP 模式")
		go func() {
			time.Sleep(2 * time.Second)
			os.Exit(0)
		}()
		nodeJSON(w, 200, map[string]any{
			"ok":      true,
			"message": "已删除最后一张证书。节点将重启为明文 HTTP 模式，请用 http:// 重新打开面板签发。",
			"restart": true,
		})
		return
	}

	nodeJSON(w, 200, map[string]any{
		"ok":      true,
		"message": "已删除该证书，其余 " + strconv.Itoa(len(left)) + " 张继续生效",
		"certs":   left,
	})
}

// issue 后台执行签发。
func (p *nodePanel) issue(domains []string, dir string, auto bool, isRenew bool) {
	fail := func(msg string) {
		p.setTask(func(t *certTask) {
			t.Running = false
			t.Stage = "error"
			t.Manual = false
			t.Error = msg
			t.Message = "签发失败"
		})
		log.Printf("节点面板：签发失败 %s", msg)
	}

	var prov dnsProvider
	if auto {
		p.mu.Lock()
		token := p.cfToken
		p.mu.Unlock()
		prov = newCFProvider(token)
	} else {
		prov = &blockingManualProvider{p: p}
	}

	issuer := newACMEIssuer(p.certDir, p.staging, "")
	certPEM, keyPEM, err := issuer.Issue(domains, prov, func(msg string) {
		p.setTask(func(t *certTask) {
			if t.Manual {
				return // 手动阶段不要覆盖掉 TXT 提示
			}
			t.Message = msg
		})
	})
	if err != nil {
		fail(err.Error())
		return
	}

	cs, _ := activeCertStore.Load().(*certStore)
	if cs == nil {
		fail("证书存储未初始化")
		return
	}
	if err := cs.saveTo(dir, certPEM, keyPEM); err != nil {
		fail("写入证书失败: " + err.Error())
		return
	}
	// 有了新证书，解除"禁止自动续期"
	_ = os.Remove(filepath.Join(p.certDir, ".no-autorenew"))

	p.setTask(func(t *certTask) {
		t.Running = false
		t.Stage = "done"
		t.Manual = false
		t.Auto = isRenew
		t.Error = ""
		t.Message = "证书已签发并写入 /certs/" + dir + "，已自动生效"
	})

	// 之前是明文 HTTP 模式的话，现在有证书了必须重启才能切回 HTTPS
	if !nodeHasCert.get() {
		nodeHasCert.set(true)
		log.Printf("节点证书：已获得证书，2 秒后重启以切换到 HTTPS 模式")
		go func() {
			time.Sleep(2 * time.Second)
			os.Exit(0)
		}()
	}
}

// blockingManualProvider：Present 时把 TXT 抛给面板并阻塞，直到用户点「继续」。
type blockingManualProvider struct {
	p *nodePanel
}

func (b *blockingManualProvider) Kind() string { return "manual" }

func (b *blockingManualProvider) Present(fqdn, value string) error {
	b.p.setTask(func(t *certTask) {
		t.Running = false
		t.Stage = "challenge"
		t.Manual = true
		t.TXTName = fqdn
		t.TXTValue = value
		t.Message = "请到 DNS 服务商添加这条 TXT 记录，添加后点「我已添加，继续验证」"
	})
	b.p.mu.Lock()
	ch := b.p.verifyCh
	b.p.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-time.After(30 * time.Minute):
		return fmt.Errorf("等待确认超时（30 分钟），请重新发起签发")
	}
}

func (b *blockingManualProvider) CleanUp(string, string) error { return nil }

/* ---------------- 自动续期 ---------------- */

// renewLoop 每 12 小时检查所有证书，剩余 ≤ 阈值就逐张续期（互不影响）。
func (p *nodePanel) renewLoop() {
	select {
	case <-time.After(time.Minute):
	case <-p.stop:
		return
	}
	for {
		p.checkRenew()
		select {
		case <-time.After(12 * time.Hour):
		case <-p.stop:
			return
		}
	}
}

func (p *nodePanel) checkRenew() {
	if _, err := os.Stat(filepath.Join(p.certDir, ".no-autorenew")); err == nil {
		return // 用户主动删过证书，别自动签回来
	}
	cs, _ := activeCertStore.Load().(*certStore)
	if cs == nil {
		return
	}

	p.mu.Lock()
	busy := p.task.Running
	hasToken := p.cfToken != ""
	p.mu.Unlock()
	if busy {
		return
	}

	for _, e := range cs.Entries() {
		if e.DaysLeft > p.renewDays {
			continue
		}
		domains := e.SANs
		if len(domains) == 0 {
			domains = []string{e.Domain}
		}
		dir := e.Dir
		log.Printf("节点证书：%s 只剩 %d 天，开始续期（%s）", e.Domain, e.DaysLeft,
			map[bool]string{true: "Cloudflare 自动", false: "手动确认"}[hasToken])

		p.mu.Lock()
		p.task = &certTask{Running: true, Stage: "challenge", Domain: e.Domain, Dir: dir, Auto: true,
			Message: "证书即将到期，正在自动续期…", UpdatedAt: time.Now().Unix()}
		p.verifyCh = make(chan struct{})
		p.mu.Unlock()

		go p.issue(domains, dir, hasToken, true)
		return // 一次只处理一张，避免并发干扰
	}
}

/* ---------------- 账号与入口 ---------------- */

// validPanelPath 校验面板路径（字母数字 - _，3~40 位）。
func validPanelPath(raw string) (string, error) {
	v := strings.Trim(strings.TrimSpace(raw), "/")
	if v == "" {
		return "", fmt.Errorf("入口路径不能为空")
	}
	if len(v) < 3 || len(v) > 40 {
		return "", fmt.Errorf("入口路径长度需 3~40 位")
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", fmt.Errorf("入口路径只允许字母、数字与 - _")
		}
	}
	switch v {
	case "api", "admin", "node-panel", "certs":
		return "", fmt.Errorf("这个路径太常见，换一个")
	}
	return v, nil
}

// handleAccount 修改面板账号 / 密码 / 入口。必须验证当前密码。
// 入口改了要重启（路由在启动时注册），密码与账号即时生效且写进 panel.json。
func (p *nodePanel) handleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		nodeJSON(w, 405, map[string]any{"ok": false, "error": "方法不允许"})
		return
	}
	var in struct {
		CurPass string `json:"curPass"`
		User    string `json:"user"`
		NewPass string `json:"newPass"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "参数错误"})
		return
	}

	p.mu.Lock()
	okPass := subtle.ConstantTimeCompare(
		[]byte(hashPassword(in.CurPass, p.passSalt, p.passIter)), []byte(p.passHash)) == 1
	p.mu.Unlock()
	if !okPass {
		time.Sleep(500 * time.Millisecond)
		nodeJSON(w, 401, map[string]any{"ok": false, "error": "当前密码不正确"})
		return
	}

	user := strings.TrimSpace(in.User)
	if user == "" {
		p.mu.Lock()
		user = p.user
		p.mu.Unlock()
	}
	if len(user) < 3 || len(user) > 32 {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": "账号长度需 3~32 位"})
		return
	}
	for _, c := range user {
		if c < 0x21 || c > 0x7e {
			nodeJSON(w, 400, map[string]any{"ok": false, "error": "账号只能用可见 ASCII 字符"})
			return
		}
	}

	newPass := in.NewPass
	if newPass != "" {
		if len(newPass) < 8 {
			nodeJSON(w, 400, map[string]any{"ok": false, "error": "新密码至少 8 位"})
			return
		}
		if newPass == defaultPanelPass {
			nodeJSON(w, 400, map[string]any{"ok": false, "error": "不要使用安装默认密码"})
			return
		}
	}

	p.mu.Lock()
	oldPath := p.path
	p.mu.Unlock()
	path, err := validPanelPath(in.Path)
	if in.Path == "" {
		path, err = oldPath, nil
	}
	if err != nil {
		nodeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	p.mu.Lock()
	p.user = user
	if newPass != "" {
		salt := randomHex(16)
		p.passSalt = salt
		p.passIter = 120000
		p.passHash = hashPassword(newPass, salt, p.passIter)
		p.mustChange = false
	}
	p.path = path
	p.mu.Unlock()

	p.persist()
	// 路径变了：会话仍然有效（same cookie name），但路由要重新注册 → 重启
	if path != oldPath {
		setPanelPathLive(path)
		log.Printf("节点面板：入口改为 /%s，2 秒后重启生效", path)
		nodeJSON(w, 200, map[string]any{
			"ok": true, "restart": true, "path": "/" + path,
			"message": "已保存。面板将重启并改用新入口 /" + path + "，请用新地址重新打开（账号密码不变）。",
		})
		go func() {
			time.Sleep(2 * time.Second)
			os.Exit(0)
		}()
		return
	}
	nodeJSON(w, 200, map[string]any{
		"ok": true, "path": "/" + path,
		"message": "已保存并持久化，重启后依然生效",
	})
}

// handleDDNS 读写 DDNS 规则// handleDDNS 读写 DDNS 规则：GET 看状态，POST 保存配置。
func (p *nodePanel) handleDDNS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		nodeJSON(w, 200, map[string]any{"ok": true, "ddns": p.ddns.snapshot()})
	case http.MethodPost:
		var in struct {
			Entries []ddnsEntry `json:"entries"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in); err != nil {
			nodeJSON(w, 400, map[string]any{"ok": false, "error": "参数错误"})
			return
		}
		p.ddns.setEntries(in.Entries)
		p.persist()
		nodeJSON(w, 200, map[string]any{"ok": true, "message": "已保存并持久化，正在同步 DNS 记录…", "ddns": p.ddns.snapshot()})
	default:
		nodeJSON(w, 405, map[string]any{"ok": false, "error": "方法不允许"})
	}
}

// handleCertDelete 删除当前证书。
//
// 删掉后节点会以【明文 HTTP】重新运行（进程退出、由 docker restart 拉起来），
// 这样你能直接打开面板重新签发 —— 不需要手动改任何配置。
// persist 把当前面板状态写入 panel.json，重启后仍然生效。
func (p *nodePanel) persist() {
	st := nodeLiveAllow.get()
	p.mu.Lock()
	pc := &panelConfig{
		CFToken:      p.cfToken,
		AllowHosts:   st.AllowHosts,
		AllowCIDRs:   st.AllowCIDRs,
		AllowOrigins: st.AllowOrigins,
		RenewDays:    p.renewDays,
		PanelPath:    p.path,
		PanelUser:    p.user,
		PanelSalt:    p.passSalt,
		PanelHash:    p.passHash,
		PanelIter:    p.passIter,
		PanelSecret:  p.secret,
		PanelMustChg: p.mustChange,
		SetupDone:    p.setupDone,
	}
	if p.ddns != nil {
		pc.DDNS = p.ddns.entriesCopy()
	}
	p.mu.Unlock()

	if pc.AllowHosts == nil {
		pc.AllowHosts = []string{}
	}
	if pc.AllowCIDRs == nil {
		pc.AllowCIDRs = []string{}
	}
	if pc.AllowOrigins == nil {
		pc.AllowOrigins = []string{}
	}

	if err := savePanelConfig(p.certDir, pc); err != nil {
		log.Printf("面板配置：保存失败 %v", err)
		return
	}
	p.mu.Lock()
	p.savedCfg = pc
	p.mu.Unlock()
}

func nodeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
