package main

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"log"
	"math/big"
	mathrand "math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// garbage 是服务端预生成的内存数据块，下载测速时直接回写，不触碰磁盘。
var garbage []byte

func initGarbage() {
	garbage = make([]byte, 4<<20)
	rng := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	rng.Read(garbage)
}

func queryInt(r *http.Request, key string, def, min, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

func queryInt64(r *http.Request, key string, def int64) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// handleHealthz 是容器/反代探活端点，仅返回 pong，不依赖 store，主控与节点模式都可用。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, "pong")
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	setCORS(w)
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Speedtest-Node", "1")
	w.Header().Set("X-Server-Time", strconv.FormatInt(time.Now().UnixMilli(), 10))
	io.WriteString(w, "pong")
}

// handleDownload 以固定时长向客户端持续发送内存数据。
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	setCORS(w)
	noStore(w)
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Encoding", "identity")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Speedtest-Node", "1")

	seconds := queryInt(r, "seconds", 10, 1, 30)
	maxBytes := queryInt64(r, "bytes", 0)
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	buf := garbage
	var sent int64
	for {
		if maxBytes > 0 && sent >= maxBytes {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		if _, err := w.Write(buf); err != nil {
			return
		}
		sent += int64(len(buf))
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}
}

// handleUpload 接收并丢弃请求体，用于上行测速。
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	setCORS(w)
	noStore(w)
	w.Header().Set("X-Speedtest-Node", "1")

	start := time.Now()
	buf := make([]byte, 1<<20)
	n, err := io.CopyBuffer(io.Discard, io.LimitReader(r.Body, 8<<30), buf)
	if err != nil && n == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"bytes":   n,
		"seconds": time.Since(start).Seconds(),
	})
}

/* ---------- 测速记录 ---------- */

// TimelinePoint 是报告页曲线上的一个采样点：按秒聚合，秒内没有数据的项为 0。
type TimelinePoint struct {
	T float64 `json:"t"`           // 测速开始后的第几秒
	D float64 `json:"d,omitempty"` // 该秒的下载速率（Mbps）
	U float64 `json:"u,omitempty"` // 该秒的上传速率（Mbps）
	P float64 `json:"p,omitempty"` // 该秒的延迟（ms）
}

type Result struct {
	ID          string    `json:"id"`
	Time        time.Time `json:"time"`
	IP          string    `json:"ip"`
	NodeID      string    `json:"nodeId"`
	NodeName    string    `json:"nodeName"`
	Sponsor     string    `json:"sponsor"`
	Location    string    `json:"location"`
	Ping        float64   `json:"ping"`
	Jitter      float64   `json:"jitter"`
	Download    float64   `json:"download"`
	Upload      float64   `json:"upload"`
	Loss        float64   `json:"loss"`
	// 带载延迟：下载 / 上传进行中测得的延迟（老记录没有，报告页显示 --）
	DownLat float64 `json:"downLat,omitempty"`
	UpLat   float64 `json:"upLat,omitempty"`
	Connections int       `json:"connections"`
	// 本次测速实际消耗的流量（字节）：下载 + 上传，报告页直接展示
	DownloadBytes int64 `json:"downloadBytes,omitempty"`
	UploadBytes   int64 `json:"uploadBytes,omitempty"`
	ISP           string `json:"isp"`
	ISPEn         string `json:"ispEn,omitempty"`
	IPVersion     string `json:"ipVersion,omitempty"`
	Rating        int    `json:"rating"`
	// 逐秒曲线：报告页拖动能看到每一秒的上下行速率与延迟
	Timeline []TimelinePoint `json:"timeline,omitempty"`
	// OwnerKey 是这份报告的「测速者凭证」，只在生成时返回给测速的浏览器，
	// 别人拿到报告链接也看不到测速者的 IP。
	OwnerKey string `json:"ownerKey,omitempty"`
}

// resultLog 把测速结果同时保存在内存与 data/results.jsonl 中，
// 这样分享出去的 /zh-Hans/result/<id> 在服务重启后依然可以打开。
type resultLog struct {
	mu    sync.Mutex
	items []Result
	byID  map[string]int
	file  *os.File
}

const resultKeep = 500

func newResultLog(path string) *resultLog {
	l := &resultLog{
		items: make([]Result, 0, resultKeep),
		byID:  map[string]int{},
	}
	l.load(path)
	if path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			l.file = f
		} else {
			// 打不开就别悄悄只写内存：容器重建后这些报告会全部丢失
			log.Printf("警告：结果文件 %s 不可写（%v），本次运行只保存在内存里", path, err)
		}
	}
	return l
}

func (l *resultLog) load(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	all := make([]Result, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.ID == "" {
			continue
		}
		all = append(all, r)
	}
	// 同一编号可能因为评分被追加多行，后写的覆盖先写的
	seen := make(map[string]int, len(all))
	dedup := make([]Result, 0, len(all))
	for _, r := range all {
		if i, ok := seen[r.ID]; ok {
			dedup[i] = r
			continue
		}
		seen[r.ID] = len(dedup)
		dedup = append(dedup, r)
	}
	for _, r := range dedup {
		l.push(r)
	}
	// 文件过大时压缩重写一次，避免无限增长
	if len(lines) > resultKeep*4 {
		l.compact(path)
	}
}

func (l *resultLog) push(r Result) {
	l.items = append(l.items, r)
	if len(l.items) > resultKeep {
		drop := len(l.items) - resultKeep
		l.items = l.items[drop:]
		l.byID = make(map[string]int, len(l.items))
		for i, it := range l.items {
			l.byID[it.ID] = i
		}
		return
	}
	l.byID[r.ID] = len(l.items) - 1
}

func (l *resultLog) compact(path string) {
	var b strings.Builder
	for _, it := range l.items {
		raw, err := json.Marshal(it)
		if err != nil {
			continue
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		l.file = f
	}
}

// newResultID 生成 11 位纯数字报告编号（首位非 0），并确保当前记录里不重复。
func (l *resultLog) newResultID() string {
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(90000000000))
		if err != nil {
			return strconv.FormatInt(time.Now().UnixNano()%1e11, 10)
		}
		id := strconv.FormatInt(n.Int64()+10000000000, 10)
		if _, dup := l.byID[id]; !dup {
			return id
		}
	}
}

func (l *resultLog) add(r Result) Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.ID == "" {
		r.ID = l.newResultID()
	}
	l.push(r)
	if l.file != nil {
		if raw, err := json.Marshal(r); err == nil {
			l.file.Write(append(raw, '\n'))
		}
	}
	return r
}

func (l *resultLog) get(id string) (Result, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i, ok := l.byID[id]; ok && i < len(l.items) {
		return l.items[i], true
	}
	return Result{}, false
}

func (l *resultLog) rate(id string, score int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	i, ok := l.byID[id]
	if !ok || i >= len(l.items) {
		return false
	}
	l.items[i].Rating = score
	if l.file != nil {
		if raw, err := json.Marshal(l.items[i]); err == nil {
			l.file.Write(append(raw, '\n'))
		}
	}
	return true
}

func (l *resultLog) list(limit int) []Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.items) {
		limit = len(l.items)
	}
	out := make([]Result, 0, limit)
	for i := len(l.items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.items[i])
	}
	return out
}
