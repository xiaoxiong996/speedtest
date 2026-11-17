# SpeedTest 自建测速站

仿 speedtest.net 的极简测速页面 + 自建多节点，服务端为单文件 Go 程序（无第三方依赖），Docker 部署为 linux/amd64。

## 目录结构

```
main.go / config.go / api.go / speed.go / http_util.go / isp.go / node.go   服务端
web/                                                                       前端（index / admin / i18n / prefs / 样式 / 字体）
Dockerfile                                                                 镜像构建（主控/节点共用同一镜像）
docker-compose.yml                                                         主控版编排（web + 后台 + 测速）
docker-compose.node.yml                                                    纯测速节点版编排
.env.example / .env.node.example                                           环境变量示例
data/                                                                      运行数据（config.json，自动生成）
```

## 两种运行模式

同一份代码、同一个镜像，用环境变量 `APP_MODE` 切换：

| 模式 | `APP_MODE` | 端口 | 能力 | 落盘 |
|---|---|---|---|---|
| **主控版**（默认） | `controller` | 3333（监听回环，走反代） | 完整站点：前端 + 后台 + 测速 + 报告 | `data/`（config.json / results.jsonl） |
| **纯测速节点版** | `node` | 8443（对外监听） | 只有 `/api/ping`、`/api/isp`、`/api/download`、`/api/upload` | **完全不落盘** |

节点版**没有任何后台、web 页面、报告接口**，进程里根本不注册这些路由；配置只在启动时从环境变量读入一次，运行期外部无法修改，也无法通过伪造请求头注入。根文件系统只读、`cap_drop: ALL`、禁止提权，适合放在不受信任的小鸡上做纯测速转发点。

## 上传 GitHub + 一键部署

这套代码已配好 GitHub Actions：推送 `v*` 标签就自动构建镜像并推到你自己的 **GitHub Container Registry (ghcr.io)**，小鸡上只需一份 Compose 即可拉起，不用装 Go、不用本地构建。

步骤：

```bash
# 1. 在 GitHub 新建一个空仓库（例如 speedtest）
cd 项目目录
git init
git add . && git commit -m "init"
git branch -M main
git remote add origin https://github.com/你的用户名/你的仓库名.git
git push -u origin main

# 2. 打标签触发自动构建（ ghcr.io/你的用户名/你的仓库名:v1.2.36 与 :latest ）
git tag v1.2.36 && git push origin v1.2.36
```

- 在仓库 **Settings → Actions → General → Workflow permissions** 勾选 **Read and write permissions**，否则推 ghcr.io 会没权限。
- 镜像是**私有仓库**时，小鸡上先 `docker login ghcr.io -u 用户名`（密码用 GitHub 的 Personal Access Token，需 `read:packages` 权限）；公开仓库可跳过。
- 若不想用 ghcr.io，也可以本地 `--build`，完全不依赖 GitHub Actions。

小鸡上一键部署（把 `你的用户名/你的仓库名` 换成实际值）：

```bash
# 主控版
mkdir -p /app/speed && cd /app/speed
IMAGE=ghcr.io/你的用户名/你的仓库名:1.2.36 docker compose up -d
# 节点版
IMAGE=ghcr.io/你的用户名/你的仓库名:1.2.36 docker compose -f docker-compose.node.yml up -d
```

本地无镜像时 `docker compose up -d` 会自动从 ghcr.io 拉取；`--build` 则用源码现构建。

## 快速部署（反向代理 + HTTPS，推荐）

程序默认**只监听 `127.0.0.1:3333`**，由反代把你的 HTTPS 域名转发到该地址；这样 HTTP 明文端口不会暴露在公网。对外域名可以随便换，程序会自动识别反代传来的 `Host`，不需要改配置。

