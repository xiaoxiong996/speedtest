package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Node 描述一个测速节点。Local 表示该节点由当前实例自身提供。
// URL 是 IPv4（或双栈域名）接入地址，URL6 是独立的 IPv6 接入地址，
// 两者可以只填一个，前台按访客选择的协议取对应地址。
type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sponsor  string `json:"sponsor"`
	Location string `json:"location"`
	URL      string `json:"url"`
	URL6     string `json:"url6,omitempty"`
	Enabled  bool   `json:"enabled"`
	Local    bool   `json:"local"`
	// Lat / Lon 是可选的节点坐标，填了前台就能按「设置 → 距离」显示访客到节点的距离
	Lat float64 `json:"lat,omitempty"`
	Lon float64 `json:"lon,omitempty"`
}

type Settings struct {
	SiteName        string `json:"siteName"`
	Tagline         string `json:"tagline"`
	// IconURL 是后台上传的自定义站点图标路径（如 /site-icon?v=123）。
	// 为空时用内置默认 favicon.svg；上传接口会写入带版本号的路径以绕过浏览器缓存。
	IconURL         string `json:"iconUrl,omitempty"`
	// TitleSuffix 是标签页标题里站点名后面的文字（如「网络测速」/「Speed Test」），
	// 留空时按当前界面语言使用内置默认文案。
	TitleSuffix     string `json:"titleSuffix,omitempty"`
	DefaultNode     string `json:"defaultNode"`
	DefaultNode6    string `json:"defaultNode6"`
	MaxConnections  int    `json:"maxConnections"`
	DownloadSeconds int    `json:"downloadSeconds"`
	UploadSeconds   int    `json:"uploadSeconds"`
	GaugeMax        int    `json:"gaugeMax"`
	AllowNodeSelect bool   `json:"allowNodeSelect"`
	// AutoSelectNode：前台是否按实测延迟自动选最低的节点；
	// 关闭后按后台节点列表的顺序来（默认节点优先），不自动跳。
	AutoSelectNode  bool   `json:"autoSelectNode"`
	ShowClientIP    bool   `json:"showClientIP"`
	CollectResults  bool   `json:"collectResults"`
	FooterNote      string `json:"footerNote"`
	// AdminPath 是后台入口的隐藏路径段：只有 /<AdminPath> 能打开后台页面，
	// /admin 与 /admin.html 都返回 404。可在后台「站点设置」里随时修改。
	AdminPath string `json:"adminPath"`
	// ProbeV4 / ProbeV6 是单栈补探域名：浏览器切到另一协议时，前端向它们请求
	// /api/isp 拿到访客在该协议下的地址。典型用法是同一站点配两个域名，
	// 一个只解析 A、一个只解析 AAAA（SNI 分流到同一后端），从而强制浏览器走指定协议。
	ProbeV4 string `json:"probeV4,omitempty"`
	ProbeV6 string `json:"probeV6,omitempty"`
	// 前台路由路径段（均可后台自定义）。为空时回落到内置默认值，
	// 这样老配置升级后不会突然改掉已分享出去的链接。
	PathLangZh string `json:"pathLangZh,omitempty"`
	PathLangEn string `json:"pathLangEn,omitempty"`
	PathV4     string `json:"pathV4,omitempty"`
	PathV6     string `json:"pathV6,omitempty"`
	PathResult string `json:"pathResult,omitempty"`
}

type AdminUser struct {
	Username  string    `json:"username"`
	Salt      string    `json:"salt"`
	Hash      string    `json:"hash"`
	Iter      int       `json:"iter"`
	UpdatedAt time.Time `json:"updatedAt"`
	// SessionVersion 每次改密递增，旧会话 token 里带的版本对不上就立即失效。
	SessionVersion int `json:"sessionVersion,omitempty"`
}

