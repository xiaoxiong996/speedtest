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

	// ---- 流量限额 ----
	// 主控按测速报告里浏览器统计的字节数累计「该节点承载的测速流量」。
	// 节点本身不需要改造：报告里已经带 nodeId 与 downloadBytes/uploadBytes。
	TrafficUsed int64 `json:"trafficUsed,omitempty"` // 本计费周期已用字节
	// TrafficLimitGB 为 0 表示不限流量。用 GiB（1024^3）作为换算基准，
	// 与云厂商「每月 1TB」的口径不一定完全一致，这里统一按 GiB 计。
	TrafficLimitGB float64 `json:"trafficLimitGB,omitempty"`
	// TrafficPeriodDay 是每月重置日（1-28），0 表示不自动重置。
	// 只允许到 28，避免 2 月没有 29-31 号导致整月漏重置。
	TrafficPeriodDay int `json:"trafficPeriodDay,omitempty"`
	// TrafficAutoResume 为 true 时，到重置日清零流量后自动把节点重新启用。
	TrafficAutoResume bool `json:"trafficAutoResume,omitempty"`
	// TrafficBlocked 表示该节点是被流量限额自动停用的（区别于后台手动停用），
	// 用于后台区分显示，重置或调高限额后自动解除。
	TrafficBlocked bool `json:"trafficBlocked,omitempty"`
	// TrafficResetAt 是最近一次自动/手动重置的时间（Unix 秒），
	// 用来判断本月是否已经重置过；0 表示从未重置。
	TrafficResetAt int64 `json:"trafficResetAt,omitempty"`
}

// trafficLimitBytes 返回节点的字节限额，0 表示不限。
func (n Node) trafficLimitBytes() int64 {
	if n.TrafficLimitGB <= 0 {
		return 0
	}
	return int64(n.TrafficLimitGB * float64(int64(1)<<30))
}

// trafficOver 判断节点是否已达限额。
func (n Node) trafficOver() bool {
	limit := n.trafficLimitBytes()
	return limit > 0 && n.TrafficUsed >= limit
}

type Settings struct {
	SiteName        string `json:"siteName"`
	Tagline         string `json:"tagline"`
	// IconURL 是后台上传的自定义站点图标路径（如 /site-icon?v=123）。
	// 为空时用内置默认 favicon.svg；上传接口会写入带版本号的路径以绕过浏览器缓存。
	IconURL         string `json:"iconUrl,omitempty"`
	// HideLogoIcon：顶部 logo 只显示文字，不显示任何图标（含默认圆环与自定义图标）。
	// 用于纯文字品牌；勾选后首屏就是纯文字，刷新也不会闪出图标。
	HideLogoIcon    bool   `json:"hideLogoIcon,omitempty"`
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
	// Rating 是体验指标的 5 档评级门槛（后台可调）
	Rating Rating `json:"rating"`
}

// Rating 是「网页 / 游戏 / 视频 / 通话」四个体验指标 + 丢包的 5 档评级门槛，
// 全部由后台调整。
//
// 判定方式：有效延迟 = 延迟 + 抖动 × JitterWeight，再按每个指标从小到大
// 的 4 个阈值切成 5 档（5 档最好、1 档最差）；丢包按百分比各自 4 个阈值。
type Rating struct {
	JitterWeight float64   `json:"jitterWeight"`
	Web          []float64 `json:"web"`
	Game         []float64 `json:"game"`
	Video        []float64 `json:"video"`
	Call         []float64 `json:"call"`
	Loss         []float64 `json:"loss"`
}

func defaultRating() Rating {
	return Rating{
		JitterWeight: 2,
		Web:          []float64{40, 80, 150, 300},
		Game:         []float64{30, 60, 100, 180},
		Video:        []float64{50, 100, 180, 320},
		Call:         []float64{40, 80, 140, 240},
		Loss:         []float64{0.1, 0.5, 1.5, 4},
	}
}

// normalizeRating 把缺字段 / 脏数据拉回可用状态，返回是否发生了修改。
// 老配置没有 rating 字段时这里会整体填入默认值。
func normalizeRating(r *Rating) bool {
	def := defaultRating()
	changed := false
	if !(r.JitterWeight >= 0) || r.JitterWeight > 10 {
		r.JitterWeight = def.JitterWeight
		changed = true
	}
	fix := func(v *[]float64, d []float64, max float64) {
		cur := *v
		ok := len(cur) == len(d)
		if ok {
			for i, x := range cur {
				if !(x >= 0) || x > max || (i > 0 && x < cur[i-1]) {
					ok = false
					break
				}
			}
		}
		if !ok {
			*v = append([]float64(nil), d...)
			changed = true
		}
	}
	fix(&r.Web, def.Web, 60000)
	fix(&r.Game, def.Game, 60000)
	fix(&r.Video, def.Video, 60000)
	fix(&r.Call, def.Call, 60000)
	fix(&r.Loss, def.Loss, 100)
	return changed
}