```bash
mkdir -p /app/speed && cd /app/speed
# 上传本项目文件后
cat > .env <<EOF
HTTP_HOST=127.0.0.1
HOST_PORT=3333
TRUSTED_PROXIES=127.0.0.1,::1
PUBLIC_BASE_URL=
ADMIN_USER=admin
ADMIN_PASSWORD=改成强密码
ADMIN_PATH=你自己的隐藏路径
SITE_NAME=SpeedTest
SITE_TITLE_SUFFIX=网络测速
NODE_NAME=默认节点
NODE_LOCATION=美国
NODE_SPONSOR=自建机房
# 本机节点地址留空即可自动跟随当前访问域名，换反代域名不用再改后台
NODE_URL=
NODE_URL6=
EOF
mkdir -p data && chown -R 10001:10001 data   # 容器内以 uid 10001 运行，需可写
docker compose up -d --build
```

反代配置要点（Nginx 示例）：

```nginx
location / {
    proxy_pass http://127.0.0.1:3333;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
    # 若这里改写了 Host（例如写 127.0.0.1:3333），再加下一行让程序识别真实域名：
    # proxy_set_header X-Forwarded-Host $host;
}
```

> `PUBLIC_BASE_URL` 默认留空即可：程序按当前请求的 `Host` + 协议自动推导对外地址，`/api/info` 下发给前端的跨协议探测地址会跟着反代域名走，**换域名零改动**。只有反代把 `Host` 改写成 `127.0.0.1:3333` 这类内网地址时，才需要在 `.env` 里显式写 `PUBLIC_BASE_URL=https://你的域名`。
> `SITE_TITLE_SUFFIX` 控制浏览器标签页「站点名 · 后缀」的后半段；留空时中文界面显示「网络测速」、英文界面显示「Speed Test」，也可在后台随时改。
> `TRUSTED_PROXIES` 只允许这些来源覆盖访客 IP 和 HTTPS 标记；`TRUSTED_IP_HEADERS` 默认只认反代**覆盖写入**的 `X-Real-IP`，不认访客可自填的 `CF-Connecting-IP` / `X-Forwarded-For`。直连场景务必把 `TRUSTED_PROXIES` 留空，否则任何人都能伪造头绕过报告归属和登录限流。
> 首次启动若未设置 `ADMIN_PASSWORD`，会生成随机初始密码并打印一次；未设置 `ADMIN_PATH` 则生成随机隐藏路径。请登录后立即改密并记牢路径。

### 双栈补探域名（推荐，解决"切协议拿不到地址"）

浏览器切到 IPv4/IPv6 按钮时并不会真的换网络协议，用同一个双栈域名补探只会拿到当前协议的地址。
最稳的做法是配两个**单栈域名**，用 SNI 分流到同一个后端：

- `v4.example.com` 只解析 A 记录（IPv4）→ 服务器 IPv4
- `v6.example.com` 只解析 AAAA 记录（IPv6）→ 服务器 IPv6
- 反代为这两个域名各建一条规则，都 `proxy_pass` 到 `127.0.0.1:3333`，证书用通配符即可

在后台「站点设置 → 双协议补探域名」分别填入这两个域名（留空则沿用当前访问域名）。
这样访客切到 IPv4 时前端请求 IPv4 单栈域名、切到 IPv6 时请求 IPv6 单栈域名，服务端就能看到访客在对应协议下的真实地址。
也可用 `.env` 的 `PROBE_V4` / `PROBE_V6` 设初始值（仅首次生效，之后以后台为准）。

> 主反代域名可以随时换，只要 DNS / 反代规则指到同一后端即可；程序自动识别新域名。
> 唯一需要注意：后台「测速节点」里本机节点的 IPv4/IPv6 地址如果填了旧域名，测速流量仍会走旧域名。
> 想彻底免维护，就把本机节点两个地址留空（或删掉旧值），前端会自动用当前访问域名 / 上面的补探域名。

### 自定义路由路径段

后台「站点设置 → 路由路径段」可改前台链接里的各段文字：

- 中文前缀（默认 `zh-Hans`）、英文前缀（默认 `en`）
- IPv4 协议段（默认 `ipv4`）、IPv6 协议段（默认 `ipv6`）
- 报告段（默认 `result`）
- 后台入口隐藏路径（即 `ADMIN_PATH`）

