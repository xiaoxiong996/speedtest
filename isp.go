package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ipMeta 是访客公网 IP 的归属信息。
// ISP 是中文运营商名（如“中国电信”），ISPEn 是对应的英文名（如 China Telecom），
// 前端按当前界面语言二选一展示；ASN/ASName 是它背后的自治域，
// 例如 ASN=AS4134、ASName=CHINANET-BACKBONE。
type ipMeta struct {
	ISP    string
	ISPEn  string
	ASN    string
	ASName string
	City   string
	Region string
	// Lat / Lon 是归属地近似坐标，前台「设置 · 当前连接」里展示（没有时为 0）
	Lat float64
	Lon float64
}

func (m ipMeta) empty() bool {
	return m.ISP == "" && m.ASN == "" && m.City == ""
}

type ipMetaEntry struct {
	meta ipMeta
	exp  time.Time
}

var ipMetaCache = struct {
	sync.Mutex
	m map[string]ipMetaEntry
}{m: map[string]ipMetaEntry{}}

// lookupIPMeta 依次尝试：ip-api 在线查询 -> DNS 反解。结果缓存 6 小时。
// 注意：在线查询会把访客 IP 发给 ip-api.com，仅用于展示运营商/城市。
func lookupIPMeta(ip string) ipMeta {
	if ip == "" || net.ParseIP(ip) == nil {
		return ipMeta{}
	}
	ipMetaCache.Lock()
	if e, ok := ipMetaCache.m[ip]; ok && time.Now().Before(e.exp) {
		ipMetaCache.Unlock()
		return e.meta
	}
	ipMetaCache.Unlock()

	var meta ipMeta
	var ok bool
	if !isPublicIP(ip) {
		// 内网 / 回环地址没有归属信息，直接返回，避免把 Docker 网关当成访客。
		return ipMeta{}
	}
	if strings.Contains(ip, ":") {
		// ip-api 免费版不支持 IPv6
		meta, ok = queryIPWho(ip)
	} else {
		meta, ok = queryIPAPI(ip)
		if !ok {
			meta, ok = queryIPWho(ip)
		}
	}
	if !ok {
		zh, en := ptrISP(ip)
		meta = ipMeta{ISP: zh, ISPEn: en}
	}

	ipMetaCache.Lock()
	if len(ipMetaCache.m) > 3000 {
		ipMetaCache.m = map[string]ipMetaEntry{}
	}
	ttl := 6 * time.Hour
	if meta.empty() {
		ttl = 10 * time.Minute
	}
	ipMetaCache.m[ip] = ipMetaEntry{meta: meta, exp: time.Now().Add(ttl)}
	ipMetaCache.Unlock()
	return meta
}

type ipAPIResp struct {
	Status  string  `json:"status"`
	Country string  `json:"country"`
	Region  string  `json:"regionName"`
	City    string  `json:"city"`
	ISP     string  `json:"isp"`
	Org     string  `json:"org"`
	AS      string  `json:"as"`
	Query   string  `json:"query"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

// ipWhoResp 是 ipwho.is 的响应（支持 IPv6，作为 ip-api 的补充）。
type ipWhoResp struct {
	Success   bool    `json:"success"`
	Country   string  `json:"country"`
	Region    string  `json:"region"`
	City      string  `json:"city"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Connection struct {
		ASN    int    `json:"asn"`
		Org    string `json:"org"`
		ISP    string `json:"isp"`
		Domain string `json:"domain"`
	} `json:"connection"`
}

