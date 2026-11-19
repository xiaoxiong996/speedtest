package main

// 证书存储：支持多张证书并存，按 SNI 自动选择
//
// 目录结构（NODE_CERT_DIR，默认 /certs）：
//
//	certs/
//	  ├─ node-a.example.com/
//	  │    ├─ cert.pem
//	  │    └─ key.pem
//	  ├─ node-b.example.com/
//	  │    ├─ cert.pem
//	  │    └─ key.pem
//	  └─ cert.pem / key.pem      ← 旧版单证书结构，仍兼容（当作一张）
//
// 为什么要多张：可以给不同域名分别签、分别续、分别删，
// 互不影响。一张证书里也可以有多个 SAN（一次签多个域名），两种都支持。
//
// 热加载：每 20 秒扫一次目录，有新增/删除/修改就重建证书集合，全程不断连。

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// certEntry 一张证书的运行期信息。
type certEntry struct {
	Dir      string   `json:"dir"`      // 子目录名（删除时用）
	Domain   string   `json:"domain"`   // CN
	SANs     []string `json:"sans"`     //
	Issuer   string   `json:"issuer"`   //
	NotAfter string   `json:"notAfter"` //
	DaysLeft int      `json:"daysLeft"` //
	Auto     bool     `json:"auto"`     // true=自动续期会管它
}

// certStore 管理 certDir 下的全部证书。
type certStore struct {
	dir string

	mu       sync.RWMutex
	pairs    []tls.Certificate // 当前可用的全部证书
	byName   map[string]int    // 域名(SAN) -> pairs 下标，SNI 选证书用
	entries  []certEntry       // 面板展示
	mtimeSum int64             // 目录指纹，用来跳过无变化的重扫

	fp atomic.Int64 // 目录内容指纹（供外部判断是否需要重启）
}

// activeCertStore 全局持有，供面板签发/删除后立即重载。
var activeCertStore atomic.Value // *certStore

// newCertStore 建一个证书存储。目录不存在会自动创建（只读根目录时请挂好卷）。
func newCertStore(dir string) (*certStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("无法创建证书目录 %s: %w", dir, err)
	}
	cs := &certStore{dir: dir, byName: map[string]int{}}
	if err := cs.Reload(); err != nil {
		return nil, err
	}
	activeCertStore.Store(cs)
	go cs.watch()
	return cs, nil
}

// Reload 重扫目录并重建证书集合。没有变化时几乎无开销。
func (cs *certStore) Reload() error {
	sum := dirFingerprint(cs.dir)
	cs.mu.RLock()
	unchanged := sum != 0 && sum == cs.mtimeSum
	cs.mu.RUnlock()
	if unchanged {
		return nil
	}

	var pairs []tls.Certificate
	var entries []certEntry
	byName := map[string]int{}

	// 1) 旧版单证书（根目录 cert.pem）
	if p, e, ok := loadPairFrom(filepath.Join(cs.dir, "cert.pem"), filepath.Join(cs.dir, "key.pem"), ""); ok {
		byName = indexSeg(p, e, &pairs, byName, e.Domain)
		pairs = append(pairs, p)
		entries = append(entries, e)
	}

	// 2) 子目录里的多证书
	items, _ := os.ReadDir(cs.dir)
	var names []string
	for _, it := range items {
		if it.IsDir() {
			names = append(names, it.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		d := filepath.Join(cs.dir, name)
		p, e, ok := loadPairFrom(filepath.Join(d, "cert.pem"), filepath.Join(d, "key.pem"), name)
		if !ok {
			continue
		}
		for _, san := range e.SANs {
			if _, exists := byName[strings.ToLower(san)]; !exists {
				byName[strings.ToLower(san)] = len(pairs)
			}
		}
		if e.Domain != "" {
			if _, exists := byName[strings.ToLower(e.Domain)]; !exists {
				byName[strings.ToLower(e.Domain)] = len(pairs)
			}
		}
		pairs = append(pairs, p)
		entries = append(entries, e)
	}

	cs.mu.Lock()
	cs.pairs = pairs
	cs.byName = byName
	cs.entries = entries
	cs.mtimeSum = sum
	cs.mu.Unlock()

	cs.fp.Store(time.Now().UnixNano())
	return nil
}

// indexSeg 把旧版单证书索引进 byName。
func indexSeg(p tls.Certificate, e certEntry, pairs *[]tls.Certificate, byName map[string]int, domain string) map[string]int {
	idx := len(*pairs)
	for _, san := range e.SANs {
		if _, ok := byName[strings.ToLower(san)]; !ok {
			byName[strings.ToLower(san)] = idx
		}
	}
	if domain != "" {
		if _, ok := byName[strings.ToLower(domain)]; !ok {
			byName[strings.ToLower(domain)] = idx
		}
	}
	return byName
}

func loadPairFrom(certPath, keyPath, dirName string) (tls.Certificate, certEntry, bool) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, certEntry{}, false
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return tls.Certificate{}, certEntry{}, false
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return tls.Certificate{}, certEntry{}, false
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return tls.Certificate{}, certEntry{}, false
	}
	pair.Leaf = leaf

	dir := dirName
	if dir == "" {
		dir = "."
	}
	return pair, certEntry{
		Dir:      dir,
		Domain:   leaf.Subject.CommonName,
		SANs:     leaf.DNSNames,
		Issuer:   leaf.Issuer.CommonName,
		NotAfter: leaf.NotAfter.Format("2006-01-02"),
		DaysLeft: int(time.Until(leaf.NotAfter).Hours() / 24),
		Auto:     true,
	}, true
}

