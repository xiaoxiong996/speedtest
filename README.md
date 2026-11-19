# SpeedTest 自建测速站

仿 speedtest.net 的测速站点，**主控 + 测速节点** 两套角色共用同一个 Go 二进制、同一个镜像，用 `APP_MODE` 切换。前端零依赖，Docker 部署，支持 `linux/amd64` 与 `linux/arm64`。

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
  | sudo bash -s -- --image ghcr.io/xiaoxiong996/speedtest:1.4.4 --url https://speed.example.com
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

## 节点安装

```bash
curl -fsSL https://raw.githubusercontent.com/xiaoxiong996/speedtest/main/deploy/install-node.sh \
  | sudo bash -s -- --image ghcr.io/xiaoxiong996/speedtest-node:1.4.4 --port 8443 \
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
cd /opt/speedtest && sed -i 's|^IMAGE=.*|IMAGE=ghcr.io/xiaoxiong996/speedtest:1.4.4|' .env
docker compose pull && docker compose up -d

# 节点（certs/ 不动，证书继续用）
cd /opt/speedtest-node && sed -i 's|^IMAGE=.*|IMAGE=ghcr.io/xiaoxiong996/speedtest-node:1.4.4|' .env
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
