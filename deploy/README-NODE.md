# 测速节点版（Node）—— 部署与接入

同一个镜像、同一份代码，靠 `APP_MODE` 分成两种形态：

| | 主控版 `APP_MODE=controller` | 节点版 `APP_MODE=node` |
|---|---|---|
| 网页 / 报告页 | 有 | **没有**（只返回 404） |
| 后台 / 登录 / 配置 | 有 | **没有**（进程里根本没注册这些路由） |
| 写文件 | 写 `data/config.json`、结果日志 | **完全不写**（容器根文件系统只读，`/data` 是内存盘） |
| 配置来源 | 后台 / config.json | **只有环境变量，启动时读一次** |
| 网络 | 桥接，端口只发布到 `127.0.0.1`，由反代接入 | **host 网络**，直接对外 |
| TLS | 由反代 / CDN 终止 | 自己终止（Let's Encrypt 自动证书） |
| 端点 | 全套 API | 只有 `/api/{ping,download,upload,isp}` + `/healthz` |

节点版就是一台「哑」测速服务器：只负责发数据和收数据，不存任何东西，也没有任何可以被外部改写的入口。

---

## 零、面板：证书 / DDNS / 访问限制

节点自带一个隐藏面板，与测速端点共用同一个端口：

```
https://<域名>:<NODE_PORT>/<NODE_PANEL_PATH>
```

启用条件：`NODE_PANEL_PATH` + `NODE_PANEL_USER` + `NODE_PANEL_PASS` 三个都填。

### 1. 证书（可多张，按域名分开管理）

| 能力 | 说明 |
|---|---|
| 单独签发 | 填一个或多个域名（逗号分隔）→ 签一张证书，存在 `certs/<域名>/` |
| 多张并存 | 给 A 域名签一张、B 域名再签一张，互不影响；TLS 按 SNI 自动选对的那张 |
| 单独删除 | 每张证书右侧「删除这张」，只删这一张，其余照常工作 |
| 单独续期 | 每张独立判断剩余天数，到阈值各自续，互不干扰 |

证书走 **DNS-01** 校验，只加一条 TXT 记录，**不需要 80/443**，NAT 机器可用。

两种驱动方式：
- **Cloudflare 自动**：面板填 API Token（`Zone.DNS:Edit`），自动加 TXT、自动删、自动续期，全程无人值守
- **手动**：面板显示 TXT，你去 DNS 后台添加，点一下继续

### 2. DDNS（IPv4 / IPv6 分开）

动态 IP 必备：定时检测本机公网 IP，与 Cloudflare 记录不一致就自动改。

- **IPv4（A 记录）** 和 **IPv6（AAAA）** 完全分开：各自一个区域、各自添加、各自删除
- 每行实时显示「本机 IP / DNS 当前值 / 状态（已一致 / 已更新 / 出错）」
- 默认每 5 分钟检查一次（`NODE_DDNS_INTERVAL`），只在值变化时才调 API
- 复用证书用的同一个 Cloudflare Token

也可以写在 `.env` 里（面板的改动只在内存生效，重启回到 `.env`）：

```env
NODE_DDNS=speeda.example.com:A,speedb.example.com:AAAA
NODE_DDNS_INTERVAL=5
```

### 3. 访问限制

允许的 Host / 访客 IP 段 / 允许的 Origin，保存立即生效。

**面板完全豁免这三个白名单**（只靠隐藏路径 + 账号密码 + 会话签名保护）。
原因：白名单是用来限制"谁能拿这台机器测速"的，不该限制"谁能来管这台机器"；
否则一旦 IP 段填错，面板自己就先被锁在外面，只能改 `.env` 或删配置文件才能救回来。

### 3.1 配置持久化（重启不丢）

面板里的所有改动都写进 **`certs/panel.json`**（在挂载卷里，容器重建也在）：

| 内容 | 说明 |
|---|---|
| Cloudflare Token | 签发证书与 DDNS 共用 |
| 访问限制 | Host / CIDR / Origin |
| DDNS 规则 | 每个域名 + 记录类型 |
| 续期阈值 | 剩余多少天开始续 |

启动顺序是：先读 `.env` 铺底 → 再用 `panel.json` 覆盖。
所以 `.env` 退化成"首次安装的默认值"，日常维护在面板里改就行。

文件权限 0600（里面含 Token）。想看当前值：`cat certs/panel.json`；想恢复默认：删掉它再重启。

### 4. 没有证书时怎么办

**没有证书时节点直接以明文 HTTP 运行**，浏览器能正常打开面板去签，不会卡死。
此时测速端点返回 503 并提示先签证书（明文测速会被 https 页面按混合内容拦掉）。

签好证书后进程会自动重启切到 HTTPS；删掉最后一张证书也会自动退回 HTTP。

## 一、把代码传到自己的 GitHub（只需做一次）

