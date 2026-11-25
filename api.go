package main

import (
	"crypto/subtle"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Server) apiRouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api")
	path = strings.TrimSuffix(path, "/")

	// 匿名写接口限流：报告提交和评分都不需要登录，必须防刷。
	if path == "/report" && r.Method == http.MethodPost &&
		!s.ops.allow(limitKey(r, "report"), 20, 10*time.Minute) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "提交过于频繁，请稍后再试"})
		return
	}
	if strings.HasPrefix(path, "/result/") && strings.HasSuffix(path, "/rate") && r.Method == http.MethodPost &&
		!s.ops.allow(limitKey(r, "rate"), 10, 10*time.Minute) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "操作过于频繁，请稍后再试"})
		return
	}

	switch {
	case path == "/info" && r.Method == http.MethodGet:
		s.handleInfo(w, r)
	case path == "/isp" && r.Method == http.MethodGet:
		s.handleISP(w, r)
	case path == "/servers" && r.Method == http.MethodGet:
		s.handleServers(w, r)
	// 首页「当前测速人数」：ping 是匿名高频心跳，live 是匿名只读快照，都要限流
	case path == "/live/ping" && r.Method == http.MethodPost:
		if !s.ops.allow(limitKey(r, "live"), 90, 5*time.Minute) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false})
			return
		}
		s.handleLivePing(w, r)
	case path == "/live" && r.Method == http.MethodGet:
		if !s.ops.allow(limitKey(r, "liveget"), 120, 5*time.Minute) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false})
			return
		}
		s.handleLive(w, r)
	// 注意：主控版不再自带测速节点，/ping、/download、/upload 不在这里注册。
	// 测速流量一律走节点版（APP_MODE=node），主控只负责页面、后台与节点管理。
	case path == "/report" && r.Method == http.MethodPost:
		s.handleReport(w, r)
	case strings.HasPrefix(path, "/result/") && strings.HasSuffix(path, "/rate") && r.Method == http.MethodPost:
		s.handleResultRate(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/result/"), "/rate"))
	case strings.HasPrefix(path, "/result/") && r.Method == http.MethodGet:
		s.handleResultGet(w, r, strings.TrimPrefix(path, "/result/"))

	case path == "/admin/login" && r.Method == http.MethodPost:
		s.handleLogin(w, r)
	case path == "/admin/logout" && r.Method == http.MethodPost:
		s.handleLogout(w, r)
	case path == "/admin/me" && r.Method == http.MethodGet:
		s.requireAuth(s.handleMe)(w, r)
	case path == "/admin/overview" && r.Method == http.MethodGet:
		s.requireAuth(s.handleOverview)(w, r)
	case path == "/admin/settings" && r.Method == http.MethodPut:
		s.requireAuth(s.handleSaveSettings)(w, r)
	case path == "/admin/icon" && r.Method == http.MethodPost:
		s.requireAuth(s.handleUploadIcon)(w, r)
	case path == "/admin/icon" && r.Method == http.MethodDelete:
		s.requireAuth(s.handleResetIcon)(w, r)
	case path == "/admin/password" && r.Method == http.MethodPost:
		s.requireAuth(s.handleChangePassword)(w, r)
	case path == "/admin/nodes/probe" && r.Method == http.MethodPost:
		s.requireAuth(s.handleProbeNode)(w, r)
	case path == "/admin/ipdebug" && r.Method == http.MethodGet:
		s.requireAuth(s.handleIPDebug)(w, r)
	case path == "/admin/nodes" && r.Method == http.MethodGet:
		s.requireAuth(s.handleListNode)(w, r)
	case path == "/admin/nodes" && r.Method == http.MethodPost:
		s.requireAuth(s.handleSaveNode)(w, r)
	case path == "/admin/results" && r.Method == http.MethodGet:
		s.requireAuth(s.handleResults)(w, r)
	case strings.HasPrefix(path, "/admin/nodes/"):
		rest := strings.TrimPrefix(path, "/admin/nodes/")
		// 手动重置某节点的本周期流量：POST /api/admin/nodes/{id}/traffic/reset
		if strings.HasSuffix(rest, "/traffic/reset") && r.Method == http.MethodPost {
			id := strings.TrimSuffix(rest, "/traffic/reset")
			s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
				s.handleResetNodeTraffic(w, r, id)
			})(w, r)
			return
		}
		if r.Method == http.MethodPut || r.Method == http.MethodDelete {
			s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
				s.handleNodeByID(w, r, rest)
			})(w, r)
			return
		}
		http.NotFound(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "接口不存在"})
	}
}

