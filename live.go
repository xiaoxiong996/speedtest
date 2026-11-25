package main

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// liveTTL 是在线判定窗口：超过这个时间没收到心跳就认为这次测速已结束。
// 前端每 5 秒心跳一次，留 4 个周期的余量，避免偶发丢包把人数算漏。
const liveTTL = 20 * time.Second

type livePeer struct {
	last time.Time
	node string
}

// liveTracker 只在内存里记活跃测速会话，不落盘、不含访客 IP。
// 进程重启后归零，这是刻意的：它反映的是「此刻」的人数，没有历史价值。
type liveTracker struct {
	mu    sync.Mutex
	peers map[string]livePeer
}

func newLiveTracker() *liveTracker {
	return &liveTracker{peers: make(map[string]livePeer)}
}

func (l *liveTracker) ping(sid, node string) {
	l.mu.Lock()
	l.peers[sid] = livePeer{last: time.Now(), node: node}
	l.mu.Unlock()
}

// snapshot 返回在线人数与按节点的人数分布，同时清理过期会话。
func (l *liveTracker) snapshot() (int, map[string]int) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	byNode := make(map[string]int)
	online := 0
	for sid, p := range l.peers {
		if now.Sub(p.last) > liveTTL {
			delete(l.peers, sid)
			continue
		}
		online++
		if p.node != "" {
			byNode[p.node]++
		}
	}
	return online, byNode
}

// handleLive 返回当前正在测速的人数与所用节点。公开只读，不含任何访客信息。
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	noStore(w)
	online, byNode := s.live.snapshot()
	cfg := s.store.Get()
	nameOf := make(map[string]string, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		nameOf[n.ID] = n.Name
	}
	list := make([]map[string]any, 0, len(byNode))
	for id, c := range byNode {
		name := nameOf[id]
		if name == "" {
			// 节点已被删除，计入总人数但不在节点分布里出现
			continue
		}
		list = append(list, map[string]any{"id": id, "name": name, "count": c})
	}
	sort.Slice(list, func(i, j int) bool {
		ci, cj := list[i]["count"].(int), list[j]["count"].(int)
		if ci != cj {
			return ci > cj
		}
		return list[i]["name"].(string) < list[j]["name"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "online": online, "nodes": list})
}

// handleLivePing 接收前端心跳：有人开始测速后每 5 秒打一次，用它统计在线人数。
func (s *Server) handleLivePing(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	noStore(w)
	var body struct {
		SID  string `json:"sid"`
		Node string `json:"nodeId"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	sid := strings.TrimSpace(body.SID)
	if sid == "" || len(sid) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	s.live.ping(sid, strings.TrimSpace(body.Node))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