```bash
cd speedtest                       # 有 go.mod 的那一层
git init
git add -A
git commit -m "speedtest: controller + node"
git remote add origin https://github.com/<用户名>/<仓库名>.git
git branch -M main
git push -u origin main
git tag v1.4.3 && git push origin v1.4.3     # 触发 Actions 构建镜像
```

产物：

```
ghcr.io/<用户名>/<仓库名>-node:1.4.3
ghcr.io/<用户名>/<仓库名>:latest
```

GHCR 包默认私有，节点机要能拉：仓库 → 头像 → Your packages → 选中该包 →
Package settings → Change visibility → **Public**（推荐）。
私有的话在节点机登录一次：

```bash
echo <有 read:packages 权限的 PAT> | docker login ghcr.io -u <用户名> --password-stdin
```

---

## 二、每台机器怎么部署（多机最省事的路径）

### 2.1 一条命令（推荐，脚本会把 .env / compose 都写好）

```bash
# 公网直连 + 自动证书（每台机器换一个域名和端口即可）
curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/install-node.sh \
  | sudo bash -s -- \
      --image ghcr.io/<用户名>/<仓库>:latest \
      --port 443 \
      --acme-domain node1.example.com \
      --acme-email you@example.com \
      --origin https://speed.example.com \
      --host node1.example.com
```

装完脚本会打印**要填进主控后台的节点地址**，以及证书状态查询命令。

### 2.2 多台机器 / 泛域名

每台机器一个域名最省事（脚本一条命令一个）。如果你想用一张 `*.example.com` 泛域名证书覆盖多台，
目前不支持 —— 每台机器各自向 Let's Encrypt 申请自己那张，反而更简单、续期也互不影响。

### 2.3 用仓库里的 Compose（想自己管文件时）

```bash
mkdir -p /opt/speedtest-node && cd /opt/speedtest-node
curl -fsSLO https://raw.githubusercontent.com/<用户名>/<仓库>/main/docker-compose.node.yml
curl -fsSLO https://raw.githubusercontent.com/<用户名>/<仓库>/main/.env.node.example
mv .env.node.example .env && vi .env      # 只改 IMAGE / NODE_PORT / NODE_TLS_MODE / NODE_ACME_DOMAINS
docker compose up -d
docker compose logs -f --tail=100
```

`docker-compose.node.yml` 与 `.env.node.example` 里**每个配置项都写了中文注释**
（作用、默认值、什么时候要改），可以直接当配置手册翻。

### 2.4 多机批量（十台以上）

每台机器最少只有 4 个值不同：`NODE_PORT`、`NODE_TLS_MODE`、`NODE_ACME_DOMAINS`、`NODE_PATH_PREFIX`。
用同一个镜像，按序号生成 `.env` 即可：

```bash
#!/usr/bin/env bash
# deploy-all.sh：把节点铺到多台机器（需要提前配好 ssh 免密）
IMAGE=ghcr.io/<用户名>/<仓库>:latest
ORIGIN=https://speed.example.com
i=1
for host in 10.0.0.11 10.0.0.12 10.0.0.13; do
  ssh root@$host "mkdir -p /opt/speedtest-node && cd /opt/speedtest-node &&
    IMAGE=$IMAGE NODE_PORT=443 TLS_MODE=acme DOMAIN=node$i.example.com ORIGIN=$ORIGIN bash -s" <<'EOF'
cat > .env <<ENV
IMAGE=${IMAGE}
HTTP_HOST=0.0.0.0
NODE_PORT=${NODE_PORT}
NODE_TLS_MODE=${TLS_MODE}
NODE_ACME_DOMAINS=${DOMAIN}
NODE_ACME_EMAIL=you@example.com
NODE_ACME_DIR=/certs/acme
NODE_ALLOW_ORIGINS=${ORIGIN}
NODE_ALLOW_HOSTS=${DOMAIN}
TZ=Asia/Shanghai
ENV
chmod 600 .env
docker compose -f docker-compose.node.yml up -d
EOF
  i=$((i+1))
done
```

> 每台机器要有自己的域名（`node1/node2/...`），浏览器是按域名校验证书的。

### 2.5 卸载

```bash
curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/uninstall-node.sh | sudo bash -s -- --yes
# 连镜像一起删：加 --purge-image
```

---

## 三、接进主控版

1. 主控后台 → 节点 → 添加节点；
2. 地址填脚本打印的那一行，例如 `https://node1.example.com` 或 `https://node1.example.com:8443`；
3. 有路径前缀就带上：`https://node1.example.com/n1`；
4. 保存后回首页，节点会显示实测延迟；点「测速」能跑满带宽即接入成功。

IPv4 / IPv6 是两个独立字段：只有 A 记录的节点填 IPv4 一栏，只有 AAAA 的填 IPv6 一栏。

---

## 四、证书细节