填字母数字/连字符/下划线，留空恢复默认；各段不能重复（重复会自动回退默认）。
前端通过同源 `/routes.js` 读取当前配置，所以 CSP 无需放行 inline 脚本。
**注意**：改完路径段后，按旧路径分享出去的链接会失效，请谨慎修改。可用 `.env` 的
`PATH_LANG_ZH` / `PATH_LANG_EN` / `PATH_V4` / `PATH_V6` / `PATH_RESULT` 设初始值。

访问你的反代域名即可。**后台入口是隐藏路径**，`/admin`、`/admin.html` 都返回 404；首页那两个「管理后台」按钮只是摆设，点了不会跳转。登录后台后可在「站点设置 → 后台入口（隐藏路径）」随时改成别的路径。

> `.env` 只在**首次启动**（`data/config.json` 不存在时）写入账号与站点名；之后以后台数据库为准。

## 增加测速节点（纯节点版）

推荐在每台小鸡上用**节点版**部署——它只测速、无后台无页面、配置只能写死在 Compose 里，无法被外部改动。

### 1. 在节点小鸡上部署

```bash
mkdir -p /app/speed-node && cd /app/speed-node
# 只需两个文件：docker-compose.node.yml 与 .env.node.example（复制为 .env.node 后修改）
cp .env.node.example .env.node
# 编辑 .env.node，按需填白名单与端口
docker compose --env-file .env.node -f docker-compose.node.yml up -d --build
```

节点版默认监听 `0.0.0.0:8443`（.host 网络），可直接对外，也可由反代转发。容器只读、无数据卷，删掉重建也不丢任何配置——因为配置全在 `.env.node` 里。

### 2. 三道访问白名单（都在 `.env.node` 配置）

| 变量 | 作用 | 留空 | 示例 |
|---|---|---|---|
| `NODE_ALLOW_CIDRS` | 只允许这些访客 IP 段测速 | 不限制 | `1.2.3.0/24,2001:db8::/32` |
| `NODE_ALLOW_HOSTS` | 只允许这些 Host 域名访问 | 不限制 | `node.example.com,*.speed.net` |
| `NODE_ALLOW_ORIGINS` | 只允许这些来源页面跨源调用 | 任意 Origin | `https://speed.example.com` |
| `NODE_PATH_PREFIX` | 测速端点 base 路径（加在 `/api` 前） | `/api/ping` | `/s3cr3t` → `/s3cr3t/api/ping` |

三道白名单都在**请求进入测速逻辑之前**校验，不匹配直接 403。域名校验用 `Host` 头、`Origin` 校验用浏览器的 `Origin` 头，都不是客户端可随意伪造后仍能生效的字段；IP 段用真实访客 IP（按 `TRUSTED_PROXIES` / `TRUSTED_IP_HEADERS` 解析）。

> 若需更隐蔽，把 `NODE_PATH_PREFIX` 设成随机串（如 `/x7k2`），并把主控后台的节点地址填成 `https://节点域名:8443/x7k2/`，别人扫描默认 `/api/ping` 会扑空。
> 设了 `NODE_ALLOW_HOSTS` 时，主控后台节点地址必须用该域名访问，直接用 IP 会被拒。
> 若节点也经过 CDN/反代，如实配置 `TRUSTED_PROXIES` + `TRUSTED_IP_HEADERS`，否则 `NODE_ALLOW_CIDRS` 会拿到 CDN 节点 IP。

### 3. 在主控后台添加节点

主站后台「测速节点 → 添加节点」：

- 节点名称：香港节点
- 地区名称：中国香港
- IPv4 地址：`https://节点域名:8443/`（若设了 `NODE_PATH_PREFIX=/s3cr3t`，则填 `https://节点域名:8443/s3cr3t/`）
- IPv6 地址：同上，或该域名有 AAAA 记录时填对应地址