// dirFingerprint 目录指纹：所有 cert.pem / key.pem 的 mtime 之和。
func dirFingerprint(dir string) int64 {
	var sum int64
	items, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, it := range items {
		p := filepath.Join(dir, it.Name())
		if it.IsDir() {
			for _, f := range []string{"cert.pem", "key.pem"} {
				if st, err := os.Stat(filepath.Join(p, f)); err == nil {
					sum += st.ModTime().UnixNano()
				}
			}
			continue
		}
		if it.Name() == "cert.pem" || it.Name() == "key.pem" {
			if st, err := it.Info(); err == nil {
				sum += st.ModTime().UnixNano()
			}
		}
	}
	return sum
}

func (cs *certStore) watch() {
	tk := time.NewTicker(20 * time.Second)
	defer tk.Stop()
	for range tk.C {
		if err := cs.Reload(); err != nil {
			log.Printf("节点证书：重新扫描失败（继续用旧证书）: %v", err)
		}
	}
}

// TLSConfig 返回带 SNI 选择的 TLS 配置。
func (cs *certStore) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// 只开 http/1.1：h2 会把多线并发复用成一条 TCP，多线测速上不去
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			cs.mu.RLock()
			defer cs.mu.RUnlock()
			if len(cs.pairs) == 0 {
				return nil, fmt.Errorf("节点还没有可用证书")
			}
			name := strings.ToLower(strings.TrimSuffix(hi.ServerName, "."))
			// 先精确匹配
			if i, ok := cs.byName[name]; ok && i < len(cs.pairs) {
				return &cs.pairs[i], nil
			}
			// 再按通配符 / 父域后缀匹配
			for host, i := range cs.byName {
				if strings.HasPrefix(host, "*.") && strings.HasSuffix(name, host[1:]) && i < len(cs.pairs) {
					return &cs.pairs[i], nil
				}
			}
			// 用 IP 直连等没有 SNI 的情况：给第一张，至少能握上手
			return &cs.pairs[0], nil
		},
	}
}

// HasCert 是否有任何可用证书。
func (cs *certStore) HasCert() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.pairs) > 0
}

// Entries 返回面板展示用的列表。
func (cs *certStore) Entries() []certEntry {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]certEntry, len(cs.entries))
	copy(out, cs.entries)
	return out
}

// CertDirOf 返回某张证书所在目录（删除/续期用）。
func (cs *certStore) CertDirOf(dirName string) string {
	if dirName == "." || dirName == "" {
		return cs.dir
	}
	return filepath.Join(cs.dir, dirName)
}

// saveTo 把证书写进 <dir>/<sub>/，并立即重载。
func (cs *certStore) saveTo(sub string, certPEM, keyPEM []byte) error {
	sub = strings.ToLower(strings.TrimSpace(sub))
	sub = strings.NewReplacer("/", "_", "\\", "_", "..", "_", "*", "_").Replace(sub)
	if sub == "" {
		return fmt.Errorf("证书目录名不能为空")
	}
	d := filepath.Join(cs.dir, sub)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(d, "cert.pem"), certPEM, 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(d, "key.pem"), keyPEM, 0o600); err != nil {
		return err
	}
	return cs.Reload()
}

// Remove 删除某张证书（整目录），并立即重载。
func (cs *certStore) Remove(dirName string) error {
	d := cs.CertDirOf(dirName)
	if dirName == "." || dirName == "" {
		// 旧版单证书：只删两个文件，别把整个 certs 目录端了
		for _, f := range []string{"cert.pem", "key.pem"} {
			if err := os.Remove(filepath.Join(d, f)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	} else {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return cs.Reload()
}

// 小工具 ------------------------------------------------------------

// writeFileAtomic 先写临时文件再 rename，避免读到写了一半的证书。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// envBool 读取布尔型环境变量（1/true/yes/on 为真）。
func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// certDaysLeft 全目录里最久的那张证书的剩余天数；一张都没有返回 -1。
func certDaysLeft(dir string) int {
	best := -1
	items, err := os.ReadDir(dir)
	if err != nil {
		return -1
	}
	probe := func(certPath string) {
		raw, err := os.ReadFile(certPath)
		if err != nil {
			return
		}
		blk, _ := pem.Decode(raw)
		if blk == nil {
			return
		}
		leaf, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return
		}
		d := int(time.Until(leaf.NotAfter).Hours() / 24)
		if d > best {
			best = d
		}
	}
	for _, it := range items {
		if it.IsDir() {
			probe(filepath.Join(dir, it.Name(), "cert.pem"))
		} else if it.Name() == "cert.pem" {
			probe(filepath.Join(dir, it.Name()))
		}
	}
	return best
}

// reloadActiveCerts 立刻重扫（面板签发/删除后调用）。
func reloadActiveCerts() {
	if v, ok := activeCertStore.Load().(*certStore); ok && v != nil {
		if err := v.Reload(); err != nil {
			log.Printf("节点证书：立即重载失败（等下一轮自动重试）: %v", err)
		}
	}
}
