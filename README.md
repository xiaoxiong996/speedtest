# SpeedTest 自建测速站

仿 speedtest.net 的测速站点，**主控 + 测速节点** 两套角色共用同一个 Go 二进制、同一个镜像，用 `APP_MODE` 切换。前端零依赖，支持 Docker 部署，也支持直接跑二进制（非 root、systemd 用户服务），支持 `linux/amd64` 与 `linux/arm64`。

- **主控**：测速页面、后台管理、结果报告页、节点管理、多语言、深浅色、IPv4/IPv6 分离
- **节点**：只暴露 `ping / isp / download / upload` 四个接口，不落盘、无后台页面，适合放在不受信任的小鸡
- **节点进阶**：多证书按 SNI 选择、ACME DNS-01（Cloudflare 自动或手动 TXT）、DDNS 的 A/AAAA 分开维护

镜像由 GitHub Actions 自动构建，推 `main` 或 `v*` 标签即发布：

| 用途 | 镜像 |
|---|---|
| 主控 | `ghcr.io/xiaoxiong996/speedtest:latest` |
| 节点 | `ghcr.io/xiaoxiong996/speedtest-node:latest` |

## 主控安装

```bash
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/install.sh \
  | sudo bash -s -- --image ghcr.io/xiaoxiong996/speedtest:1.5.2 --url https://speed.example.com
```

不加任何参数则进入交互式安装（依次询问域名、端口、后台路径、密码）：

```bash
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/install.sh -o install.sh
sudo bash install.sh
```

| 默认项 | 值 |
|---|---|
| 安装目录 | `/opt/speedtest` |
| 监听端口 | `127.0.0.1:3333`（给反代用，不直接对外） |
| 后台入口 | `/st-panel` |
| 账号 / 密码 | `admin` / `Speedtest@2026`（**首次登录强制改密**） |

常用参数：`--port` `--bind` `--url` `--admin-user` `--admin-pass` `--admin-path` `--site-name`，完整列表见 `bash install.sh --help`。

## 节点流量限额与在线人数

后台「测速节点 → 编辑」可以给每个节点设每月流量限额：

| 配置 | 说明 |
|---|---|
| 每月流量限额（GB） | 按测速报告里浏览器统计的上下行字节累计；达到限额后主控自动停用该节点，前台立刻选不到它 |
| 每月重置日（1-28） | 到日子把已用流量清零；只允许到 28 号，避免 2 月漏重置 |
| 重置时自动启用 | 清零后顺带把因超限停用的节点恢复启用 |

节点列表里随时可以手动「清零」当月流量并重新启用。流量完全由主控统计，节点侧不需要任何配置；即使关闭「收集测速结果」，限额计数照常工作。

测速中途点「停止」、直接关页面或断网时，已经跑掉的上下行字节会单独补报给主控（只结流量、不生成报告），
不会出现「测一半就退出」白用节点带宽的情况。正常跑完的测速仍然只报一次，不会重复计入。

首页会在连接信息与 IPv4/IPv6 切换之间显示「N 人正在测速 · 使用 XX 节点」。人数由测速期间的匿名心跳统计（每 5 秒一次，20 秒无心跳即视为结束），主控不记录访客 IP。

## 节点安装

```bash
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/install-node.sh \
  | sudo bash -s -- --image ghcr.io/xiaoxiong996/speedtest-node:1.5.2 --port 8443 \
      --domain node.example.com --host node.example.com --origin https://speed.example.com
```

| 默认项 | 值 |
|---|---|
| 安装目录 | `/opt/speedtest-node` |
| 端口 | `8443` |
| 面板入口 | `/st-node-panel` |
| 账号 / 密码 | `admin` / `Node@2026` |

首次登录需要**一次性安装码**，打印在容器日志里：`docker logs speedtest-node | grep 安装码`；登录一次后自动解锁。也可以用 `--panel-user` `--panel-pass` `--panel-path` 直接指定，或用 `NODE_SETUP_CODE=xxx` 自定安装码。

常用参数：`--prefix` `--cidr` `--host` `--origin` `--cf-token` `--acme-email`，完整列表见 `bash install-node.sh --help`。

## 更新

```bash
# 主控
cd /opt/speedtest && sed -i 's|^IMAGE=.*|IMAGE=ghcr.io/xiaoxiong996/speedtest:1.5.2|' .env
docker compose pull && docker compose up -d

# 节点（certs/ 不动，证书继续用）
cd /opt/speedtest-node && sed -i 's|^IMAGE=.*|IMAGE=ghcr.io/xiaoxiong996/speedtest-node:1.5.2|' .env
docker compose pull && docker compose up -d --force-recreate
```

## 卸载

```bash
# 主控（默认保留 data/，加 --purge-data 才删配置与报告历史）
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/uninstall.sh \
  | sudo bash -s -- --yes

# 节点
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/uninstall-node.sh \
  | sudo bash -s -- --yes
```

## 不用 Docker / 不用 root

Release 里附了静态编译的单文件二进制（`speedtest-linux-amd64` / `speedtest-linux-arm64`），网页与控制台都内嵌在里面，拷过去就能跑。安装脚本会把它装成 **systemd 用户服务**（`systemctl --user`），全程不需要 root。

```bash
# 主控：默认监听 127.0.0.1:3333，前面自己接 HTTPS 反代
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/install-local.sh \
  | bash -s -- --mode controller --port 3333

# 测速节点：监听 8443，自动签发 Let's Encrypt 证书
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/install-local.sh \
  | bash -s -- --mode node --port 8443 --domain node.example.com --origin https://speed.example.com
```

| 项 | 主控 | 节点 |
|---|---|---|
| 目录 | `~/.local/share/speedtest` | `~/.local/share/speedtest-node` |
| 配置 | `~/.local/share/speedtest/.env`（600） | 同左，证书在 `certs/` |
| 服务 | `speedtest.service` | `speedtest-node.service` |
| 日志 | `journalctl --user -u speedtest -f` | `journalctl --user -u speedtest-node -f` |

几点区别要注意：

- 端口必须 **≥1024**（非 root 绑不了 80/443）。要标准 https 就在前面放 Caddy/Nginx。
- **注销后服务会停**，重启也不会自启，除非执行一次 `sudo loginctl enable-linger $USER`（这条需要 sudo，但只做一次，之后服务就在后台常驻了）。脚本装完会把这条命令打出来。
- 主控首次安装的后台密码会在安装结束时打印（也可以用 `--admin-pass` 指定）。
- 更新二进制：重跑一次安装脚本即可，`.env` 和数据目录都保留。只想换二进制也可以 `--binary ./speedtest-linux-amd64`。

卸载：

```bash
# 主控
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/uninstall-local.sh \
  | bash -s -- --mode controller

# 节点（--purge 连数据与证书一起删）
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/uninstall-local.sh \
  | bash -s -- --mode node --purge
```

## 反向代理

主控默认只监听 `127.0.0.1:3333`，用 Nginx / Caddy 把你自己的 HTTPS 域名转发过去即可，程序会自动识别 `Host`，换域名不用改配置。

```nginx
location / {
    proxy_pass http://127.0.0.1:3333;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    client_max_body_size 64m;
}
```

## 自行构建

```bash
docker build -t speedtest:dev .
IMAGE=speedtest:dev docker compose up -d
```

## 许可

[MIT](LICENSE)