一台节点可以只填 IPv4、只填 IPv6，或两个都填（双栈）。点「测试」会分别探测已填写的地址。默认禁止服务端探测内网地址，内部节点可设 `ALLOW_PRIVATE_PROBE=1` 显式开启。
前端只列**当前协议**可用的节点，用户也可点击「更换」手动切换；节点行右侧显示实测延迟，
延迟最低的会打上「最低延迟」标记。

> 节点端点的 CORS：节点版会根据 `NODE_ALLOW_ORIGINS` 精确回显允许的 Origin；主控后台用「测试」探测节点时是不带 Origin 的服务端请求，不受影响。

**自动选点开关**：后台「站点设置 → 前台行为 → 按延迟自动选择节点」。

- 开启（默认）：前台并行测各节点延迟，按从低到高排序并自动选中最低的那个；切换 IPv4 / IPv6 后重新排一次。
- 关闭：严格按后台节点列表的顺序来（默认节点优先），不排序、也不自动跳节点；列表里的延迟只作参考。

用户手动点过某个节点后，本次会话内不再被自动切换（换协议会重新开始自动挑选）。

跨机节点需要浏览器能直接访问该地址，因此每台节点都要开放对应端口（CORS 已内置）。

## 后台功能

- **概览**：节点数、运行时长、最近 300 条测速记录（内存保存）；每条记录可点击整行或右侧报告编号，
  直接打开对应的测速报告页（链接带语言与协议前缀）
- **测速节点**：增删改、启用/停用、连通性测试；可选填纬度/经度，填了前台节点列表才显示访客到节点的距离
- **站点设置**：站点名、默认节点（IPv4 / IPv6 各一个）、并发连接数、上下行时长、表盘量程、是否允许切换节点、是否按延迟自动选择节点、是否显示访客 IP、是否记录结果、页脚文案
- **账号安全**：修改管理员账号与密码（PBKDF2-SHA256 加盐存储）

## 测速实现

- 延迟：5 秒内连续打 `/api/ping`，取最小值，抖动取平均偏差
- 下载：`/api/download` 由服务端从内存缓冲（4MB 随机块）持续回写，纯内存不落盘；前端用 Blob Worker 多连接并发拉流，按字节数增量计算速率
- 时长：延迟/抖动/丢包固定测 5 秒，下载、上传各 15 秒；**报告取最后 10 秒的平均值**（前 5 秒留给 TCP 慢启动），
  表盘读数同样是「最近 10 秒平均」，所以指针爬升到稳态后基本不动，和最终报告数字一致
- 上传：`/api/upload` 服务端直接丢弃请求体；前端多路 XHR 并发推送数据块（默认 1MB，下载速度超过 300Mbps 时改用 4MB）
- 表盘按对数刻度映射，量程在读数稳定后自动收敛到整齐档位（10/25/50/100/250/500/1000…）
- 单连接/多连接可在前台「连接」处切换，多连接数由后台配置
- 测速中显示：下载/上传实时值、Ping·抖动·丢包、四项体验评级（浏览网页 / 游戏 / 视频 / 通话，
  按吞吐与延迟映射为 0-5 点）、对数刻度表盘；完成后自动进入报告页

## 测速结果报告页

每次测速完成后，服务端会生成 11 位数字报告编号（例如 `19918499857`），并在页面底部给出「结果 ID / 复制链接 / 查看报告」。

- 报告地址带语言与协议前缀：`http://<站点>/zh-Hans/ipv4/result/<报告编号>`（英文界面是 `/en/...`，
  走 IPv6 测速时是 `.../ipv6/result/...`），测速完成后自动跳转；老的 `/zh-Hans/result/<编号>`、`/result/<编号>` 仍然可用
- **测速历史**：点首页工具栏「结果」或菜单「结果」打开，列出这台浏览器最近 30 次测速（时间、下载/上传、Ping、协议、报告链接），
  每条都能点进报告页。数据存在 `localStorage["st_hist"]`，只在本机可见、不上传；首次打开时会按 `st_rc` 里存过的测速者凭证
  回查旧报告补齐（最多 20 份），可一键清空。报告链接带语言与协议前缀，切换语言后历史里的时间/速度单位同步跟着偏好变。
