package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"
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
	c := &nodeConfig{prefix: normalizeNodePrefix(os.Getenv("NODE_PATH_PREFIX"))}

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
			log.Printf("节点：忽略无效 IP 段 %q", raw)
		}
	}

	for _, raw := range splitListEnv("NODE_ALLOW_HOSTS", "") {
		h := strings.ToLower(strings.TrimSpace(raw))
		if h != "" {
			c.allowHosts = append(c.allowHosts, h)
		}
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
	if len(c.allowHosts) == 0 {
		return true
	}
	h := hostNameOf(r)
	for _, want := range c.allowHosts {
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
	if len(c.allowCIDRs) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range c.allowCIDRs {
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

// nodeGuard 是节点模式的统一入口：安全头 → 域名/IP 白名单 → CORS。
// 校验失败一律 403，不进入测速逻辑，也不产生任何写入。
func (c *nodeConfig) nodeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")

		// 健康检查只供本机/docker 探活，豁免域名与 IP 白名单。
		if r.URL.Path == "/healthz" {
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
		if !c.ipAllowed(clientIP(r)) {
			deny("ip not allowed")
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
	mux.HandleFunc(base+"/api/ping", srv.handlePing)
	mux.HandleFunc(base+"/api/isp", srv.handleISP)
	mux.HandleFunc(base+"/api/download", srv.handleDownload)
	mux.HandleFunc(base+"/api/upload", srv.handleUpload)

	addr := net.JoinHostPort(env("HTTP_HOST", "0.0.0.0"), env("HTTP_PORT", "3333"))
	hs := &http.Server{
		Addr:              addr,
		Handler:           cfg.nodeGuard(mux),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("测速节点已启动 %s (版本 %s, 前缀 %q, IP段 %d, 域名 %d, Origin %d)",
		addr, appVersion, cfg.prefix, len(cfg.allowCIDRs), len(cfg.allowHosts), len(cfg.allowOrigins))
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("节点启动失败: %v", err)
	}
}