func publicSettings(st Settings) map[string]any {
	return map[string]any{
		"siteName":        st.SiteName,
		"tagline":         st.Tagline,
		"iconUrl":         st.IconURL,
		"hideLogoIcon":    st.HideLogoIcon,
		"titleSuffix":     st.TitleSuffix,
		"defaultNode":     st.DefaultNode,
		"defaultNode6":    st.DefaultNode6,
		"maxConnections":  st.MaxConnections,
		"downloadSeconds": st.DownloadSeconds,
		"uploadSeconds":   st.UploadSeconds,
		"gaugeMax":        st.GaugeMax,
		"allowNodeSelect": st.AllowNodeSelect,
		"autoSelectNode":  st.AutoSelectNode,
		"showClientIP":    st.ShowClientIP,
		"footerNote":      st.FooterNote,
		"probeV4":         st.ProbeV4,
		"probeV6":         st.ProbeV6,
		"ratingCfg":       st.Rating,
		"routes":          effectiveRoutesMap(st),
	}
}

// effectiveRoutesMap 供后台/接口读取当前生效的路径段（与 /routes.js、路由解析一致）。
func effectiveRoutesMap(st Settings) map[string]string {
	a, b, c, d, e := effectivePaths(st)
	return map[string]string{
		"langZh": a, "langEn": b, "v4": c, "v6": d, "result": e,
	}
}

func publicNodes(nodes []Node) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		out = append(out, map[string]any{
			"id":       n.ID,
			"name":     n.Name,
			"sponsor":  n.Sponsor,
			"location": n.Location,
			"url":      n.URL,
			"url6":     n.URL6,
			"local":    n.Local,
			"lat":      n.Lat,
			"lon":      n.Lon,
		})
	}
	return out
}

// logClientIP 在 LOG_CLIENT_IP=1 时打印访客地址解析过程，便于排查反向代理 / 转发链路。
func logClientIP(r *http.Request, resolved string) {
	if os.Getenv("LOG_CLIENT_IP") != "1" {
		return
	}
	log.Printf("[ip] path=%s resolved=%q remote=%q xff=%q real=%q cf=%q fwd=%q ua=%q",
		r.URL.Path, resolved, r.RemoteAddr,
		r.Header.Get("X-Forwarded-For"),
		r.Header.Get("X-Real-IP"),
		r.Header.Get("CF-Connecting-IP"),
		r.Header.Get("Forwarded"),
		r.Header.Get("User-Agent"),
	)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	noStore(w)
	cfg := s.store.Get()
	w.Header().Set("X-Speedtest-Node", "1")
	ip := clientIP(r)
	logClientIP(r, ip)
	meta := cachedMeta(ip)
	addrs4, addrs6 := hostAddresses()
	// 反代模式下服务器裸 IP 已不对客户端开放，前端跨协议探测要改用对外域名。
	// 默认从当前请求的 Host / X-Forwarded-* 自动识别，换反代域名无需改配置；
	// PUBLIC_BASE_URL 仅在反代把 Host 改写成回环地址时作为显式覆盖。
	if base := requestPublicBase(r); base != "" {
		addrs4 = []string{base}
		addrs6 = []string{base}
	}
	// 后台可配置的单栈补探域名优先：给 v4 只解析 A 的域名、给 v6 只解析 AAAA 的域名，
	// 浏览器请求它们时会被 DNS 强制走对应协议，服务端就能看到访客在该协议下的真实地址。
	if v := normalizeProbeURL(cfg.Settings.ProbeV4); v != "" {
		addrs4 = []string{v}
	}
	if v := normalizeProbeURL(cfg.Settings.ProbeV6); v != "" {
		addrs6 = []string{v}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"ip":        ip,
		"ipVersion": ipFamily(ip),
		"isp":       meta.ISP,
		"ispEn":     meta.ISPEn,
		"asn":       meta.ASN,
		"asName":    meta.ASName,
		"city":      meta.City,
		"lat":       meta.Lat,
		// 是否管理员登录：前台据此决定要不要展示节点地址等敏感信息
		"admin":     s.isAuthed(r),
		"lon":       meta.Lon,
		// 本机地址：前台切换 IPv4 / IPv6 时用它补探访客在另一协议下的地址
		"addrs":    map[string][]string{"v4": addrs4, "v6": addrs6},
		"settings": publicSettings(cfg.Settings),
		// servers 就是后台里配置的全部节点；主控自身不再是节点，没有 node 字段
		"servers": publicNodes(cfg.Nodes),
		"version": appVersion,
	})
}

func (s *Server) handleISP(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	noStore(w)
	ip := clientIP(r)
	logClientIP(r, ip)
	meta := lookupIPMeta(ip)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"ip":     ip,
		"isp":    meta.ISP,
		"ispEn":  meta.ISPEn,
		"asn":    meta.ASN,
		"asName": meta.ASName,
		"city":   meta.City,
		"region": meta.Region,
		"lat":    meta.Lat,
		"lon":    meta.Lon,
	})
}