- 报告含：下载、上传、Ping、抖动、丢包、连接数、消耗流量、运营商与 IP、节点服务商与地区、时间、四项体验评级、逐秒曲线
- **逐秒曲线**：下载 / 上传阶段每 250ms 采样、按秒聚合，随 `/api/report` 的 `timeline` 一起存库
  （服务端限制 120 个点、数值范围）；报告页在评分框上方把**下行与上行两段各 15 秒叠加到同一根 0–15s 轴上**，
  共用一根 Mbps 纵轴（无延迟曲线、无右侧 ms 轴），按住拖动出现十字线，提示框显示「第 N 秒 + 下载 + 上传」。
  老报告没有这个字段时显示「没有逐秒数据」。注意测速仍是「延迟 5 秒 → 下载 15 秒 → 上传 15 秒」顺序执行，
  曲线只是把两段按各自的秒数对齐叠加，方便直接对比
- 版式与 speedtest 结果页一致：左上 GO、右上下载/上传、中间 Ping 行与体验图标、左下连接信息、右下 0-10 推荐评分
- **消耗流量**：前端在下载 / 上传阶段按实际收到的字节统计（`downloadBytes` / `uploadBytes` 随 `/api/report` 上报），
  报告页显示「总量 · ↓ 下载 / ↑ 上传」，按 1024 进制换算成 B/KB/MB/GB
- **隐私**：报告是可以随手分享的链接，但**只有测速者本人**能看到自己的 IP：
  - 测速成功时 `/api/report` 会返回一个一次性凭证 `owner`，首页写进本浏览器 `localStorage["st_rc"]`（只保留最近 40 份）；
    打开报告页时通过请求头 `X-Result-Owner` 带上，服务端校验通过才下发 IP
  - 换设备 / 清了缓存时，若来源 IP 与记录里的测速者一致（`sameIP`），同样视为本人
  - 管理员登录后台后打开任意报告都能看到完整信息
  - 其他人打开时 `ip` 为空、`restricted=true`，页面显示「已隐藏（仅测速者可见）」，节点内部 ID 也不会下发
- **地址与协议一致**：页面可能是走 IPv6 打开却选了 IPv4 节点，服务端从连接上只会看到 IPv6 地址。
  测速首页会按所选协议另发一次 `/api/isp` 探测，把该协议下的访客地址随报告一起上报（`clientIp`），
  服务端校验后优先采用，保证「IP 协议」与下面的地址属于同一协议
- 记录落盘到 `data/results.jsonl`，保留最近 500 条，服务重启后分享链接依然有效

关闭「收集测速结果」后不再生成报告编号。

## 访客信息展示

前台左侧信息块第一行是 **ASN 服务商**（如「中国电信」，悬停或小标签显示 `AS4134`），第二行是本次测速使用的**公网 IP**；
右侧信息块第一行是**节点服务商**、第二行是**地区**，两者都在后台「测速节点」里设置。

IPv6 地址较长，按「前 4 段 / 后 4 段」折成两行显示：

```
2001:db8:4a2f:1100:
d923:7900:709a:8f54
```

实现见 `app.js` / `result.js` 的 `ipLines()` + `.cb-sub { white-space: pre-line }`。

**IP 跟随「协议」切换**：浏览器只会用它自己选定的协议跟我们通信——页面若是走 IPv6 打开的，
同源请求一律是 IPv6，服务端自然也只看得到 IPv6 地址。所以 `/api/info` 会把宿主机的公网地址
（`addrs.v4` / `addrs.v6`，见 `http_util.go` 的 `hostAddresses()`）一并返回，前台切到另一个协议时
用它在那个协议下再请求一次 `/api/isp`，拿到的就是访客在该协议下的真实地址：

- 探测成功：地址、运营商、ASN 都换成该协议的数据（`app.js` 的 `probeClient()`）；
- 探测不到（例如服务器没有该协议的公网地址）：显示「未获取到该协议的地址」，**不会**拿另一个协议的地址冒充；
- 同协议下若本机节点留空地址，测速改用探测到的该协议地址做基址，避免「选了 IPv4 但流量走 IPv6」。

