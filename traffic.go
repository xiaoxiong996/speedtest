package main

import (
	"errors"
	"log"
	"sync"
	"time"
)

// trafficFlushInterval 是流量缓冲落盘间隔。
// 每条测速报告都直接写 config.json 会造成大量小文件写入，这里先在内存累加，
// 定期一次性落盘；容器异常退出最多丢这个间隔内的计数。
const trafficFlushInterval = 15 * time.Second

// errNoChange 用于让 Store.Update 在没有任何改动时跳过写盘（避免每秒刷盘）。
var errNoChange = errors.New("无需保存")

type trafficMeter struct {
	mu      sync.Mutex
	pending map[string]int64
}

func newTrafficMeter() *trafficMeter {
	return &trafficMeter{pending: make(map[string]int64)}
}

// add 把一次测速的流量记到节点名下（字节）。
func (m *trafficMeter) add(nodeID string, n int64) {
	if nodeID == "" || n <= 0 {
		return
	}
	m.mu.Lock()
	m.pending[nodeID] += n
	m.mu.Unlock()
}

// take 取出并清空待落盘计数。
func (m *trafficMeter) take() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	out := m.pending
	m.pending = make(map[string]int64)
	return out
}

// discard 丢弃某节点尚未落盘的增量。手动重置流量时必须调用，
// 否则下一次 flush 会把重置前那十几秒的流量又加回去。
func (m *trafficMeter) discard(nodeID string) {
	m.mu.Lock()
	delete(m.pending, nodeID)
	m.mu.Unlock()
}

// flushTraffic 把内存里的流量增量写进配置，并处理超额停用。
func (s *Server) flushTraffic() {
	deltas := s.traffic.take()
	if len(deltas) == 0 {
		return
	}
	var blocked []string
	err := s.store.Update(func(c *Config) error {
		hit := false
		for i := range c.Nodes {
			n := &c.Nodes[i]
			d, ok := deltas[n.ID]
			if !ok {
				continue
			}
			n.TrafficUsed += d
			hit = true
			limit := n.trafficLimitBytes()
			if limit > 0 && !n.TrafficBlocked && n.TrafficUsed >= limit {
				n.TrafficBlocked = true
				if n.Enabled {
					n.Enabled = false
				}
				blocked = append(blocked, n.Name)
			}
		}
		if !hit {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		log.Printf("流量统计落盘失败: %v", err)
	}
	for _, name := range blocked {
		log.Printf("节点 %s 已达流量限额，已自动停用（到重置日或手动重置后恢复）", name)
	}
}

// maybeResetTraffic 检查每个节点是否到了每月重置日：
// 已到点且本月还没重置过 → 清零已用流量、解除超限停用，按配置决定是否自动启用。
func (s *Server) maybeResetTraffic() {
	now := time.Now()
	var resumed []string
	err := s.store.Update(func(c *Config) error {
		changed := false
		for i := range c.Nodes {
			n := &c.Nodes[i]
			day := n.TrafficPeriodDay
			if day <= 0 {
				continue
			}
			// 只允许到 28，避免 2 月缺少 29-31 号导致整月漏重置
			if day > 28 {
				day = 28
			}
			// 本月重置点（当天零点）。还没到，或者本月已经重置过，都跳过。
			point := time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, now.Location())
			if now.Before(point) {
				continue
			}
			if n.TrafficResetAt > 0 && !time.Unix(n.TrafficResetAt, 0).Before(point) {
				continue
			}
			n.TrafficUsed = 0
			n.TrafficResetAt = point.Unix()
			n.TrafficBlocked = false
			changed = true
			if n.TrafficAutoResume && !n.Enabled {
				n.Enabled = true
				resumed = append(resumed, n.Name)
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		log.Printf("流量按月重置失败: %v", err)
	}
	for _, name := range resumed {
		log.Printf("节点 %s 已到每月流量重置日，流量清零并自动恢复启用", name)
	}
}

// resetNodeTraffic 手动把某节点本周期流量清零；resume 为 true 时同时启用。
// 返回重置后的节点与是否找到。
func (s *Server) resetNodeTraffic(id string, resume bool) (Node, bool) {
	var out Node
	found := false
	err := s.store.Update(func(c *Config) error {
		for i := range c.Nodes {
			if c.Nodes[i].ID != id {
				continue
			}
			found = true
			c.Nodes[i].TrafficUsed = 0
			c.Nodes[i].TrafficBlocked = false
			c.Nodes[i].TrafficResetAt = time.Now().Unix()
			if resume {
				c.Nodes[i].Enabled = true
			}
			out = c.Nodes[i]
			break
		}
		if !found {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		log.Printf("手动重置节点流量失败: %v", err)
	}
	if found {
		s.traffic.discard(id)
	}
	return out, found
}