// handleIPDebug 返回「源站实际看到的转发信息」，用来核对 CDN 回源配置。
// 换 CDN（Cloudflare / 阿里云 / 腾讯云 / EdgeOne）后打开它，就能看到
// 是哪个头带回了访客地址、解析结果对不对，不用再去翻容器日志。
func (s *Server) handleIPDebug(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	remote := normalizeIP(r.RemoteAddr)
	names := []string{
		"CF-Connecting-IP", "True-Client-IP", "Ali-Cdn-Real-Ip", "Ali-Real-Ip",
		"EO-Connecting-IP", "X-Real-IP", "X-Forwarded-For", "Forwarded",
		"X-Forwarded-Proto", "CDN-Src-Ip",
	}
	headers := map[string]string{}
	for _, h := range names {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			headers[h] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"resolved":       clientIP(r),
		"resolvedFamily": ipFamily(clientIP(r)),
		"remoteAddr":     r.RemoteAddr,
		"remoteIP":       remote,
		"remoteTrusted":  isTrustedProxy(remote),
		"remotePublic":   isPublicIP(remote),
		"trustedHeaders": trustedIPHeaders(),
		"clientHeaders":  headers,
	})
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	noStore(w)
	cfg := s.store.Get()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "servers": publicNodes(cfg.Nodes)})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	var payload struct {
		NodeID      string  `json:"nodeId"`
		NodeName    string  `json:"nodeName"`
		Ping        float64 `json:"ping"`
		Jitter      float64 `json:"jitter"`
		Download    float64 `json:"download"`
		Upload      float64 `json:"upload"`
		Loss        float64 `json:"loss"`
		// 带载延迟（下载 / 上传进行中测得的延迟）
		DownLat     float64 `json:"downLat"`
		UpLat       float64 `json:"upLat"`
		Connections int     `json:"connections"`
		ISP         string  `json:"isp"`
		ISPEn       string  `json:"ispEn"`
		IPVersion   string  `json:"ipVersion"`
		// 前端按所选协议探测到的访客地址（浏览器走 IPv6 打开页面时，
		// 服务端从连接上只能看到 IPv6 地址，IPv4 地址得靠前端另发探测拿到）
		ClientIP string `json:"clientIp"`
		// 前端统计的本次测速流量（字节）
		DownloadBytes int64 `json:"downloadBytes"`
		UploadBytes   int64 `json:"uploadBytes"`
		// 前端按秒聚合的测速曲线（延迟 → 下行 → 上行）
		Timeline []TimelinePoint `json:"timeline"`
	}
	if err := readJSON(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	// 字符串字段来自浏览器，限长避免一条报告塞进近 1MB 文本把内存/日志打满。
	clampText := func(v string, max int) string {
		r := []rune(v)
		if len(r) <= max {
			return v
		}
		return string(r[:max])
	}
	payload.NodeID = clampText(payload.NodeID, 64)
	payload.NodeName = clampText(payload.NodeName, 128)
	payload.ISP = clampText(payload.ISP, 128)
	payload.ISPEn = clampText(payload.ISPEn, 128)
	// 流量数字来自浏览器，做一次范围约束，避免脏数据写进报告
	const maxTraffic = int64(200) << 30
	if payload.DownloadBytes < 0 || payload.DownloadBytes > maxTraffic {
		payload.DownloadBytes = 0
	}
	if payload.UploadBytes < 0 || payload.UploadBytes > maxTraffic {
		payload.UploadBytes = 0
	}
	// 节点流量统计放在「是否收集报告」之前：关闭报告收集只是不留历史记录，
	// 流量限额仍然要算，否则一关收集限额就形同虚设。
	s.traffic.add(strings.TrimSpace(payload.NodeID), payload.DownloadBytes+payload.UploadBytes)
	if !cfg.Settings.CollectResults {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "skipped": true})
		return
	}
	// 曲线来自浏览器，做一次范围约束：最多 120 个点，越界与非数值一律按 0 / 丢点处理
	const maxTimeline = 120
	timeline := make([]TimelinePoint, 0, len(payload.Timeline))
	for _, p := range payload.Timeline {
		if len(timeline) >= maxTimeline {
			break
		}
		if p.T < 0 || p.T > 900 {
			continue
		}
		timeline = append(timeline, TimelinePoint{
			T: math.Round(p.T*10) / 10,
			D: clampNum(p.D, 1e5),
			U: clampNum(p.U, 1e5),
			P: clampNum(p.P, 60000),
		})
	}
	ipVersion := strings.ToLower(strings.TrimSpace(payload.IPVersion))
	if ipVersion != "v4" && ipVersion != "v6" {
		ipVersion = ""
	}
	// 决定报告里记哪个地址：
	// 1. 前端探到的地址与所选协议一致 → 用它（跨协议访问时这是唯一正确的选择）；
	// 2. 否则退回连接来源地址；
	// 3. 最终地址的协议与所选协议仍不一致时，以真实地址为准改正协议标记，
	//    避免报告里出现「IPv4 协议 + IPv6 地址」这种自相矛盾的组合。
	recordIP := clientIP(r)
	if probeIP := normalizeIP(payload.ClientIP); probeIP != "" && isPublicIP(probeIP) {
		if ipVersion == "" || ipFamily(probeIP) == ipVersion {
			recordIP = probeIP
			if ipVersion == "" {
				ipVersion = ipFamily(probeIP)
			}
		}
	}
	if recordIP != "" && ipVersion != "" && ipFamily(recordIP) != ipVersion {
		// 连接地址与所选协议不一致时，再翻一遍代理链，看有没有该协议的地址
		for _, cand := range clientIPChain(r) {
			if isPublicIP(cand) && ipFamily(cand) == ipVersion {
				recordIP = cand
				break
			}
		}
	}
	if recordIP != "" {
		if fam := ipFamily(recordIP); fam != "" {
			ipVersion = fam
		}
	}
	sponsor, location := "", ""
	for _, n := range cfg.Nodes {
		if n.ID == payload.NodeID {
			sponsor, location = n.Sponsor, n.Location
			if payload.NodeName == "" {
				payload.NodeName = n.Name
			}
			break
		}
	}
	saved := s.results.add(Result{
		Time:          time.Now(),
		IP:            recordIP,
		NodeID:        payload.NodeID,
		NodeName:      payload.NodeName,
		Sponsor:       sponsor,
		Location:      location,
		Ping:          payload.Ping,
		Jitter:        payload.Jitter,
		Download:      payload.Download,
		Upload:        payload.Upload,
		Loss:          payload.Loss,
		DownLat:       clampNum(payload.DownLat, 60000),
		UpLat:         clampNum(payload.UpLat, 60000),
		Connections:   payload.Connections,
		DownloadBytes: payload.DownloadBytes,
		UploadBytes:   payload.UploadBytes,
		ISP:           payload.ISP,
		ISPEn:         payload.ISPEn,
		IPVersion:     ipVersion,
		Timeline:      timeline,
		OwnerKey:      randomHex(16),
	})
	// owner 只回给测速的这门浏览器，报告页凭它显示测速者自己的 IP
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID, "owner": saved.OwnerKey})
}