**协议对应网址**：点 IPv4 / IPv6 时地址栏同步换成 `/ipv4`、`/ipv6`（在中文入口下是
`/zh-Hans/ipv4`），用 `history.replaceState` 改路径、不刷新页面。后端 `handleStatic` 把
`/ipv4`、`/ipv6`、`/zh-Hans/ipv4`、`/zh-Hans/ipv6` 都映射到首页，所以刷新或直接分享该链接
打开时，初始协议就是路径里写的那个（优先于上次选择）。浏览器前进/后退改路径也会跟着切协议。

运营商解析：服务端先查本地缓存，再异步请求 `ip-api.com`（取 `isp` 与 `as` 字段，可换成自建服务），失败时回退到 DNS 反解。若访客经代理/VPN 访问，显示的会是代理出口 IP。

> 如需完全不外联，可删除 `isp.go` 中 `queryIPAPI` 分支，仅保留 DNS 反解。

## 多语言（中文 / English）

前台与报告页支持中英切换，入口在顶栏「中文(简体) ▾ / English ▾」，选择记录在 `localStorage["st_lang"]`，
首次访问按浏览器语言自动判定。文案集中在 `web/i18n.js`，HTML 用 `data-i18n` / `data-i18n-aria` / `data-i18n-title`
标记，JS 用 `I18N.t("key", {n: 1})` 取值；新增语言只要往 `DICT` 里加一份词表。

运营商名同样跟随语言：后端把 ISP 归一成中英两个名字（`isp` / `ispEn`，见 `isp.go` 的 `splitISP`），
前端由 `I18N.ispName(zh, en)` 按当前语言取值，中文界面显示「中国电信」、英文界面显示 China Telecom；
早期没有 `ispEn` 的历史报告用 `i18n.js` 内置的「中文名 → 英文名」表兜底。
节点「服务商名称 / 地区名称」是后台自定义文本，两种语言共用同一份内容，需要英文时直接填英文即可。

地址栏也同样跟着语言走：中文是 `/zh-Hans/...`，切成 English 后立刻变成 `/en/...`（`i18n.js` 的 `syncPath()`
用 `history.replaceState` 改写，不刷新页面），刷新和分享出去的链接都带着对应语言；
`main.go` 的 `stripLang()` 让 `/zh-Hans`、`/en` 两种前缀都能正常路由，没有前缀的老链接会自动补上。

## 网络模式（重要）

容器使用 `network_mode: host` 直接监听宿主端口，不再做端口映射。这是为了拿到访客真实 IP：

- Docker 默认的 IPv4 网桥不处理 IPv6 端口发布，IPv6 访问会由 `docker-proxy` 用户态转发进容器，来源地址被替换成网桥网关（`172.x.0.1`）；
- 使用宿主网络后，容器看到的连接来源就是访客真实地址，IPv4 / IPv6 都正确。

对应地，`HOST_PORT` 既是宿主端口也是容器监听端口，修改后无需改 `ports`。若宿主端口被占用会启动失败，先停掉占用进程即可。

## 前台布局

按 speedtest.net 的结构排布（左侧运营商与 IP 右对齐贴住人像圆钮）：

```
[运营商 + ASN / IP]  (人像圆钮) (服务器圆钮)  [节点服务商 / 地区 / 更换]
                       协议：IPv4  IPv6
                       连接：多重  (⇄ 圆钮)  单一
```

这一行用三列网格（`minmax(0,1fr) auto minmax(0,1fr)`）：中间两枚圆钮始终落在 GO 圆环的正下方，
左侧文案贴左、右侧文案贴右，两侧再长也不会把图标挤偏。两块文案之间的两枚圆钮分别打开账号信息与节点列表；
「多重 / 单一」中间的圆钮点一下即切换。
垂直方向：工具栏（结果 / 设置）与 GO 圆环、信息行在同一个居中栈（`.stage` + `.stage-inner`
的 `margin: auto 0`）里，整组内容在剩余空间垂直居中、水平都对齐到圆环中心；仪表盘展开变高时
整组向上收拢，不会裁掉顶部。

