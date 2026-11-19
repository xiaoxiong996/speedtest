package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookieName = "speedtest_session"

type sessionPayload struct {
	User string `json:"u"`
	Exp  int64  `json:"e"`
	// V 是管理员密码版本，改密后旧 token 即使没过期也会被拒绝。
	V int `json:"v"`
}

func (s *Server) signSession(user string, ttl time.Duration) string {
	cfg := s.store.Get()
	raw, _ := json.Marshal(sessionPayload{User: user, Exp: time.Now().Add(ttl).Unix(), V: cfg.Admin.SessionVersion})
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// sessionDuration 是登录有效期。后台是低频操作的工具站，
// 12 小时太短（第二天刷新就掉登录），这里放宽到 30 天并做滑动续期。
const sessionDuration = 30 * 24 * time.Hour

// sessionRenewBefore：剩余有效期少于这个值就顺手换一张新票据（滑动续期），
// 只要还在用就不会掉登录。
const sessionRenewBefore = 7 * 24 * time.Hour

func (s *Server) verifySession(token string) (sessionPayload, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return sessionPayload{}, false
	}
	mac := hmac.New(sha256.New, []byte(s.store.Get().Secret))
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return sessionPayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return sessionPayload{}, false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return sessionPayload{}, false
	}
	cfg := s.store.Get()
	if p.Exp < time.Now().Unix() {
		return sessionPayload{}, false
	}
	if p.V != cfg.Admin.SessionVersion {
		return sessionPayload{}, false
	}
	if p.User != cfg.Admin.Username {
		return sessionPayload{}, false
	}
	return p, true
}

// firstValidSession 在同名 Cookie 有多份时，返回第一份「验签通过」的。
//
// 为什么需要：浏览器可能同时带着「宿主级」和「Domain=父域」两份 speedtest_session
// （改过域名作用域、或从不同子域登录过），Cookie 头的先后顺序由浏览器决定。
// 只取 r.Cookie() 拿到的那一份，就可能时而正常、时而 401 —— 表现就是刷新掉登录。
func (s *Server) firstValidSession(r *http.Request) (sessionPayload, bool) {
	for _, c := range r.Cookies() {
		if c.Name != sessionCookieName || c.Value == "" {
			continue
		}
		if p, ok := s.verifySession(c.Value); ok {
			return p, true
		}
	}
	return sessionPayload{}, false
}

func (s *Server) isAuthed(r *http.Request) bool {
	_, ok := s.firstValidSession(r)
	return ok
}

// adminCookieDomain 返回后台会话 Cookie 的作用域：
//   · ADMIN_COOKIE_DOMAIN 显式配置时用它（例如 .example.com，让 speed.example.com 与
//     speedtest.example.com 共用同一份登录态）
//   · 没配就是空 → 宿主级 Cookie，只在签发它的那个域名下有效
//
// 为什么需要它：站点可能同时有多个子域名指向同一份后台（历史域名、加速域名），
// 宿主级 Cookie 换个子域名就「刷新变未登录」，表现很像会话失效。
func adminCookieDomain() string {
	d := strings.TrimSpace(os.Getenv("ADMIN_COOKIE_DOMAIN"))
	if d == "" {
		return ""
	}
	if !strings.HasPrefix(d, ".") {
		d = "." + d
	}
	return d
}

// setSessionCookie 统一登录/续期的 Cookie 属性，避免两处写法不一致。
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Domain:   adminCookieDomain(),
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration / time.Second),
	})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.firstValidSession(r)
		if !ok {
			n := 0
			for _, c := range r.Cookies() {
				if c.Name == sessionCookieName {
					n++
				}
			}
			// 排障用：Host + 同名 Cookie 份数，能直接看出是不是换域名/多份 Cookie 打架
			log.Printf("[auth] 401 host=%s cookies=%d ip=%s", r.Host, n, clientIP(r))
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "登录已失效，请重新登录"})
			return
		}
		// 滑动续期：快到期就换新票据，一路用下去不会突然掉登录
		if time.Until(time.Unix(p.Exp, 0)) < sessionRenewBefore {
			s.setSessionCookie(w, r, s.signSession(p.User, sessionDuration))
		}
		next(w, r)
	}
}

