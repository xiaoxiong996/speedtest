package main

// 节点版证书签发：Go 原生实现 ACME DNS-01（不依赖 acme.sh / lego）
//
// 为什么用 DNS-01：NAT 机器只有高端口被映射，80/443 都到不了本机，
// TLS-ALPN-01 与 HTTP-01 都通不过。DNS-01 只要求在 DNS 里加一条 TXT 记录，
// 全程不需要任何入站端口。
//
// 提供两种「怎么加这条 TXT」：
//   1. Cloudflare API Token —— 全自动：自动加记录、自动等待生效、自动删记录、自动续期
//   2. 手动              —— 面板把 TXT 显示出来，你去 DNS 后台添加，点一下继续
//
// 两种都用同一套 Go ACME 客户端（golang.org/x/crypto/acme），不再 shell 出去调 acme.sh。

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

/* ===================== DNS 服务商抽象 ===================== */

type dnsProvider interface {
	// Present 添加/更新 TXT 记录：fqdn=_acme-challenge.域名
	Present(fqdn, value string) error
	// CleanUp 删除记录（失败不算致命）
	CleanUp(fqdn, value string) error
	// Kind 用于日志与面板展示
	Kind() string
}

/* ---------- Cloudflare ---------- */

type cfProvider struct {
	token string
	zone  string // 缓存 zone id
	mu    sync.Mutex
	ids   map[string]string // fqdn -> record id
}

func newCFProvider(token string) *cfProvider {
	return &cfProvider{token: token, ids: map[string]string{}}
}

func (c *cfProvider) Kind() string { return "cloudflare" }

func (c *cfProvider) do(method, path string, body any) (map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "https://api.cloudflare.com/client/v4"+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Cloudflare 返回无法解析（%d）: %s", res.StatusCode, truncate(string(raw), 160))
	}
	if ok, _ := out["success"].(bool); !ok {
		return nil, fmt.Errorf("Cloudflare API 失败: %s", cfErrors(out))
	}
	return out, nil
}

func cfErrors(out map[string]any) string {
	var msgs []string
	if errs, ok := out["errors"].([]any); ok {
		for _, e := range errs {
			if m, ok := e.(map[string]any); ok {
				msgs = append(msgs, fmt.Sprintf("%v(代码 %v)", m["message"], m["code"]))
			}
		}
	}
	if len(msgs) == 0 {
		return "未知错误"
	}
	return strings.Join(msgs, "; ")
}

// zoneOf 从 _acme-challenge.node1.example.com 推出 example.com（逐级尝试）。
func (c *cfProvider) zoneOf(fqdn string) (string, error) {
	if c.zone != "" {
		return c.zone, nil
	}
	labels := strings.Split(strings.TrimSuffix(fqdn, "."), ".")
	for i := 1; i < len(labels)-1; i++ {
		guess := strings.Join(labels[i:], ".")
		out, err := c.do("GET", "/zones?name="+guess+"&per_page=1", nil)
		if err != nil {
			continue
		}
		if res, ok := out["result"].([]any); ok && len(res) > 0 {
			if z, ok := res[0].(map[string]any); ok {
				if id, ok := z["id"].(string); ok {
					c.zone = id
					return id, nil
				}
			}
		}
	}
	return "", fmt.Errorf("找不到 %s 所在的 Cloudflare 区域（检查 API Token 权限）", fqdn)
}

func (c *cfProvider) Present(fqdn, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	zone, err := c.zoneOf(fqdn)
	if err != nil {
		return err
	}

	// 先删同名旧记录，避免多条 TXT 导致校验取错值
	if id := c.ids[fqdn]; id != "" {
		_, _ = c.do("DELETE", "/zones/"+zone+"/dns_records/"+id, nil)
		delete(c.ids, fqdn)
	}
	if out, err := c.do("GET", "/zones/"+zone+"/dns_records?type=TXT&name="+fqdn+"&per_page=100", nil); err == nil {
		if res, ok := out["result"].([]any); ok {
			for _, it := range res {
				if m, ok := it.(map[string]any); ok {
					if id, ok := m["id"].(string); ok {
						_, _ = c.do("DELETE", "/zones/"+zone+"/dns_records/"+id, nil)
					}
				}
			}
		}
	}

	out, err := c.do("POST", "/zones/"+zone+"/dns_records", map[string]any{
		"type":    "TXT",
		"name":    fqdn,
		"content": value,
		"ttl":     120,
		"comment": "speedtest-node acme",
	})
	if err != nil {
		return err
	}
	if res, ok := out["result"].(map[string]any); ok {
		if id, ok := res["id"].(string); ok {
			c.ids[fqdn] = id
		}
	}
	log.Printf("节点证书：已在 Cloudflare 添加 TXT %s", fqdn)
	return nil
}