| 项 | 说明 |
|---|---|
| 签发 | 面板点一下即可，自动加 TXT、自动校验（约 30~60 秒） |
| 续期 | 到期前 30 天自动续，热加载不断连；每 6 小时检查一次 |
| 校验 | DNS-01，只加一条 TXT 记录，**不需要 80/443**，NAT 机器可用 |
| 缓存 | `./certs/acme`，备份它等于备份证书；删掉会重新申请 |
| 自查 | `curl -s https://<域名>:<端口>/api/tls`（含 daysLeft / issuer / state） |
| 调试 | `NODE_ACME_STAGING=1` 用测试环境，正式环境每周限额 50 张 |

### NAT 机器没有 80 端口怎么签证书？

**不用 80 也能签**：`acme` 模式默认用 **TLS-ALPN-01** 校验，整个握手发生在你节点自己的
HTTPS 端口（8445/443）内，Let's Encrypt 直接连这个端口完成验证。前提只有两条：

1. 域名 A/AAAA 记录指向这台机器的**公网 IP**（NAT 后面就填出口 IP），DNS 用**灰云直连**；
2. 这个 HTTPS 端口能被公网访问（`nc -zv 域名 8445` 通）。

所以 NAT 机器只要把 8445 端口映射出去就行，**完全不需要 80**。
`NODE_ACME_HTTP_ADDR` 留空即可（它只用于额外的 HTTP-01 校验和 http→https 跳转）。

只有当 443 端口被别的服务彻底占死、你又没法让节点用别的端口时，才需要绕道 HTTP-01：

```env
NODE_TLS_MODE=acme
NODE_ACME_DOMAINS=node1.example.com
NODE_ACME_CHALLENGE=http-01        # 显式改用 HTTP-01
NODE_ACME_HTTP_ADDR=:80            # 需要一个能对外的 80
```

状态自查：`curl -s https://<域名>:<端口>/api/tls`，返回里 `challenge` 会写明当前用的是哪种校验。

### 附：为什么节点自己终止 TLS 时只跑 http/1.1

h2 会把浏览器的 6 条并发下载**复用成 1 条 TCP**，多线测速立刻退化成单线（一条 TCP 在长途链路上可能只有 20 Mbps）。
节点的 TLS 配置里写死了 `NextProtos: ["http/1.1"]`，就是为了让 6 个请求真实建立 6 条连接。
反代 / CDN 场景同理：关掉该站点的 HTTP/2。

---

## 五、为什么说「外部改不了、也注入不进来」

| 防护 | 做法 |
|---|---|
| 没有写入面 | 根文件系统 `read_only`；`/data` 是 `tmpfs`；不挂 `config.json`；没有后台路由 |
| 没有提权面 | 非 root（uid 10001）、`cap_drop: ALL`、`no-new-privileges`、`pids_limit` |
| 配置只读 | 启动时读一次环境变量，非法值直接退出（前缀 / CIDR / 域名都校验），运行期无法修改 |
| 三道白名单 | `NODE_ALLOW_CIDRS`（访客 IP）、`NODE_ALLOW_HOSTS`（Host 头）、`NODE_ALLOW_ORIGINS`（跨源），未命中 403 |
| 方法与头部限制 | 只允许 GET/HEAD/POST/OPTIONS，`MaxHeaderBytes=16KiB`，`X-Robots-Tag: noindex` |

需要绑 443/80 时唯一多给的权限是 `NET_BIND_SERVICE`（只用于绑定特权端口）。

---

## 六、常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| 节点延迟正常，但测速 **0.00** | 证书还没签下来，或域名没解析到本机 | `curl -s https://<域名>/api/tls` 看 state 是否 ready；未 ready 检查 DNS 与端口放通 |
| 首页看不到 IPv4 地址 | 探测目标被 CDN 落到 v6 边缘 | 主控后台「访客地址解析 → 检测当前解析」看是哪个头带回地址 |
| `acme` 一直 pending / 签发失败 | 域名没解析到本机、DNS 走了 CDN 代理、或端口没放通 | `dig +short <域名>` 应为公网 IP；DNS 记录必须是**灰云**；`nc -zv <域名> 443` 要通 |
| 签发报 `rejectedIdentifier` | 域名被 CA 策略拒绝（如 example.com） | 换真实自有域名 |
| 签发报 `too many certificates` | 正式环境限额（每周 50 张） | 调试一律先开 `NODE_ACME_STAGING=1` |
| 白名单 403 | 访客 IP 不在 `NODE_ALLOW_CIDRS`，或反代场景没配 `TRUSTED_PROXIES` | 开 `LOG_CLIENT_IP=1` 看日志里的解析结果 |
| 多线 ≈ 单线 | h2 复用单条 TCP | 节点已关 h2；反代/CDN 场景需在那一层关 h2 |
| 重启后重新签发证书 | `certs/` 没挂持久卷 | 保持 `./certs:/certs:rw` |
| 签发报 `bind: permission denied` | 容器非 root 绑 <1024 端口 | 用 8445 这类高端口，或设 `sysctl -w net.ipv4.ip_unprivileged_port_start=0` |

排错三连：

```bash
docker logs --tail=50 speedtest-node
curl -s https://<域名>/api/tls      # 证书状态（acme / auto）
curl -s https://<域名>/api/ping     # 端点是否可达
```
