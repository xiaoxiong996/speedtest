package main

// 节点版 DDNS：把域名的 A / AAAA 记录自动指向本机当前公网 IP
//
// 适用场景：家宽 / 动态 IP 的 NAT 机器，重启光猫或拨号换了 IP 后，
// 域名的解析需要跟着变，否则主控连不上节点、证书也续不了。
//
// 实现：复用面板里配的 Cloudflare API Token（同一套权限 Zone.DNS:Edit），
// 定时（默认 5 分钟）做三件事：
//   1. 查本机公网 IP（v4 与 v6 分别查）
//   2. 查 Cloudflare 上该域名当前的记录值
//   3. 不一致就 PATCH 更新（没有记录就创建）
//
// 只在值变化时才调用 API，不会疯狂刷 Cloudflare 的请求。

import (
	"context"
	"encoding/json"
	"fmt"
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

// ddnsEntry 一条 DDNS 规则：哪个域名、哪种记录。
type ddnsEntry struct {
	Domain string `json:"domain"` // 例如 speeda.example.com
	Type   string `json:"type"`   // A 或 AAAA
	// 运行期状态（只读，由 check 填）
	CurrentIP string `json:"currentIp,omitempty"` // 本机公网 IP
	DNSIP     string `json:"dnsIp,omitempty"`     // Cloudflare 上当前值
	State     string `json:"state,omitempty"`     // ok / updated / error / pending
	Message   string `json:"message,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

type ddnsManager struct {
	mu      sync.Mutex
	entries []ddnsEntry
	enabled bool
	minutes int
	token   func() string // 取当前 CF Token（面板可能随时改）

	lastRun atomic2Int64
	stop    chan struct{}
}

type atomic2Int64 struct {
	mu sync.RWMutex
	v  int64
}

func (a *atomic2Int64) set(v int64) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomic2Int64) get() int64  { a.mu.RLock(); defer a.mu.RUnlock(); return a.v }

// parseDDNSEnv 解析 NODE_DDNS，格式："域名:A,域名2:AAAA"（逗号分隔，类型可省，默认 A）
func parseDDNSEnv(raw string) []ddnsEntry {
	var out []ddnsEntry
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		domain, typ := item, "A"
		if i := strings.LastIndex(item, ":"); i > 0 {
			domain = strings.TrimSpace(item[:i])
			typ = strings.ToUpper(strings.TrimSpace(item[i+1:]))
		}
		if domain == "" || !strings.Contains(domain, ".") {
			continue
		}
		if typ != "A" && typ != "AAAA" {
			typ = "A"
		}
		out = append(out, ddnsEntry{Domain: strings.ToLower(domain), Type: typ, State: "pending"})
	}
	return out
}

func setupDDNS(token func() string) *ddnsManager {
	// 面板保存过的规则优先（重启后仍生效）；没有才用 .env 的 NODE_DDNS
	var entries []ddnsEntry
	if pc := loadPanelConfig(env("NODE_CERT_DIR", "/certs")); pc != nil && len(pc.DDNS) > 0 {
		for _, e := range pc.DDNS {
			e.State = "pending"
			e.CurrentIP, e.DNSIP, e.Message, e.UpdatedAt = "", "", "", 0
			entries = append(entries, e)
		}
	} else {
		entries = parseDDNSEnv(os.Getenv("NODE_DDNS"))
	}
	minutes := 5
	if v := strings.TrimSpace(os.Getenv("NODE_DDNS_INTERVAL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 1440 {
			minutes = n
		}
	}
	m := &ddnsManager{
		entries: entries,
		enabled: len(entries) > 0,
		minutes: minutes,
		token:   token,
		stop:    make(chan struct{}),
	}
	if m.enabled {
		log.Printf("节点 DDNS：已启用，%d 条规则，每 %d 分钟检查一次", len(entries), minutes)
		go m.loop()
	}
	return m
}

func (m *ddnsManager) loop() {
	// 启动后先等 20 秒，避开启动时的证书检查
	select {
	case <-time.After(20 * time.Second):
	case <-m.stop:
		return
	}
	m.checkAll()
	tk := time.NewTicker(time.Duration(m.minutes) * time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-tk.C:
			m.checkAll()
		case <-m.stop:
			return
		}
	}
}

// snapshot 给面板展示。
func (m *ddnsManager) snapshot() map[string]any {
	m.mu.Lock()
	list := make([]ddnsEntry, len(m.entries))
	copy(list, m.entries)
	m.mu.Unlock()
	return map[string]any{
		"enabled":   m.enabled,
		"interval":  m.minutes,
		"entries":   list,
		"lastRunAt": m.lastRun.get(),
	}
}

// entriesCopy 返回规则副本（持久化用）。
func (m *ddnsManager) entriesCopy() []ddnsEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ddnsEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// setEntries 面板改配置。
func (m *ddnsManager) setEntries(in []ddnsEntry) {
	var clean []ddnsEntry
	for _, e := range in {
		d := strings.ToLower(strings.TrimSpace(e.Domain))
		t := strings.ToUpper(strings.TrimSpace(e.Type))
		if d == "" || !strings.Contains(d, ".") {
			continue
		}
		if t != "A" && t != "AAAA" {
			t = "A"
		}
		clean = append(clean, ddnsEntry{Domain: d, Type: t, State: "pending"})
	}
	m.mu.Lock()
	m.entries = clean
	m.enabled = len(clean) > 0
	m.mu.Unlock()
	if m.enabled {
		go m.checkAll() // 保存后立刻跑一次
	}
}

func (m *ddnsManager) checkAll() {
	m.mu.Lock()
	entries := make([]ddnsEntry, len(m.entries))
	copy(entries, m.entries)
	enabled := m.enabled
	m.mu.Unlock()
	if !enabled {
		return
	}

	token := m.token()
	if token == "" {
		m.markAll("error", "没有配置 Cloudflare API Token，无法更新 DNS")
		return
	}

	prov := newCFProvider(token)
	// 一次运行里只查一次本机 IP
	ipCache := map[string]string{}

	for i := range entries {
		e := entries[i]
		ip := ipCache[e.Type]
		if ip == "" {
			v, err := publicIP(e.Type)
			if err != nil {
				m.update(i, func(x *ddnsEntry) {
					x.State = "error"
					x.Message = "查本机公网 IP 失败: " + err.Error()
				})
				continue
			}
			ip = v
			ipCache[e.Type] = v
		}
		if ip == "" {
			m.update(i, func(x *ddnsEntry) {
				x.State = "error"
				x.Message = "本机没有 " + e.Type + " 地址（该协议不可用）"
			})
			continue
		}

		cur, err := prov.getRecord(e.Domain, e.Type)
		if err != nil {
			m.update(i, func(x *ddnsEntry) {
				x.State = "error"
				x.Message = err.Error()
			})
			continue
		}
		if cur == ip {
			m.update(i, func(x *ddnsEntry) {
				x.CurrentIP = ip
				x.DNSIP = cur
				x.State = "ok"
				x.Message = "已是最新"
			})
			continue
		}

		if err := prov.upsertRecord(e.Domain, e.Type, ip); err != nil {
			m.update(i, func(x *ddnsEntry) {
				x.CurrentIP = ip
				x.DNSIP = cur
				x.State = "error"
				x.Message = "更新失败: " + err.Error()
			})
			continue
		}
		log.Printf("节点 DDNS：%s 的 %s 记录 %s → %s", e.Domain, e.Type, emptyDash(cur), ip)
		m.update(i, func(x *ddnsEntry) {
			x.CurrentIP = ip
			x.DNSIP = ip
			x.State = "updated"
			x.Message = "已更新为 " + ip
		})
	}
	m.lastRun.set(time.Now().Unix())
}

// update 按下标改某条的状态（用下标避免并发时改错行）。
func (m *ddnsManager) update(idx int, fn func(*ddnsEntry)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx < 0 || idx >= len(m.entries) {
		return
	}
	fn(&m.entries[idx])
	m.entries[idx].UpdatedAt = time.Now().Unix()
}

func (m *ddnsManager) markAll(state, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.entries {
		m.entries[i].State = state
		m.entries[i].Message = msg
		m.entries[i].UpdatedAt = time.Now().Unix()
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "(无记录)"
	}
	return s
}

/* ---------------- 公网 IP 探测 ---------------- */

// publicIP 查本机对外公网 IP。A 走 v4 端点，AAAA 走 v6 端点。
// 用多个端点兜底，避免某一个服务挂了就整个 DDNS 失效。
func publicIP(recType string) (string, error) {
	var urls []string
	if recType == "AAAA" {
		urls = []string{
			"https://api6.ipify.org",
			"https://v6.ident.me",
			"https://[2606:4700:4700::1111]/cdn-cgi/trace",
		}
	} else {
		urls = []string{
			"https://api.ipify.org",
			"https://v4.ident.me",
			"https://1.1.1.1/cdn-cgi/trace",
		}
	}
	var lastErr error
	for _, u := range urls {
		ip, err := fetchIP(u)
		if err != nil {
			lastErr = err
			continue
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			lastErr = fmt.Errorf("%s 返回的不是 IP: %q", u, ip)
			continue
		}
		// 族校验：查 A 却拿到 v6（说明本机走了 v6 出口）就换下一个端点
		if recType == "AAAA" && parsed.To4() != nil {
			lastErr = fmt.Errorf("%s 返回了 IPv4，与记录类型不符", u)
			continue
		}
		if recType == "A" && parsed.To4() == nil {
			lastErr = fmt.Errorf("%s 返回了 IPv6，与记录类型不符", u)
			continue
		}
		return parsed.String(), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有探测端点都失败")
	}
	return "", lastErr
}

func fetchIP(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "speedtest-node-ddns")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	text := strings.TrimSpace(string(body))

	// ipify/ident.me 直接返回纯 IP；Cloudflare trace 是 key=value 多行
	if strings.Contains(text, "ip=") {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "ip=") {
				return strings.TrimSpace(strings.TrimPrefix(line, "ip=")), nil
			}
		}
	}
	// 有些端点返回 JSON
	if strings.HasPrefix(text, "{") {
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			if v, ok := m["ip"].(string); ok {
				return v, nil
			}
		}
	}
	return text, nil
}