// queryIPWho 走 ipwho.is 查询归属信息，IPv4 / IPv6 都支持。
func queryIPWho(ip string) (ipMeta, bool) {
	client := &http.Client{Timeout: 2500 * time.Millisecond}
	// 不传 lang，拿英文原始名，中文名由 splitISP 统一映射
	resp, err := client.Get("https://ipwho.is/" + ip)
	if err != nil {
		return ipMeta{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ipMeta{}, false
	}
	var body ipWhoResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || !body.Success {
		return ipMeta{}, false
	}
	// ipwho 的 isp 字段有时是街道地址（如 "No.31,Jin-rong Street"），优先用 org
	isp := strings.TrimSpace(body.Connection.Org)
	if isp == "" {
		isp = strings.TrimSpace(body.Connection.ISP)
	}
	city := strings.TrimSpace(body.City)
	if city == "" {
		city = strings.TrimSpace(body.Region)
	}
	if isp == "" && city == "" {
		return ipMeta{}, false
	}
	asn := ""
	if body.Connection.ASN > 0 {
		asn = "AS" + strconv.Itoa(body.Connection.ASN)
	}
	zh, en := splitISP(isp, body.Connection.Org+" "+body.Connection.Domain)
	return ipMeta{
		ISP:    zh,
		ISPEn:  en,
		ASN:    asn,
		ASName: strings.TrimSpace(body.Connection.Org),
		City:   city,
		Region: strings.TrimSpace(body.Region),
		Lat:    body.Latitude,
		Lon:    body.Longitude,
	}, true
}

func queryIPAPI(ip string) (ipMeta, bool) {
	client := &http.Client{Timeout: 1800 * time.Millisecond}
	url := "http://ip-api.com/json/" + ip + "?fields=status,country,regionName,city,isp,org,as,lat,lon"
	resp, err := client.Get(url)
	if err != nil {
		return ipMeta{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ipMeta{}, false
	}
	var body ipAPIResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Status != "success" {
		return ipMeta{}, false
	}

	asn, asName := splitAS(body.AS)
	isp := strings.TrimSpace(body.ISP)
	if isp == "" {
		isp = strings.TrimSpace(body.Org)
	}
	city := strings.TrimSpace(body.City)
	if city == "" {
		city = strings.TrimSpace(body.Region)
	}
	if isp == "" && asName == "" && city == "" {
		return ipMeta{}, false
	}
	zh, en := splitISP(isp, asName)
	return ipMeta{
		ISP:    zh,
		ISPEn:  en,
		ASN:    asn,
		ASName: asName,
		City:   city,
		Region: strings.TrimSpace(body.Region),
		Lat:    body.Lat,
		Lon:    body.Lon,
	}, true
}

// splitAS 把 ip-api 的 "AS4134 CHINANET-BACKBONE" 拆成编号与机构名。
func splitAS(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	parts := strings.SplitN(s, " ", 2)
	asn := strings.TrimSpace(parts[0])
	if !strings.HasPrefix(strings.ToUpper(asn), "AS") {
		return "", s
	}
	name := ""
	if len(parts) > 1 {
		name = strings.TrimSpace(parts[1])
	}
	return strings.ToUpper(asn), name
}

// ispNames 是常见运营商 / 云服务商的中英文对照表。
// 关键词按小写匹配 isp + asName，命中后返回固定的中英文展示名。
var ispNames = []struct {
	keys []string
	zh   string
	en   string
}{
	{[]string{"chinanet", "chinatelecom", "china telecom", "163data"}, "中国电信", "China Telecom"},
	{[]string{"china unicom", "chinaunicom", "china169", "cncgroup", "china unicom"}, "中国联通", "China Unicom"},
	{[]string{"china mobile", "chinamobile", "cmnet", "cmcc", "cmi"}, "中国移动", "China Mobile"},
	{[]string{"cernet", "edu.cn"}, "教育网", "CERNET"},
	{[]string{"aliyun", "alibaba", "alicloud"}, "阿里云", "Alibaba Cloud"},
	{[]string{"tencent", "qcloud"}, "腾讯云", "Tencent Cloud"},
	{[]string{"huawei", "huaweicloud"}, "华为云", "Huawei Cloud"},
	{[]string{"cloudflare"}, "Cloudflare", "Cloudflare"},
	{[]string{"amazon", "aws"}, "Amazon AWS", "Amazon AWS"},
	{[]string{"microsoft", "azure"}, "Microsoft Azure", "Microsoft Azure"},
	{[]string{"google", "gcp"}, "Google Cloud", "Google Cloud"},
	{[]string{"vultr", "choopa"}, "Vultr", "Vultr"},
	{[]string{"digitalocean"}, "DigitalOcean", "DigitalOcean"},
	{[]string{"linode", "akamai"}, "Akamai", "Akamai"},
	{[]string{"ovh"}, "OVH", "OVH"},
	{[]string{"hetzner"}, "Hetzner", "Hetzner"},
	{[]string{"cogent"}, "Cogent", "Cogent"},
	{[]string{"level3", "lumen"}, "Lumen", "Lumen"},
	{[]string{"gtt", "tinet"}, "GTT", "GTT"},
	{[]string{"telia"}, "Telia", "Telia"},
}

func matchISP(all string) (string, string, bool) {
	all = strings.ToLower(all)
	for _, item := range ispNames {
		for _, k := range item.keys {
			if strings.Contains(all, k) {
				return item.zh, item.en, true
			}
		}
	}
	return "", "", false
}

// splitISP 把原始 ISP / AS 机构名归一到大陆用户熟悉的运营商名，
// 同时返回中英文两个展示名：中文用于中文界面，英文用于英文界面。
// 命中不了对照表时，两个语言都用原始名。
func splitISP(s, asName string) (string, string) {
	if zh, en, ok := matchISP(strings.TrimSpace(s) + " " + strings.TrimSpace(asName)); ok {
		return zh, en
	}

	name := strings.TrimSpace(s)
	if name == "" {
		name = strings.TrimSpace(asName)
	}
	if name == "" {
		return "", ""
	}
	// 归一后仍过长时截断，避免撑破前端一行。
	if r := []rune(name); len(r) > 26 {
		name = string(r[:26])
	}
	return name, name
}

// ptrISP 反解域名并美化，作为在线查询失败时的兜底，返回中英文两个名字。
func ptrISP(ip string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return "", ""
	}
	return prettifyHost(names[0])
}

func prettifyHost(host string) (string, string) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return "", ""
	}
	if zh, en, ok := matchISP(host); ok {
		return zh, en
	}
	parts := strings.Split(host, ".")
	keep := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || isNumericLabel(p) {
			continue
		}
		switch p {
		case "static", "dynamic", "broadband", "ppp", "dial", "ip", "dsl", "adsl", "cable", "host", "reverse":
			continue
		}
		keep = append(keep, p)
	}
	if len(keep) == 0 {
		return "", ""
	}
	if len(keep) >= 2 {
		keep = keep[len(keep)-2:]
	}
	out := strings.Join(keep, ".")
	if len(out) > 32 {
		out = out[:32]
	}
	return out, out
}

func isNumericLabel(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// cachedMeta 只读取缓存，不发起查询，用于首屏快速返回。
func cachedMeta(ip string) ipMeta {
	ipMetaCache.Lock()
	defer ipMetaCache.Unlock()
	if e, ok := ipMetaCache.m[ip]; ok && time.Now().Before(e.exp) {
		return e.meta
	}
	return ipMeta{}
}

// cachedISP 只读取缓存中的运营商名，保留给旧调用点使用。
func cachedISP(ip string) string {
	return cachedMeta(ip).ISP
}