type Config struct {
	Admin     AdminUser `json:"admin"`
	Settings  Settings  `json:"settings"`
	Nodes     []Node    `json:"nodes"`
	Secret    string    `json:"secret"`
	InstallID string    `json:"installId"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func defaultSettings() Settings {
	return Settings{
		SiteName:        "SpeedTest",
		Tagline:         "全球网络测速",
		TitleSuffix:     strings.TrimSpace(env("SITE_TITLE_SUFFIX", "")),
		DefaultNode:     "",
		MaxConnections:  6,
		DownloadSeconds: 15,
		UploadSeconds:   15,
		GaugeMax:        1000,
		AllowNodeSelect: true,
		AutoSelectNode:  true,
		ShowClientIP:    true,
		CollectResults:  true,
		FooterNote:      "",
		AdminPath:       defaultAdminPath(),
		ProbeV4:         strings.TrimSpace(env("PROBE_V4", "")),
		ProbeV6:         strings.TrimSpace(env("PROBE_V6", "")),
		PathLangZh:      normalizePathSeg(env("PATH_LANG_ZH", ""), "zh-Hans"),
		PathLangEn:      normalizePathSeg(env("PATH_LANG_EN", ""), "en"),
		PathV4:          normalizePathSeg(env("PATH_V4", ""), "ipv4"),
		PathV6:          normalizePathSeg(env("PATH_V6", ""), "ipv6"),
		PathResult:      normalizePathSeg(env("PATH_RESULT", ""), "result"),
	}
}

// 路由路径段的内置默认值。后台留空或旧配置没有这些字段时回落到这里。
const (
	defPathLangZh = "zh-Hans"
	defPathLangEn = "en"
	defPathV4     = "ipv4"
	defPathV6     = "ipv6"
	defPathResult = "result"
)

// normalizePathSeg 规范化一个路由路径段：只允许字母数字、连字符、下划线，
// 长度 1–32。校验不通过回落到默认值（而不是拒绝保存，避免旧链接失效）。
func normalizePathSeg(v, def string) string {
	v = strings.Trim(strings.TrimSpace(v), "/")
	if v == "" {
		return def
	}
	if len(v) > 32 {
		return def
	}
	for _, c := range v {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return def
	}
	return v
}

// effectivePaths 返回后台配置生效后的全部路由路径段（已回落默认值）。
// 同时保证各段互不重复——撞车的段按顺序回落到默认值，避免路由无法区分。
func effectivePaths(st Settings) (langZh, langEn, v4, v6, result string) {
	langZh = normalizePathSeg(st.PathLangZh, defPathLangZh)
	langEn = normalizePathSeg(st.PathLangEn, defPathLangEn)
	v4 = normalizePathSeg(st.PathV4, defPathV4)
	v6 = normalizePathSeg(st.PathV6, defPathV6)
	result = normalizePathSeg(st.PathResult, defPathResult)
	used := map[string]bool{}
	fix := func(v, def string) string {
		// 依次尝试自定义值、默认值、默认值+序号，取第一个没被占用的。
		candidates := []string{v, def}
		for i := 2; i <= 9; i++ {
			candidates = append(candidates, def+"-"+string(rune('0'+i)))
		}
		for _, c := range candidates {
			if !used[c] {
				used[c] = true
				return c
			}
		}
		return v
	}
	langZh = fix(langZh, defPathLangZh)
	langEn = fix(langEn, defPathLangEn)
	v4 = fix(v4, defPathV4)
	v6 = fix(v6, defPathV6)
	result = fix(result, defPathResult)
	return
}

// 后台入口是一段 3–40 位的字母数字/下划线/连字符，不能撞上任何已有路由或静态文件名。
// 校验不通过返回空串，调用方据此拒绝修改或回落到默认值。
func normalizeAdminPath(v string) string {
	v = strings.Trim(strings.TrimSpace(v), "/")
	if len(v) < 3 || len(v) > 40 {
		return ""
	}
	for _, c := range v {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return ""
	}
	reserved := map[string]bool{
		"admin": true, "api": true, "result": true, "results": true,
		"ipv4": true, "ipv6": true, "zh-hans": true, "en": true,
		"index": true, "index.html": true, "favicon.ico": true, "favicon.svg": true,
		"admin.html": true, "admin.js": true, "admin.css": true,
		"result.html": true, "result.js": true, "result.css": true,
		"style.css": true, "app.js": true, "i18n.js": true, "prefs.js": true,
		"ping": true, "download": true, "upload": true, "report": true,
		"info": true, "isp": true, "servers": true,
	}
	if reserved[strings.ToLower(v)] {
		return ""
	}
	return v
}

func defaultAdminPath() string {
	if v := normalizeAdminPath(env("ADMIN_PATH", "")); v != "" {
		return v
	}
	// 新安装不再使用 README 里人人皆知的固定路径，随机生成并写进 config.json；
	// 登录后台后可随时在「站点设置 → 后台入口」改成自己好记的值。
	return "panel-" + randomHex(4)
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	s.cfg = Config{Settings: defaultSettings()}
	changed := false
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &s.cfg); err != nil {
			return nil, fmt.Errorf("配置文件解析失败: %w", err)
		}
		// 老配置里没有 autoSelectNode 字段：按默认值开启，并落盘
		var probe struct {
			Settings map[string]json.RawMessage `json:"settings"`
		}
		if json.Unmarshal(b, &probe) == nil {
			if _, ok := probe.Settings["autoSelectNode"]; !ok {
				s.cfg.Settings.AutoSelectNode = true
				changed = true
			}
		}
	}
	if s.cfg.Secret == "" {
		s.cfg.Secret = randomHex(32)
		changed = true
	}
	if s.cfg.InstallID == "" {
		s.cfg.InstallID = randomHex(8)
		changed = true
	}
	// 老配置没有 adminPath：补上默认隐藏路径并落盘，避免 /666abc 打不开
	if normalizeAdminPath(s.cfg.Settings.AdminPath) == "" {
		s.cfg.Settings.AdminPath = defaultAdminPath()
		changed = true
	}
	if s.cfg.Admin.Username == "" {
		user := env("ADMIN_USER", "admin")
		pass := os.Getenv("ADMIN_PASSWORD")
		if pass == "" {
			// 不再退回 admin888 这种公开默认值：随机生成并只写进 data/config.json 的哈希，
			// 同时打一条日志提醒管理员首次登录后立即改密。
			pass = randomHex(12)
			log.Printf("未设置 ADMIN_PASSWORD，已生成随机初始密码（仅此一次显示）: %s", pass)
		}
		s.cfg.Admin = newAdmin(user, pass)
		changed = true
	}
	if len(s.cfg.Nodes) == 0 {
		s.cfg.Nodes = []Node{{
			ID:       "node-local",
			Name:     env("NODE_NAME", "默认节点"),
			Sponsor:  env("NODE_SPONSOR", "自建机房"),
			Location: env("NODE_LOCATION", "中国"),
			URL:      normalizeNodeURL(env("NODE_URL", "")),
			URL6:     normalizeNodeURL(env("NODE_URL6", "")),
			Enabled:  true,
			Local:    true,
		}}
		s.cfg.Settings.DefaultNode = "node-local"
		s.cfg.Settings.DefaultNode6 = "node-local"
		changed = true
	}
	if changed {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.Nodes = append([]Node(nil), s.cfg.Nodes...)
	return c
}

func (s *Store) Update(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.cfg); err != nil {
		return err
	}
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// LocalNode 返回当前实例自身节点的信息。
func (s *Store) LocalNode() Node {
	c := s.Get()
	for _, n := range c.Nodes {
		if n.Local {
			return n
		}
	}
	return Node{ID: "node-local", Name: "默认节点"}
}

/* ---------- 密码 ---------- */

func newAdmin(user, pass string) AdminUser {
	salt := randomHex(16)
	iter := 120000
	return AdminUser{
		Username:       user,
		Salt:           salt,
		Hash:           hashPassword(pass, salt, iter),
		Iter:           iter,
		UpdatedAt:      time.Now(),
		SessionVersion: 1,
	}
}

func hashPassword(password, salt string, iter int) string {
	return hex.EncodeToString(pbkdf2SHA256([]byte(password), []byte(salt), iter, 32))
}

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf)
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

/* ---------- 工具 ---------- */

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