/* ---------- 管理端 ---------- */

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	ip := clientIP(r)
	if wait := s.limiter.blocked(ip); wait > 0 {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok":    false,
			"error": "尝试次数过多，请 " + wait.Round(time.Second).String() + " 后重试",
		})
		return
	}
	cfg := s.store.Get()
	user := cfg.Admin
	got := hashPassword(body.Password, user.Salt, user.Iter)
	okUser := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Username)), []byte(user.Username)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(got), []byte(user.Hash)) == 1
	if !okUser || !okPass {
		s.limiter.fail(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "账号或密码不正确"})
		return
	}
	s.limiter.reset(ip)
	// 30 天有效期 + 滑动续期（见 http_util.go），避免第二天刷新就掉登录
	s.setSessionCookie(w, r, s.signSession(user.Username, sessionDuration))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "username": user.Username, "mustChange": user.MustChange,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   adminCookieDomain(),
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"username":   cfg.Admin.Username,
		"version":    appVersion,
		"mustChange": cfg.Admin.MustChange,
		"adminPath":  normalizeAdminPath(cfg.Settings.AdminPath),
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": cfg.Admin.Username,
		"settings": cfg.Settings,
		"nodes":    cfg.Nodes,
		"version":  appVersion,
		"localIP":  clientIP(r),
		"uptime":   int(time.Since(startedAt).Seconds()),
	})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var st Settings
	if err := readJSON(r, &st); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	if strings.TrimSpace(st.SiteName) == "" {
		st.SiteName = "SpeedTest"
	}
	// 标签页标题后缀：留空表示跟随界面语言用默认文案，限制长度避免异常标题。
	st.TitleSuffix = strings.TrimSpace(st.TitleSuffix)
	if r := []rune(st.TitleSuffix); len(r) > 40 {
		st.TitleSuffix = string(r[:40])
	}
	// 单栈补探域名：允许只填域名或完整 URL，统一规范成 scheme://host。
	// 留空表示不覆盖（沿用 PUBLIC_BASE_URL / 本机地址）。
	st.ProbeV4 = normalizeProbeURL(st.ProbeV4)
	st.ProbeV6 = normalizeProbeURL(st.ProbeV6)
	// 前台路由路径段：非法值回落到默认；各段撞车时由 effectivePaths 统一去重。
	st.PathLangZh = normalizePathSeg(st.PathLangZh, defPathLangZh)
	st.PathLangEn = normalizePathSeg(st.PathLangEn, defPathLangEn)
	st.PathV4 = normalizePathSeg(st.PathV4, defPathV4)
	st.PathV6 = normalizePathSeg(st.PathV6, defPathV6)
	st.PathResult = normalizePathSeg(st.PathResult, defPathResult)
	// 后台隐藏路径是敏感项：没提交（旧页面）就保留原值；提交了但非法则直接拒绝，
	// 不能让一次设置保存把入口清掉或改成 admin / api 之类的保留路径。
	current := s.store.Get().Settings.AdminPath
	if strings.TrimSpace(st.AdminPath) == "" {
		st.AdminPath = current
	} else if np := normalizeAdminPath(st.AdminPath); np != "" {
		st.AdminPath = np
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "后台路径只能用 3-40 位字母、数字、下划线或连字符，且不能与现有页面重名"})
		return
	}
	if normalizeAdminPath(st.AdminPath) == "" {
		st.AdminPath = defaultAdminPath()
	}
	if st.MaxConnections < 1 {
		st.MaxConnections = 1
	}
	if st.MaxConnections > 16 {
		st.MaxConnections = 16
	}
	if st.DownloadSeconds < 3 {
		st.DownloadSeconds = 3
	}
	if st.DownloadSeconds > 30 {
		st.DownloadSeconds = 30
	}
	if st.UploadSeconds < 3 {
		st.UploadSeconds = 3
	}
	if st.UploadSeconds > 30 {
		st.UploadSeconds = 30
	}
	if st.GaugeMax < 100 {
		st.GaugeMax = 100
	}
	if st.GaugeMax > 100000 {
		st.GaugeMax = 100000
	}
	// 评级门槛：缺字段/乱序/越界一律回落到默认值，避免前台按脏数据算档位。
	normalizeRating(&st.Rating)
	// 图标由专门的上传/重置接口管理，设置保存不动它（旧页面不会提交 iconUrl）。
	st.IconURL = s.store.Get().Settings.IconURL
	if err := s.store.Update(func(c *Config) error {
		c.Settings = st
		return nil
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "adminPath": st.AdminPath})
}