type AdminUser struct {
	Username  string    `json:"username"`
	Salt      string    `json:"salt"`
	Hash      string    `json:"hash"`
	Iter      int       `json:"iter"`
	UpdatedAt time.Time `json:"updatedAt"`
	// SessionVersion 每次改密递增，旧会话 token 里带的版本对不上就立即失效。
	SessionVersion int `json:"sessionVersion,omitempty"`
	// MustChange 首次安装仍在用内置默认密码时为 true，登录后界面会一直提醒改密，
	// 改完密码自动置回 false（并落盘），重启也不会再提示。
	MustChange bool `json:"mustChange,omitempty"`
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
		Rating:          defaultRating(),
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

// 新安装的默认后台入口：开箱即用，登录后可在「站点设置 → 后台入口」改成任意值，
// 改动写进 data/config.json，重启后仍然生效。
const defaultAdminPathValue = "st-panel"

// 新安装的默认管理员账号 / 密码：固定值，装上就能登录；
// 首次登录后界面会持续提醒修改（改完写进 config.json，重启不丢）。
const (
	defaultAdminUser = "admin"
	defaultAdminPass = "Speedtest@2026"
)

func defaultAdminPath() string {
	if v := normalizeAdminPath(env("ADMIN_PATH", "")); v != "" {
		return v
	}
	return defaultAdminPathValue
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
	// 老配置没有 rating：补默认评级门槛并落盘
	if normalizeRating(&s.cfg.Settings.Rating) {
		changed = true
	}
	if s.cfg.Admin.Username == "" {
		user := env("ADMIN_USER", defaultAdminUser)
		pass := os.Getenv("ADMIN_PASSWORD")
		usingDefault := false
		if pass == "" {
			// 固定默认密码，装上就能登录（否则新用户根本不知道该填什么）。
			// 代价是默认值在文档里公开，所以用 mustChange 一直提醒，登录后改掉即可。
			pass = defaultAdminPass
			usingDefault = true
		}
		s.cfg.Admin = newAdmin(user, pass)
		s.cfg.Admin.MustChange = usingDefault
		if usingDefault {
			log.Printf("首次安装：后台入口 /%s，账号 %s，密码 %s（默认值，请登录后立即修改）",
				s.cfg.Settings.AdminPath, user, pass)
		}
		changed = true
	} else if os.Getenv("ADMIN_PASSWORD") != "" {
		// 账号已经建过：env 里的 ADMIN_PASSWORD 只用于首次初始化，
		// 之后一律以 data/config.json 里的哈希为准，重启不会把后台改过的密码覆盖回去。
		log.Printf("管理员账号已存在，忽略环境变量 ADMIN_PASSWORD（密码以后台设置为准，重启不覆盖）")
	}
	// 主控只做控制台：不再自动生成「本机节点」。
	// 老版本首次安装会塞一个 local=true 的节点（URL 留空时表示“测速走主控自己”），
	// 现在主控不再提供测速带宽，这类没有地址的节点必须清掉；
	// 有地址的 local 节点（历史上把自建节点标成了本机）保留，只是把标记改回普通节点。
	migrated := false
	if len(s.cfg.Nodes) > 0 {
		kept := make([]Node, 0, len(s.cfg.Nodes))
		dropped := map[string]bool{}
		for _, n := range s.cfg.Nodes {
			if n.Local && strings.TrimSpace(n.URL) == "" && strings.TrimSpace(n.URL6) == "" {
				dropped[n.ID] = true
				migrated = true
				continue
			}
			if n.Local {
				n.Local = false
				migrated = true
			}
			kept = append(kept, n)
		}
		if migrated {
			s.cfg.Nodes = kept
			if dropped[s.cfg.Settings.DefaultNode] {
				s.cfg.Settings.DefaultNode = firstNodeID(kept)
			}
			if dropped[s.cfg.Settings.DefaultNode6] {
				s.cfg.Settings.DefaultNode6 = firstNodeID(kept)
			}
		}
	}
	if migrated {
		changed = true
	}
	if changed {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// firstNodeID 返回第一个节点的 ID（用于把失效的默认节点指回来）。
func firstNodeID(nodes []Node) string {
	for _, n := range nodes {
		if n.Enabled {
			return n.ID
		}
	}
	return ""
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

// newAdmin 生成一条新的账号记录（MustChange 默认 false，改密后不该再提示）。
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