func (c *cfProvider) CleanUp(fqdn, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.ids[fqdn]
	if id == "" {
		return nil
	}
	zone, err := c.zoneOf(fqdn)
	if err != nil {
		return err
	}
	_, err = c.do("DELETE", "/zones/"+zone+"/dns_records/"+id, nil)
	delete(c.ids, fqdn)
	return err
}

/* ---------- 手动 ---------- */

// manualProvider 把待添加的记录暴露出去，等用户在面板点「我已添加」。
type manualProvider struct {
	mu    sync.Mutex
	fqdn  string
	value string
}

func (m *manualProvider) Kind() string { return "manual" }

func (m *manualProvider) Present(fqdn, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fqdn, m.value = fqdn, value
	return nil
}

func (m *manualProvider) CleanUp(string, string) error { return nil }

func (m *manualProvider) pending() (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fqdn, m.value
}

/* ===================== ACME 客户端 ===================== */

// acmeIssuer 负责签发与续期；证书写入 <dir>/cert.pem 与 key.pem。
type acmeIssuer struct {
	dir     string
	staging bool
	email   string

	mu   sync.Mutex
	key  crypto.Signer
	acme *acme.Client
}

func newACMEIssuer(dir string, staging bool, email string) *acmeIssuer {
	return &acmeIssuer{dir: dir, staging: staging, email: email}
}

// client 惰性建立：账户私钥持久化，重复注册不会撞限额。
func (a *acmeIssuer) client() (*acme.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.acme != nil {
		return a.acme, nil
	}
	keyPath := filepath.Join(a.dir, "account.key")
	var signer crypto.Signer
	if raw, err := os.ReadFile(keyPath); err == nil {
		if blk, _ := pem.Decode(raw); blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				signer = k
			}
		}
	}
	if signer == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomic(keyPath,
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
		signer = k
	}

	c := &acme.Client{Key: signer}
	if a.staging {
		c.DirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := c.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil {
		// 已注册过的账户会返回错误，忽略即可
		if !strings.Contains(err.Error(), "already") {
			log.Printf("节点证书：账户注册提示（可忽略）: %v", err)
		}
	}
	a.key, a.acme = signer, c
	return c, nil
}

// Issue 走 DNS-01 签发证书，返回 cert/key 的 PEM。
func (a *acmeIssuer) Issue(domains []string, prov dnsProvider, progress func(string)) ([]byte, []byte, error) {
	c, err := a.client()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	ids := make([]acme.AuthzID, 0, len(domains))
	for _, d := range domains {
		ids = append(ids, acme.AuthzID{Type: "dns", Value: d})
	}
	order, err := c.AuthorizeOrder(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("创建订单失败: %w", err)
	}

	for _, authURL := range order.AuthzURLs {
		authz, err := c.GetAuthorization(ctx, authURL)
		if err != nil {
			return nil, nil, fmt.Errorf("获取授权失败: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}

		var chal *acme.Challenge
		for _, ch := range authz.Challenges {
			if ch.Type == "dns-01" {
				chal = ch
				break
			}
		}
		if chal == nil {
			return nil, nil, fmt.Errorf("域名 %s 没有提供 DNS-01 挑战", authz.Identifier.Value)
		}

		// TXT 值 = base64url(sha256(keyAuth))
		rec, err := c.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return nil, nil, fmt.Errorf("计算 TXT 值失败: %w", err)
		}
		fqdn := "_acme-challenge." + authz.Identifier.Value
		if err := prov.Present(fqdn, rec); err != nil {
			return nil, nil, fmt.Errorf("添加 TXT 记录失败: %w", err)
		}
		defer prov.CleanUp(fqdn, rec)

		if progress != nil {
			progress("正在等待 DNS 记录生效…")
		}
		// 给 DNS 一点传播时间（Cloudflare 通常几十秒内生效）
		if err := waitForTXT(ctx, fqdn, rec); err != nil {
			log.Printf("节点证书：等待 TXT 生效超时（继续尝试验证）: %v", err)
		}

		if _, err := c.Accept(ctx, chal); err != nil {
			return nil, nil, fmt.Errorf("确认挑战失败: %w", err)
		}
		if progress != nil {
			progress("Let's Encrypt 正在校验…")
		}
		if _, err := c.WaitAuthorization(ctx, authURL); err != nil {
			return nil, nil, fmt.Errorf("域名 %s 校验失败: %w", authz.Identifier.Value, err)
		}
	}

	// 生成 CSR
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domains[0]},
		DNSNames: domains,
	}, leafKey)
	if err != nil {
		return nil, nil, err
	}
	der, _, err := c.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, nil, fmt.Errorf("签发失败: %w", err)
	}

	var certPEM bytes.Buffer
	for _, b := range der {
		certPEM.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b}))
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	return certPEM.Bytes(), keyPEM, nil
}