// sameIP 判断两个地址是否指向同一个 IP（兼容 IPv6 的不同写法）。
func sameIP(a, b string) bool {
	ia := net.ParseIP(strings.Trim(a, "[]"))
	ib := net.ParseIP(strings.Trim(b, "[]"))
	if ia == nil || ib == nil {
		return false
	}
	return ia.Equal(ib)
}

// isResultOwner 判断当前请求是不是「跑出这份报告的那个人」。
// 两种认法：
//  1. 请求头带着报告专属凭证 —— 测速成功时写进测速浏览器的 localStorage，
//     别人只拿到报告链接是拿不到凭证的；
//  2. 来源 IP 与记录里的测速者一致 —— 同网络下换设备打开，看到的本来也是自己的地址。
//
// 都不满足时视为「别人在看这份报告」，IP 不下发。
func (s *Server) isResultOwner(r *http.Request, res Result) bool {
	if res.OwnerKey != "" {
		if tok := strings.TrimSpace(r.Header.Get("X-Result-Owner")); tok != "" {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(res.OwnerKey)) == 1 {
				return true
			}
		}
	}
	return res.IP != "" && sameIP(clientIP(r), res.IP)
}

/* ---------- 登录限流 ---------- */

type attemptRecord struct {
	count int
	until time.Time
}

type loginLimiter struct {
	mu   sync.Mutex
	data map[string]*attemptRecord
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{data: map[string]*attemptRecord{}}
}

func (l *loginLimiter) blocked(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.data[key]
	if !ok {
		return 0
	}
	if rec.count >= 8 && time.Now().Before(rec.until) {
		return time.Until(rec.until)
	}
	if time.Now().After(rec.until) {
		delete(l.data, key)
	}
	return 0
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.data[key]
	if !ok {
		rec = &attemptRecord{}
		l.data[key] = rec
	}
	rec.count++
	rec.until = time.Now().Add(10 * time.Minute)
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.data, key)
}

/* ---------- 匿名接口限流 ---------- */

type opRecord struct {
	count int
	until time.Time
}

type opLimiter struct {
	mu   sync.Mutex
	data map[string]*opRecord
}

func newOpLimiter() *opLimiter {
	return &opLimiter{data: map[string]*opRecord{}}
}

// allow 在 window 时间内最多放行 limit 次；超限时返回 false。
// 按 IP + 接口名分桶，避免单个访客刷报告、刷评分。
func (l *opLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	rec, ok := l.data[key]
	if !ok || now.After(rec.until) {
		rec = &opRecord{until: now.Add(window)}
		l.data[key] = rec
		// 顺手清理过期桶，避免长期运行后 map 无限增长
		if len(l.data) > 4096 {
			for k, v := range l.data {
				if now.After(v.until) {
					delete(l.data, k)
				}
			}
		}
	}
	if rec.count >= limit {
		return false
	}
	rec.count++
	return true
}

func limitKey(r *http.Request, prefix string) string {
	ip := clientIP(r)
	if ip == "" {
		ip = normalizeIP(r.RemoteAddr)
	}
	if ip == "" {
		ip = r.RemoteAddr
	}
	return prefix + ":" + ip
}

