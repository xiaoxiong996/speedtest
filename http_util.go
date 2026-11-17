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

func (s *Server) verifySession(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.store.Get().Secret))
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	cfg := s.store.Get()
	if p.Exp < time.Now().Unix() {
		return false
	}
	if p.V != cfg.Admin.SessionVersion {
		return false
	}
	return p.User == cfg.Admin.Username
}

func (s *Server) isAuthed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return s.verifySession(c.Value)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthed(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "登录已失效，请重新登录"})
			return
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

// 只有连接来源是 TRUSTED_PROXIES 里的可信代理时，才采信 TRUSTED_IP_HEADERS
// 指定的头（默认只有 X-Real-IP，要求反代覆盖写入，不能透传访客自填的值）；
// 直连场景一律以 RemoteAddr 为准，避免任何人伪造头绕过登录限流或报告归属校验。
func clientIP(r *http.Request) string {
	remote := normalizeIP(r.RemoteAddr)
	if remote == "" || !isTrustedProxy(remote) {
		if isPublicIP(remote) {
			return remote
		}
		return ""
	}
	for _, h := range trustedIPHeaders() {
		if strings.EqualFold(h, "X-Forwarded-For") {
			// 走代理链时取最靠近代理的公网地址；客户端自己塞在前面的值不算。
			cands := clientIPChain(r)
			for i := len(cands) - 1; i >= 0; i-- {
				if isPublicIP(cands[i]) {
					return cands[i]
				}
			}
			continue
		}
		if ip := normalizeIP(r.Header.Get(h)); ip != "" && isPublicIP(ip) {
			return ip
		}
	}
	return remote
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

func parseTrustedProxies() {
	trustedProxyOnce.Do(func() {
		trustedIPHeaderNames = splitListEnv("TRUSTED_IP_HEADERS", "X-Real-IP")
		for _, raw := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if !strings.Contains(raw, "/") {
				if ip := net.ParseIP(raw); ip != nil {
					bits := 128
					if ip.To4() != nil {
						bits = 32
					}
					raw += "/" + strconv.Itoa(bits)
				}
			}
			if _, n, err := net.ParseCIDR(raw); err == nil {
				trustedProxyNets = append(trustedProxyNets, n)
			} else {
				log.Printf("忽略无效的 TRUSTED_PROXIES 条目: %q", raw)
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
	if strings.Contains(s, ":") && !strings.Contains(s, "::") {
		// 形如 1.2.3.4:5678 或 [::1]:5678
		if host, _, err := net.SplitHostPort(s); err == nil {
			s = host
		}
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