// waitForTXT 轮询公共 DNS，等 TXT 记录真的能被查到。
func waitForTXT(ctx context.Context, fqdn, want string) error {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
		got, err := lookupTXT(fqdn)
		if err != nil {
			continue
		}
		for _, v := range got {
			if v == want {
				return nil
			}
		}
	}
	return fmt.Errorf("3 分钟内没查到 TXT 记录")
}

// lookupTXT 用 DNS-over-HTTPS 查（容器里不一定有系统解析器配置）
func lookupTXT(fqdn string) ([]string, error) {
	req, _ := http.NewRequest("GET",
		"https://cloudflare-dns.com/dns-query?name="+fqdn+"&type=TXT", nil)
	req.Header.Set("Accept", "application/dns-json")
	res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Answer []struct {
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	var vals []string
	for _, a := range out.Answer {
		// DoH 返回的 TXT 带引号
		vals = append(vals, strings.Trim(a.Data, `"`))
	}
	return vals, nil
}

// saveCert 原子写入证书与私钥（单证书结构，保留给内部复用）。
func saveCert(dir string, certPEM, keyPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "cert.pem"), certPEM, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "key.pem"), keyPEM, 0o600)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}


// getRecord 查某域名某类型的当前值；没有记录返回空串。
func (c *cfProvider) getRecord(name, recType string) (string, error) {
	zone, err := c.zoneOf(name)
	if err != nil {
		return "", err
	}
	out, err := c.do("GET", "/zones/"+zone+"/dns_records?type="+recType+"&name="+name+"&per_page=1", nil)
	if err != nil {
		return "", err
	}
	if res, ok := out["result"].([]any); ok && len(res) > 0 {
		if m, ok := res[0].(map[string]any); ok {
			if v, ok := m["content"].(string); ok {
				return v, nil
			}
		}
	}
	return "", nil
}

// upsertRecord 有记录就改，没有就建。DDNS 用它把域名指向新的公网 IP。
func (c *cfProvider) upsertRecord(name, recType, ip string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	zone, err := c.zoneOf(name)
	if err != nil {
		return err
	}

	// 先找同类型同名的记录
	out, err := c.do("GET", "/zones/"+zone+"/dns_records?type="+recType+"&name="+name+"&per_page=1", nil)
	if err != nil {
		return err
	}
	if res, ok := out["result"].([]any); ok && len(res) > 0 {
		if m, ok := res[0].(map[string]any); ok {
			id, _ := m["id"].(string)
			proxied, _ := m["proxied"].(bool)
			if id != "" {
				// PATCH 而不是 PUT：只改 content，保留 proxied/ttl 等原有设置
				body := map[string]any{"content": ip}
				// 用 A/AAAA 记录必须是灰云（橙云会走 CF 代理，测速就不准了）
				if proxied {
					body["proxied"] = false
				}
				_, err := c.do("PATCH", "/zones/"+zone+"/dns_records/"+id, body)
				return err
			}
		}
	}
	_, err = c.do("POST", "/zones/"+zone+"/dns_records", map[string]any{
		"type":    recType,
		"name":    name,
		"content": ip,
		"ttl":     120,
		"proxied": false,
		"comment": "speedtest-node ddns",
	})
	return err
}