点「设置」展开的是一块三段式面板（与 speedtest.net 的 SETTINGS 同构）：**全局设置**（时间 12/24 小时、
日期格式、距离单位、速度单位）、**当前连接**（运营商 / IP / 地区 / 经纬度）、**测速节点**（当前节点 + 更换）。
偏好存在 `localStorage["st_prefs2"]`，由 `web/prefs.js` 读写；报告页顶栏的「设置」是同一个面板（多一行「服务器」）。

- **默认值**与原版一致：12 小时制、`MM/DD/YYYY`、英里、Mbps。
- 改完**立刻生效**，不用刷新：面板底部有一行示例（`预览：11/18/2026 1:24:05 PM · 54.28 Mbps · 12.4 mi`），
  报告页的时间、下载/上传数字与单位、节点距离会同步重算（Kbps 会换算数值并改单位标签）。
- 存储键名带版本号（`st_prefs2`），老访客以前点过的 24 小时 / 公里会被新默认值取代。
- **距离**要显示在节点列表里，需要节点填了经纬度（后台「测速节点 → 纬度 / 经度」，可选）；
  没填时该行只显示地区，面板里的示例行仍会按所选单位换算。

展开时下方「信息行 / 协议 / 连接方式」让位，开始测速自动收起。

## IPv4 / IPv6 分开

后台每个节点可以分别填写 **IPv4 地址**（`url`）和 **IPv6 地址**（`url6`），前端在「协议」一行切换：

- 只有当至少一个节点填了 IPv6 地址（或本机节点两个地址都留空）时，「IPv4 / IPv6」选项才会出现；
  只有一个协议可用时这一行自动隐藏，老站点升级后界面不变。
- 选中某个协议后，节点列表、延迟排序、默认节点都只在该协议的节点里计算，选择记在 `localStorage["st_family"]`。
- 本机节点两个地址都留空表示「跟随当前访问地址」：测速请求打到页面同源地址，走哪种协议由浏览器决定
  （双栈网络下浏览器优先 IPv6）。需要强制某个协议时，把该协议的地址填上即可。
- 设置的默认节点也分 IPv4 / IPv6 两份（`defaultNode` / `defaultNode6`），没有对应协议的节点不会出现在下拉里。
- 上报结果会记录本次使用的协议，报告页在「连接」上方多一行「IP 协议」；旧报告没有这个字段时整行隐藏。

- 访客地址按协议分别探测（见上文），切换协议后信息块里的 IP / 运营商同步变化。

> 裸写 IPv6 地址（如 `2001:db8:4a2f:1100::1`）会自动补成 `http://[2001:db8:4a2f:1100::1]`，
> 误填进 IPv4 栏也会自动挪到 IPv6 栏。

## 单连接 / 多连接配色

与 speedtest.net 一致：多连接圆环为青蓝渐变（`#2DE5D1 → #1FA4E9`），切换到「单一」后圆环、仪表盘弧线、
选中文字都变为橙色（`#FFD684 → #FF886A`，文字 `#E56B4F`）。实现是给 `#dial` 加 `data-mode`，
由 `applyMode()` 改写 `#ringGrad` / `#gaugeGrad` 的 stop-color。

## 常用运维

```bash
docker compose logs -f          # 日志
docker compose restart          # 重启
docker compose up -d --build    # 改代码后重建
```

主控版数据只有 `data/config.json`（测速报告在 `data/results.jsonl`），备份 `data/` 即可。
节点版无任何数据文件，无需备份，配置全在 `.env.node`。

```bash
# 节点版运维
docker compose --env-file .env.node -f docker-compose.node.yml logs -f
docker compose --env-file .env.node -f docker-compose.node.yml restart
docker compose --env-file .env.node -f docker-compose.node.yml up -d --build   # 改配置后重建
```