// handleUploadIcon 接收后台上传的站点图标，保存到 data 目录（覆盖式），
// 并把带版本号的访问路径写进配置，前端据此更新 favicon 与 logo。
func (s *Server) handleUploadIcon(w http.ResponseWriter, r *http.Request) {
	// 限制 5MB，防滥用
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "图片过大或格式错误（上限 5MB）"})
		return
	}
	file, header, err := r.FormFile("icon")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请选择图片文件"})
		return
	}
	defer file.Close()

	// 只按内容探测类型，不信任扩展名；要求确实是图片。
	buf := make([]byte, 512)
	n, _ := io.ReadFull(file, buf)
	if n == 0 || !strings.HasPrefix(http.DetectContentType(buf[:n]), "image/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "仅支持 PNG / JPG / GIF / WEBP / SVG 等图片格式"})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "读取图片失败"})
		return
	}
	// 校验文件后缀（仅用于告诉浏览器类型，真实类型上面已按内容确认）。
	ext := strings.ToLower(filepath.Ext(header.Filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico", ".avif":
	default:
		ext = ".png"
	}

	tmp := s.siteIconFile() + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "无法写入图标"})
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		os.Remove(tmp)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "写入图标失败"})
		return
	}
	out.Close()
	// 把扩展名也落盘，供 handleSiteIcon 之外的地方参考（当前按内容探测，非必需）。
	os.WriteFile(s.siteIconFile()+".ext", []byte(ext), 0o644)
	if err := os.Rename(tmp, s.siteIconFile()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存图标失败"})
		return
	}

	iconURL := "/site-icon?v=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := s.store.Update(func(c *Config) error {
		c.Settings.IconURL = iconURL
		return nil
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "iconUrl": iconURL, "ext": ext})
}