/* ---------- HTTP 小工具 ---------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

// 访客地址解析：先看连接来源，再按可信头取值。
//   - 来源是公网且不在 TRUSTED_PROXIES：说明是真直连，一律以 RemoteAddr 为准，
//     防止任何人自填头伪造地址（会把登录限流、报告归属一起绕过）。
//   - 来源在 TRUSTED_PROXIES（含内置 CDN 回源段）：按头优先级取真实访客地址。
//   - 来源是回环 / 私网但没配进 TRUSTED_PROXIES：说明前面确实有反代（nginx、
//     Docker 网关、Lucky 换网段等），退一步采信头里的公网地址，避免把访客
//     地址显示成空白。
func clientIP(r *http.Request) string {
	remote := normalizeIP(r.RemoteAddr)
	if remote != "" && isTrustedProxy(remote) {
		if ip := headerClientIP(r); ip != "" {
			return ip
		}
		return remote
	}
	if isPublicIP(remote) {
		return remote
	}
	// 回环 / 私网来源：反代就在前面，头里带的是访客地址
	if ip := headerClientIP(r); ip != "" {
		return ip
	}
	return ""
}

// headerClientIP 按 TRUSTED_IP_HEADERS 的顺序取访客地址。
// 默认顺序覆盖 Cloudflare、阿里云 CDN、腾讯云 EdgeOne、通用 X-Real-IP，
// X-Forwarded-For 放最后兜底（解析方式不同，单独处理）。
func headerClientIP(r *http.Request) string {
	for _, h := range trustedIPHeaders() {
		if strings.EqualFold(h, "X-Forwarded-For") {
			if ip := pickFromXFF(r); ip != "" {
				return ip
			}
			continue
		}
		for _, raw := range headerValues(r, h) {
			if ip := normalizeIP(raw); ip != "" && isPublicIP(ip) {
				return ip
			}
		}
	}
	return ""
}

// headerValues 取某个头的全部值：同名多行 + 逗号分隔（部分 CDN 会把多个
// 地址塞进同一个头里）都拆开，按出现顺序返回。
func headerValues(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.Header.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// pickFromXFF 从 X-Forwarded-For 里挑出真实访客地址。
// 代理是「每跳往右追加」的，所以从右往左走：跳过可信代理（CF / 阿里 / 腾讯
// 的边缘节点、本机反代）和私有地址，第一个既公网又不可信的地址就是访客；
// 整条链都不可信时再退回最左边那个公网地址。
func pickFromXFF(r *http.Request) string {
	cands := clientIPChain(r) // 左 → 右，末尾是 RemoteAddr
	// 最左是原始客户端：CDN 回源时会用它自己看到的地址重写这一行，
	// 后面追加的都是中间代理，所以先取最左边那个公网地址。
	for _, ip := range cands {
		if isPublicIP(ip) && !isTrustedProxy(ip) {
			return ip
		}
	}
	// 整条链都在可信段里：退一步从右往左取第一个公网地址
	for i := len(cands) - 1; i >= 0; i-- {
		if isPublicIP(cands[i]) {
			return cands[i]
		}
	}
	return ""
}

var (
	trustedProxyOnce sync.Once
	trustedProxyNets []*net.IPNet
	trustedIPHeaderNames []string
)

func splitListEnv(key, def string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		raw = def
	}
	out := make([]string, 0, 4)
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func trustedIPHeaders() []string {
	parseTrustedProxies()
	return trustedIPHeaderNames
}

// defaultIPHeaders 是各家 CDN 用来传访客真实地址的头，按可信度排序：
// Cloudflare / 阿里云 CDN / 腾讯云 EdgeOne / 通用反代，X-Forwarded-For 兜底。
const defaultIPHeaders = "CF-Connecting-IP,True-Client-IP,Ali-Cdn-Real-Ip,Ali-Real-Ip,EO-Connecting-IP,X-Forwarded-For,X-Real-IP"

// cloudflareNets 是 Cloudflare 官方回源网段（cloudflare.com/ips）。
// 站点挂在 CF 后面、回源直连源站（不经本机反代）时，RemoteAddr 会是这些段里
// 的边缘地址；只有把它们算作可信代理，才敢采信 CF-Connecting-IP。
// 换用别的 CDN 时，CDN 边缘地址段通过 TRUSTED_PROXIES 补充即可。
var cloudflareNets = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

func parseTrustedProxies() {
	trustedProxyOnce.Do(func() {
		trustedIPHeaderNames = splitListEnv("TRUSTED_IP_HEADERS", defaultIPHeaders)
		raw := os.Getenv("TRUSTED_PROXIES")
		// 内置的 CDN 回源段默认启用（可 TRUST_CDN_PRESETS=off 关闭）
		if !strings.EqualFold(strings.TrimSpace(os.Getenv("TRUST_CDN_PRESETS")), "off") {
			raw = raw + "," + strings.Join(cloudflareNets, ",")
		}
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if !strings.Contains(item, "/") {
				if ip := net.ParseIP(item); ip != nil {
					bits := 128
					if ip.To4() != nil {
						bits = 32
					}
					item += "/" + strconv.Itoa(bits)
				}
			}
			if _, n, err := net.ParseCIDR(item); err == nil {
				trustedProxyNets = append(trustedProxyNets, n)
			} else {
				log.Printf("忽略无效的 TRUSTED_PROXIES 条目: %q", item)
			}
		}
	})
}

func isTrustedProxy(ip string) bool {
	parseTrustedProxies()
	if len(trustedProxyNets) == 0 {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range trustedProxyNets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// isSecureRequest 判断当前请求是否走 HTTPS（含可信代理终止 TLS 的情况），
// 用来决定会话 Cookie 要不要带 Secure 标记。
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if isTrustedProxy(normalizeIP(r.RemoteAddr)) {
		p := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
		return p == "https"
	}
	return false
}

// clientIPChain 按优先级列出所有可能的来源地址（已规范化，未去重过滤）。
func clientIPChain(r *http.Request) []string {
	out := make([]string, 0, 6)
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(v, ",") {
			if ip := normalizeIP(part); ip != "" {
				out = append(out, ip)
			}
		}
	}
	if ip := normalizeIP(r.RemoteAddr); ip != "" {
		out = append(out, ip)
	}
	return out
}

// publicFromChain 取候选里第一个公网地址；都拿不到时返回空串（表示无法确定访客真实地址）。
func publicFromChain(cands []string) string {
	for _, ip := range cands {
		if isPublicIP(ip) {
			return ip
		}
	}
	return ""
}

// normalizeIP 去掉端口、方括号与 IPv6 zone，校验后返回纯 IP 字符串。
func normalizeIP(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		// 形如 1.2.3.4:5678 / [2001:db8::1]:5678
		s = host
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if i := strings.IndexByte(s, '%'); i > 0 {
		s = s[:i]
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// isPublicIP 判断是否为可在互联网路由的地址。
// 回环、私有网段（含 Docker 默认 172.16/12）、链路本地、保留段都视为非公网。
func isPublicIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 10:
			return false
		case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
			return false
		case v4[0] == 192 && v4[1] == 168:
			return false
		case v4[0] == 169 && v4[1] == 254:
			return false
		case v4[0] == 0:
			return false
		case v4[0] >= 224:
			return false
		}
		return true
	}
	// IPv6：fc00::/7 唯一本地地址、fe80::/10 链路本地
	if len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc {
			return false
		}
	}
	return true
}

// ipFamily 返回地址所属协议族："v4" / "v6"，无法判断时返回空串。
func ipFamily(s string) string {
	ip := net.ParseIP(strings.Trim(s, "[]"))
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return "v4"
	}
	return "v6"
}

// hostAddresses 列出本机可供外部访问的地址（每族公网在前、内网在后）。
// 前台在切换 IPv4 / IPv6 时用它补探访客地址：
// 页面若是走 IPv6 打开的，取访客的 IPv4 地址只能另发一个 IPv4 请求，
// 这里把本机的 v4 / v6 地址交给浏览器去拼目标地址。
func hostAddresses() (v4, v6 []string) {
	type cand struct {
		ip   string
		priv bool
	}
	var c4, c6 []cand
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() ||
				ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
				continue
			}
			s := ip.String()
			priv := !isPublicIP(s)
			if v4ip := ip.To4(); v4ip != nil {
				s = v4ip.String()
				c4 = append(c4, cand{s, priv})
			} else {
				c6 = append(c6, cand{s, priv})
			}
		}
	}
	// 有公网地址时只给公网（私网地址在前台往往不可达，探测会白等超时）；
	// 纯内网部署（没有公网地址）才退回私网地址。
	ordered := func(cs []cand) []string {
		var pub, priv []string
		for _, c := range cs {
			if c.priv {
				priv = append(priv, c.ip)
			} else {
				pub = append(pub, c.ip)
			}
		}
		if len(pub) > 0 {
			return pub
		}
		if len(priv) > 2 {
			priv = priv[:2]
		}
		return priv
	}
	return ordered(c4), ordered(c6)
}

var nodeCORSOrigins []string

// setCORSOrigins 由节点模式在启动时调用一次，限定 Access-Control-Allow-Origin。
// 主控模式不调用，保持原有的 * 行为。
func setCORSOrigins(origins []string) {
	nodeCORSOrigins = origins
}

func setCORS(w http.ResponseWriter) {
	h := w.Header()
	if len(nodeCORSOrigins) == 0 {
		h.Set("Access-Control-Allow-Origin", "*")
	}
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Range, X-Result-Owner")
	h.Set("Access-Control-Max-Age", "86400")
}

func noStore(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")
}