// handleResetIcon 删除自定义图标，恢复内置默认 favicon。
func (s *Server) handleResetIcon(w http.ResponseWriter, r *http.Request) {
	os.Remove(s.siteIconFile())
	os.Remove(s.siteIconFile() + ".ext")
	if err := s.store.Update(func(c *Config) error {
		c.Settings.IconURL = ""
		return nil
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "iconUrl": ""})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	if len(body.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "新密码至少 8 位"})
		return
	}
	if body.NewPassword == defaultAdminPass {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "不要继续使用安装默认密码"})
		return
	}
	cfg := s.store.Get()
	if hashPassword(body.OldPassword, cfg.Admin.Salt, cfg.Admin.Iter) != cfg.Admin.Hash {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "当前密码不正确"})
		return
	}
	user := strings.TrimSpace(body.Username)
	if user == "" {
		user = cfg.Admin.Username
	}
	if err := s.store.Update(func(c *Config) error {
		nextVersion := c.Admin.SessionVersion + 1
		c.Admin = newAdmin(user, body.NewPassword)
		c.Admin.SessionVersion = nextVersion
		return nil
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.setSessionCookie(w, r, s.signSession(user, sessionDuration))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListNode(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "nodes": cfg.Nodes})
}

type nodePayload struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sponsor  string  `json:"sponsor"`
	Location string  `json:"location"`
	URL      string  `json:"url"`
	URL6     string  `json:"url6"`
	Enabled  bool    `json:"enabled"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	// 流量限额：限额为 0 表示不限；重置日为 0 表示不自动重置
	TrafficLimitGB    float64 `json:"trafficLimitGB"`
	TrafficPeriodDay  int     `json:"trafficPeriodDay"`
	TrafficAutoResume bool    `json:"trafficAutoResume"`
}

func normalizeNodeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		switch {
		case strings.HasPrefix(raw, "["):
			// [2001:db8::1]:8081
			raw = "http://" + raw
		case looksIPv6(raw):
			// 2001:db8:4a2f:1100::1 —— 裸写 IPv6 时补上方括号
			raw = "http://[" + raw + "]"
		default:
			raw = "http://" + raw
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// requestPublicBase 推导本次请求对应的对外 origin（scheme://host）。
// 默认跟随反代传来的 Host / X-Forwarded-*，所以换任意域名都不需要改 .env；
// PUBLIC_BASE_URL 只作为显式覆盖，用于反代把 Host 改写成 127.0.0.1:3333 的场景。
func requestPublicBase(r *http.Request) string {
	if base := normalizeProbeURL(os.Getenv("PUBLIC_BASE_URL")); base != "" {
		return base
	}
	scheme := "http"
	if isSecureRequest(r) {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)
	// 反代把 Host 改写成回环/内网地址时，取可信代理传来的 X-Forwarded-Host。
	if isTrustedProxy(normalizeIP(r.RemoteAddr)) && isInternalHost(host) {
		if xfh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); xfh != "" && safeHostnamePort(xfh) {
			host = xfh
		}
	}
	if !safeHostnamePort(host) {
		return ""
	}
	return scheme + "://" + host
}

// safeHostnamePort 校验 Host 头只含合法字符，避免把伪造内容拼进 CSP / 下发地址。
func safeHostnamePort(v string) bool {
	if v == "" || len(v) > 255 {
		return false
	}
	if strings.HasPrefix(v, "[") {
		if _, _, err := net.SplitHostPort(v); err == nil {
			return true
		}
		return net.ParseIP(strings.Trim(v, "[]")) != nil
	}
	host := v
	if h, _, err := net.SplitHostPort(v); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return false
	}
	for _, c := range host {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// isInternalHost 判断 Host 是不是回环/内网地址（说明反代改写了 Host）。
func isInternalHost(v string) bool {
	host := v
	if h, _, err := net.SplitHostPort(v); err == nil {
		host = h
	}
	host = strings.Trim(strings.TrimSuffix(host, "."), "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return strings.EqualFold(host, "localhost")
}

// normalizeProbeURL 规范化后台填写的单栈补探域名。兼容只填域名/只填完整 URL 两种写法，
// 去掉路径、查询串与结尾斜杠，只保留 scheme://host，避免前端补探打到奇怪路径上。
func normalizeProbeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// looksIPv6 判断不带方括号的地址是否是 IPv6 字面量（至少两个冒号且没有路径）。
func looksIPv6(s string) bool {
	return strings.Count(s, ":") >= 2 && !strings.ContainsAny(s, "/")
}

// nodeFamily 推断节点地址属于哪个协议族；无法判断时返回空串（双栈域名）。
func nodeFamily(rawURL string) string {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return ""
	}
	if net.ParseIP(host) == nil {
		return ""
	}
	if ipFamily(host) == "v6" {
		return "v6"
	}
	return "v4"
}

func validateNode(p nodePayload) (Node, string) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return Node{}, "节点名称不能为空"
	}
	n := Node{
		ID:       strings.TrimSpace(p.ID),
		Name:     name,
		Sponsor:  strings.TrimSpace(p.Sponsor),
		Location: strings.TrimSpace(p.Location),
		URL:      normalizeNodeURL(p.URL),
		URL6:     normalizeNodeURL(p.URL6),
		Enabled:  p.Enabled,
		Lat:      p.Lat,
		Lon:      p.Lon,
		TrafficLimitGB:    math.Round(p.TrafficLimitGB*100) / 100,
		TrafficPeriodDay:  p.TrafficPeriodDay,
		TrafficAutoResume: p.TrafficAutoResume,
	}
	// 流量限额：负数按「不限」处理，上限 100 万 GB，防止误填天文数字把界面撑坏
	if n.TrafficLimitGB < 0 {
		n.TrafficLimitGB = 0
	}
	if n.TrafficLimitGB > 1e6 {
		n.TrafficLimitGB = 1e6
	}
	// 重置日只接受 1-28：0 表示不自动重置，超过 28 一律按 28（2 月没有 29-31 号）
	if n.TrafficPeriodDay < 0 {
		n.TrafficPeriodDay = 0
	}
	if n.TrafficPeriodDay > 28 {
		n.TrafficPeriodDay = 28
	}
	// 坐标只用于前台算距离，越界按「没有坐标」处理
	if n.Lat < -90 || n.Lat > 90 || n.Lon < -180 || n.Lon > 180 {
		n.Lat, n.Lon = 0, 0
	}
	// 把误填进 IPv4 栏的 IPv6 地址挪到 IPv6 栏，避免前台选错协议
	if n.URL != "" && n.URL6 == "" && nodeFamily(n.URL) == "v6" {
		n.URL, n.URL6 = "", n.URL
	}
	// 主控不提供测速带宽，节点必须自带地址（否则测速请求无处可去）
	if n.URL == "" && n.URL6 == "" {
		return Node{}, "节点地址不能为空：请填写节点版的访问地址，例如 https://node1.example.com:8443"
	}
	return n, ""
}

func (s *Server) handleSaveNode(w http.ResponseWriter, r *http.Request) {
	var p nodePayload
	if err := readJSON(r, &p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	n, verr := validateNode(p)
	if verr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": verr})
		return
	}
	if n.ID == "" {
		n.ID = "node-" + randomHex(4)
	}
	n.Local = false
	if err := s.store.Update(func(c *Config) error {
		for i := range c.Nodes {
			if c.Nodes[i].ID == n.ID {
				n.Local = c.Nodes[i].Local
				// 累计量由服务端维护，编辑表单不提交这些值
				n.TrafficUsed = c.Nodes[i].TrafficUsed
				n.TrafficResetAt = c.Nodes[i].TrafficResetAt
				n.TrafficBlocked = c.Nodes[i].TrafficBlocked
				// 调高限额或清空限额后，之前的超限停用自动解除，否则用户会以为改不动
				if n.TrafficBlocked && !n.trafficOver() {
					n.TrafficBlocked = false
				}
				c.Nodes[i] = n
				return nil
			}
		}
		c.Nodes = append(c.Nodes, n)
		return nil
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node": n})
}

// handleResetNodeTraffic 手动把某节点的本周期流量清零，可选择同时启用它。
func (s *Server) handleResetNodeTraffic(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Resume bool `json:"resume"`
	}
	// 允许空 body：不带参数就是只清零、不改启用状态
	_ = readJSON(r, &body)
	n, ok := s.resetNodeTraffic(strings.TrimSpace(id), body.Resume)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "节点不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node": n})
}

func (s *Server) handleNodeByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodDelete:
		cfg := s.store.Get()
		for _, n := range cfg.Nodes {
			if n.ID == id && n.Local {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "本机节点不可删除"})
				return
			}
		}
		if err := s.store.Update(func(c *Config) error {
			out := make([]Node, 0, len(c.Nodes))
			for _, n := range c.Nodes {
				if n.ID != id {
					out = append(out, n)
				}
			}
			c.Nodes = out
			if c.Settings.DefaultNode == id {
				c.Settings.DefaultNode = ""
			}
			if c.Settings.DefaultNode6 == id {
				c.Settings.DefaultNode6 = ""
			}
			return nil
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodPut:
		var p nodePayload
		if err := readJSON(r, &p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
			return
		}
		p.ID = id
		n, verr := validateNode(p)
		if verr != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": verr})
			return
		}
		found := false
		if err := s.store.Update(func(c *Config) error {
			for i := range c.Nodes {
				if c.Nodes[i].ID == id {
					n.Local = c.Nodes[i].Local
					c.Nodes[i] = n
					found = true
					return nil
				}
			}
			return errNodeNotFound
		}); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "节点不存在"})
			return
		}
		_ = found
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node": n})
	default:
		http.NotFound(w, r)
	}
}

type probeError string

func (e probeError) Error() string { return string(e) }

func (s *Server) handleProbeNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	target := normalizeNodeURL(body.URL)
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请填写节点地址"})
		return
	}
	// 后台探测是服务端发请求：默认禁止内网/回环地址并禁止跟随跳转，防止被当成 SSRF 跳板。
	if os.Getenv("ALLOW_PRIVATE_PROBE") != "1" {
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "节点地址无效"})
			return
		}
		host := u.Hostname()
		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			ips, _ = net.LookupIP(host)
		}
		if len(ips) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "节点地址无法解析"})
			return
		}
		for _, ip := range ips {
			if !isPublicIP(ip.String()) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "出于安全考虑，不能探测内网地址（可用 ALLOW_PRIVATE_PROBE=1 显式开启）"})
				return
			}
		}
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	best := time.Hour
	okCount := 0
	for i := 0; i < 3; i++ {
		start := time.Now()
		req, _ := http.NewRequest(http.MethodGet, target+"/api/ping?ts="+randomHex(4), nil)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.Header.Get("X-Speedtest-Node") != "1" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "该地址可以访问，但不是本测速服务节点",
			})
			return
		}
		okCount++
		if d := time.Since(start); d < best {
			best = d
		}
	}
	if okCount == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "节点无法连接，请检查地址与端口"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"ms":     float64(best.Microseconds()) / 1000.0,
		"url":    target,
		"family": nodeFamily(target),
	})
}

func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50, 1, 300)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": s.results.list(limit)})
}

/* ---------- 结果分享页接口 ---------- */

// clampNum 把浏览器上报的数值收进 [0, hi]：NaN / Inf / 负数都按 0 处理，最多保留两位小数。
func clampNum(v, hi float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	if v > hi {
		v = hi
	}
	return math.Round(v*100) / 100
}

func publicResult(r Result, cfg Config) map[string]any {
	ip := r.IP
	if !cfg.Settings.ShowClientIP {
		ip = ""
	}
	// 协议标记以实际记下的地址为准：老记录里出现过「IPv4 协议 + IPv6 地址」的矛盾组合
	ipVersion := r.IPVersion
	if fam := ipFamily(r.IP); fam != "" {
		ipVersion = fam
	}
	return map[string]any{
		"id":          r.ID,
		"time":        r.Time.Unix(),
		"ip":          ip,
		"ipVersion":   ipVersion,
		"isp":         r.ISP,
		"ispEn":       r.ISPEn,
		"ping":        r.Ping,
		"jitter":      r.Jitter,
		"download":    r.Download,
		"upload":      r.Upload,
		"loss":        r.Loss,
		"downLat":     r.DownLat,
		"upLat":       r.UpLat,
		// 体验评级门槛随报告下发，报告页据此算 5 档，和首页保持一致
		"ratingCfg":   cfg.Settings.Rating,
		"connections": r.Connections,
		"rating":      r.Rating,
		// 本次测速消耗的流量（字节）
		"downloadBytes": r.DownloadBytes,
		"uploadBytes":   r.UploadBytes,
		"bytes":         r.DownloadBytes + r.UploadBytes,
		// 逐秒曲线（老报告没有这个字段，报告页会自动隐藏图表面板）
		"timeline": r.Timeline,
		"node": map[string]any{
			"id":       r.NodeID,
			"name":     r.NodeName,
			"sponsor":  r.Sponsor,
			"location": r.Location,
		},
		"siteName": cfg.Settings.SiteName,
	}
}

func (s *Server) handleResultGet(w http.ResponseWriter, r *http.Request, id string) {
	noStore(w)
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 32 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "报告编号无效"})
		return
	}
	res, ok := s.results.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "报告不存在或已过期"})
		return
	}
	out := publicResult(res, s.store.Get())
	authed := s.isAuthed(r)
	// 节点内部标识只给后台登录状态
	if !authed {
		if node, ok := out["node"].(map[string]any); ok {
			delete(node, "id")
		}
	}
	// 报告链接可以随手分享：测速者本人（凭证匹配或同一来源 IP）与管理员能看到自己的 IP，
	// 其他人打开时不下发地址，只提示「仅测速者可见」。
	owner := authed || s.isResultOwner(r, res)
	if !owner {
		out["ip"] = ""
		out["restricted"] = true
	}
	out["owner"] = owner
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": out})
}

func (s *Server) handleResultRate(w http.ResponseWriter, r *http.Request, id string) {
	noStore(w)
	var payload struct {
		Score int `json:"score"`
	}
	if err := readJSON(r, &payload); err != nil || payload.Score < 0 || payload.Score > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "评分需在 0-10 之间"})
		return
	}
	if !s.results.rate(strings.TrimSpace(id), payload.Score) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "报告不存在或已过期"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
