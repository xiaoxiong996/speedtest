#!/usr/bin/env bash
# ============================================================================
#  SpeedTest 纯测速节点 —— 一键安装脚本
#
#  用法（推荐先把 IMAGE 换成你自己的 GitHub 镜像地址）：
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/install-node.sh \
#      | sudo bash -s -- --image ghcr.io/<用户名>/<仓库>:latest --port 8443
#
#  常用参数：
#    --image  <镜像>      必填（除非本机已有 speedtest-node:latest）
#    --port   <端口>      默认 8443；公网直连想用标准 https 就填 443
#    --prefix <路径前缀>  例如 /n1，端点变成 /n1/api/ping
#    --cidr   <IP 段>     允许测速的访客 IP，逗号分隔，留空不限制
#    --host   <域名>      允许的 Host，逗号分隔，支持 *.example.com
#    --origin <来源>      允许跨源的主控站，例如 https://speed.example.com
#    --domain <域名>      必填，证书域名（Let's Encrypt DNS-01 自动签发），需已解析到本机公网 IP
#    --email  <邮箱>      到期提醒邮箱（可选）
#    --staging            用 Let's Encrypt 测试环境（仅供调试，证书不被信任）
#    --panel-path <路径>  面板隐藏路径，例如 panel-9f3a（不给则不启用面板）
#    --panel-user <账号>  面板账号
#    --panel-pass <密码>  面板密码
#    --cf-token <令牌>    Cloudflare API Token（给了就全自动签发+续期）
#    --dir    <目录>      安装目录，默认 /opt/speedtest-node
#    --no-start           只写文件不启动
#
#  卸载：同目录下的 uninstall-node.sh，或
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/uninstall-node.sh | sudo bash -s -- --yes
# ============================================================================
set -euo pipefail

IMAGE=""
PORT="8443"
PREFIX=""
CIDRS=""
HOSTS=""
ORIGINS=""
ACME_DOMAINS=""
ACME_EMAIL=""
ACME_STAGING="0"
PANEL_PATH=""
PANEL_USER=""
PANEL_PASS=""
CF_TOKEN=""
DIR="/opt/speedtest-node"
START="1"

log()  { printf '\033[36m[node]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[node]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[node]\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --image)      IMAGE="${2:-}"; shift 2 ;;
    --port)       PORT="${2:-}"; shift 2 ;;
    --prefix)     PREFIX="${2:-}"; shift 2 ;;
    --cidr)       CIDRS="${2:-}"; shift 2 ;;
    --host)       HOSTS="${2:-}"; shift 2 ;;
    --origin)     ORIGINS="${2:-}"; shift 2 ;;
    --domain)     ACME_DOMAINS="${2:-}"; shift 2 ;;
    --panel-path) PANEL_PATH="${2:-}"; shift 2 ;;
    --panel-user) PANEL_USER="${2:-}"; shift 2 ;;
    --panel-pass) PANEL_PASS="${2:-}"; shift 2 ;;
    --cf-token)   CF_TOKEN="${2:-}"; shift 2 ;;
    --acme-domain) ACME_DOMAINS="${2:-}"; shift 2 ;;   # 兼容旧写法
    --email)      ACME_EMAIL="${2:-}"; shift 2 ;;
    --acme-email) ACME_EMAIL="${2:-}"; shift 2 ;;      # 兼容旧写法
    --staging|--acme-staging) ACME_STAGING="1"; shift ;;
    --dir)        DIR="${2:-}"; shift 2 ;;
    --no-start)   START="0"; shift ;;
    -h|--help)
      cat <<'USAGE'
用法：install-node.sh --image <镜像> [选项]

  --image  <镜像>       必填，例如 ghcr.io/me/speedtest:latest
  --port   <端口>       默认 8443；公网直连想用标准 https 就填 443
  --prefix <路径前缀>   例如 /n1，端点变成 /n1/api/ping
  --cidr   <IP 段>      允许测速的访客 IP，逗号分隔，留空不限制
  --host   <域名>       允许的 Host，逗号分隔，支持 *.example.com
  --origin <来源>       允许跨源的主控站，例如 https://speed.example.com
  --domain <域名>       必填，证书域名（Let's Encrypt DNS-01），可逗号分隔多个
  --email  <邮箱>       到期提醒邮箱（可选）
  --staging             用 Let's Encrypt 测试环境（仅供调试）
  --panel-path <路径>   面板隐藏路径，例如 panel-9f3a（不给则不启用）
  --panel-user <账号>   面板账号
  --panel-pass <密码>   面板密码
  --cf-token <令牌>     Cloudflare Token，给了就全自动续期
  --dir    <目录>       安装目录，默认 /opt/speedtest-node
  --no-start            只写文件不启动
  -h, --help            显示本帮助
USAGE
      exit 0 ;;
    *)            die "未知参数：$1（用 --help 看用法）" ;;
  esac
done

# ---------- 运行环境检查 ----------
[ "$(id -u)" = "0" ] || die "请用 root 运行（或前面加 sudo）"
command -v docker >/dev/null 2>&1 || die "未安装 Docker，请先装：https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "缺少 docker compose 插件（v2），请先安装"

case "$PORT" in
  ''|*[!0-9]*) die "--port 必须是数字" ;;
esac
[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die "--port 超出范围"

if [ -n "$PREFIX" ]; then
  case "$PREFIX" in
    /*) ;;
    *) PREFIX="/$PREFIX" ;;
  esac
  case "$PREFIX" in
    *[!A-Za-z0-9/_-]*) die "--prefix 只允许字母、数字与 - _ /" ;;
  esac
fi

# 节点只有一种 HTTPS 方式：Let's Encrypt。域名是必填项。
[ -n "$ACME_DOMAINS" ] || die "请用 --domain 指定证书域名（需已解析到本机公网 IP，DNS 用灰云直连）"
case "$ACME_DOMAINS" in
  *[A-Z]*) warn "域名含大写字母，已自动转小写（证书只认小写）" ;;
esac
ACME_DOMAINS="$(printf '%s' "$ACME_DOMAINS" | tr 'A-Z' 'a-z')"
# 绑 1~1023 特权端口（443 / 80）：容器内是非 root。
# 实测只加 NET_BIND_SERVICE 在部分 docker 版本下拿不到有效集（CapPrm=0），
# 最稳的做法是把内核的"非特权端口起始值"放到 0 —— 本机自用完全够，且不影响容器隔离。
NEED_LOW_PORT=0
[ "$PORT" -lt 1024 ] && NEED_LOW_PORT=1
if [ "$NEED_LOW_PORT" = "1" ]; then
  if [ "$(cat /proc/sys/net/ipv4/ip_unprivileged_port_start 2>/dev/null || echo 1024)" != "0" ]; then
    log "需要绑定 80/443：放开非特权端口限制（/etc/sysctl.d/99-speedtest-node.conf）"
    echo 'net.ipv4.ip_unprivileged_port_start=0' > /etc/sysctl.d/99-speedtest-node.conf
    sysctl --system >/dev/null 2>&1 || sysctl -w net.ipv4.ip_unprivileged_port_start=0 >/dev/null 2>&1 || true
  fi
  [ "$(cat /proc/sys/net/ipv4/ip_unprivileged_port_start 2>/dev/null || echo 1024)" = "0" ] \
    || warn "内核仍限制非特权端口，80/443 可能绑定失败；请手动执行：sysctl -w net.ipv4.ip_unprivileged_port_start=0"
fi

if [ -z "$IMAGE" ]; then
  if docker image inspect speedtest-node:latest >/dev/null 2>&1; then
    IMAGE="speedtest-node:latest"
    warn "未指定 --image，使用本机已有的 speedtest-node:latest"
  else
    die "请用 --image 指定镜像，例如 ghcr.io/<用户名>/<仓库>:latest"
  fi
fi

# ---------- 目录与证书 ----------
umask 077
mkdir -p "$DIR" "$DIR/certs"
# 容器内是 uid 10001 的 app 用户，auto 模式要在 certs/ 里生成根证书与节点证书
chown -R 10001:10001 "$DIR/certs" 2>/dev/null || warn "chown certs 失败，auto 模式可能无法写证书"
chmod 700 "$DIR/certs"

# ---------- 写 .env（只有 root 可读）----------
cat > "$DIR/.env" <<EOF
IMAGE=$IMAGE
HTTP_HOST=0.0.0.0
NODE_PORT=$PORT
TZ=Asia/Shanghai
NODE_ALLOW_CIDRS=$CIDRS
NODE_ALLOW_HOSTS=$HOSTS
NODE_ALLOW_ORIGINS=$ORIGINS
NODE_PATH_PREFIX=$PREFIX
NODE_ACME_DOMAINS=$ACME_DOMAINS
NODE_ACME_EMAIL=$ACME_EMAIL
NODE_ACME_DIR=/certs/acme
NODE_ACME_STAGING=$ACME_STAGING
NODE_CERT_DIR=/certs
NODE_CERT_RENEW_DAYS=30
CF_DNS_API_TOKEN=$CF_TOKEN
NODE_HTTP_BOOTSTRAP_MINUTES=10
NODE_HTTP_PORT=
NODE_PANEL_PATH=$PANEL_PATH
NODE_PANEL_USER=$PANEL_USER
NODE_PANEL_PASS=$PANEL_PASS
TRUSTED_PROXIES=
TRUSTED_IP_HEADERS=CF-Connecting-IP,True-Client-IP,Ali-Cdn-Real-Ip,Ali-Real-Ip,EO-Connecting-IP,X-Forwarded-For,X-Real-IP
LOG_CLIENT_IP=0
EOF
chmod 600 "$DIR/.env"

# ---------- 写 compose（与仓库里的 docker-compose.node.yml 保持一致）----------
cat > "$DIR/docker-compose.yml" <<'YAML'
services:
  speedtest-node:
    image: "${IMAGE}"
    container_name: speedtest-node
    restart: unless-stopped
    network_mode: host
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    # 非 root 绑 443/80 需要它；只给"绑定特权端口"这一项，其余 capability 仍然全丢
    cap_add:
      - NET_BIND_SERVICE
    pids_limit: 256
    tmpfs:
      - /tmp:size=16m
      - /data:size=1m
    environment:
      APP_MODE: "node"
      HTTP_HOST: "${HTTP_HOST:-0.0.0.0}"
      HTTP_PORT: "${NODE_PORT:-8443}"
      TZ: "${TZ:-Asia/Shanghai}"
      NODE_ALLOW_CIDRS: "${NODE_ALLOW_CIDRS:-}"
      NODE_ALLOW_HOSTS: "${NODE_ALLOW_HOSTS:-}"
      NODE_ALLOW_ORIGINS: "${NODE_ALLOW_ORIGINS:-}"
      NODE_PATH_PREFIX: "${NODE_PATH_PREFIX:-}"
      NODE_ACME_DOMAINS: "${NODE_ACME_DOMAINS:-}"
      NODE_ACME_EMAIL: "${NODE_ACME_EMAIL:-}"
      NODE_ACME_DIR: "${NODE_ACME_DIR:-/certs/acme}"
      NODE_ACME_STAGING: "${NODE_ACME_STAGING:-0}"
      NODE_CERT_DIR: "${NODE_CERT_DIR:-/certs}"
      NODE_CERT_RENEW_DAYS: "${NODE_CERT_RENEW_DAYS:-30}"
      CF_DNS_API_TOKEN: "${CF_DNS_API_TOKEN:-}"
      NODE_HTTP_BOOTSTRAP_MINUTES: "${NODE_HTTP_BOOTSTRAP_MINUTES:-10}"
      NODE_HTTP_PORT: "${NODE_HTTP_PORT:-}"
      NODE_PANEL_PATH: "${NODE_PANEL_PATH:-}"
      NODE_PANEL_USER: "${NODE_PANEL_USER:-}"
      NODE_PANEL_PASS: "${NODE_PANEL_PASS:-}"
    volumes:
      - ./certs:/certs:rw
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
    ulimits:
      nofile:
        soft: 65535
        hard: 65535
    healthcheck:
      test: ["CMD-SHELL", "nc -z 127.0.0.1 ${NODE_PORT:-8443} || exit 1"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
YAML
chmod 644 "$DIR/docker-compose.yml"

log "配置已写入 $DIR（.env 权限 600）"

if [ "$START" != "1" ]; then
  log "已跳过启动。手动启动：cd $DIR && docker compose up -d"
  exit 0
fi

# ---------- 启动 ----------
cd "$DIR"
log "拉取镜像 $IMAGE …"
if ! docker compose pull --quiet 2>/dev/null; then
  if docker image inspect "$IMAGE" >/dev/null 2>&1; then
    warn "拉取失败，改用本机已有的同名镜像"
  else
    die "镜像拉取失败。若 GHCR 是私有仓库，请先登录：
  echo <你的PAT> | docker login ghcr.io -u <用户名> --password-stdin
或到 GitHub → Your packages 把该包可见性改为 Public。"
  fi
fi

log "启动容器 …"
docker compose up -d

# ---------- 等健康并输出结果 ----------
for i in $(seq 1 20); do
  state="$(docker inspect -f '{{.State.Health.Status}}' speedtest-node 2>/dev/null || echo unknown)"
  [ "$state" = "healthy" ] && break
  sleep 1
done

state="$(docker inspect -f '{{.State.Health.Status}}' speedtest-node 2>/dev/null || echo unknown)"
if [ "$state" = "healthy" ]; then
  log "节点已就绪（healthy）"
else
  warn "容器状态：$state，查看日志：docker logs --tail 30 speedtest-node"
fi

SCHEME="https"
PREFIX_SHOWN="${PREFIX:-}"
echo
echo "  节点地址（填到主控后台的「节点」里）："
echo "    ${SCHEME}://<本机域名或IP>:${PORT}${PREFIX_SHOWN}"
echo "  端点示例："
echo "    ${SCHEME}://<本机域名或IP>:${PORT}${PREFIX_SHOWN}/api/ping"
echo
if [ -z "$ORIGINS" ]; then
  warn "没有设置 --origin，任何网站都能跨源调用这个节点。建议改成主控域名。"
fi
if [ -z "$HOSTS" ] && [ -z "$CIDRS" ]; then
  warn "没有设置 --host / --cidr，任何人知道地址都能拿来测速。建议至少设一个。"
fi
echo "  证书（Let's Encrypt 自动签发 / 续期）："
echo "    域名：$ACME_DOMAINS"
echo "    证书缓存：$DIR/certs/acme"

  echo "    ★ 必须满足：① 域名 A/AAAA 记录已指向本机公网 IP，且 DNS 是灰云直连；"
  echo "                ② 本机 $PORT 端口对公网放通（TLS-ALPN-01 校验走这个端口）"
  echo "    证书是首次 https 握手时现签的（约 5~20 秒），之后自动续期，无需重启。"
echo "    查看状态：curl -s https://$ACME_DOMAINS:${PORT}${PREFIX_SHOWN}/api/tls"
if [ -n "$PANEL_PATH" ]; then
  echo "  面板：https://$ACME_DOMAINS:${PORT}/$PANEL_PATH"
  echo "        首次证书还没签好时，用 http://<公网IP>:$((PORT+1))/$PANEL_PATH 打开（前 10 分钟有效）"
fi
if [ -n "$CF_TOKEN" ]; then
  echo "  已配置 Cloudflare Token：签发与续期全自动"
else
  echo "  未配置 Cloudflare Token：签发/续期时需在面板手动添加 TXT 记录"
fi
if [ "$ACME_STAGING" = "1" ]; then
  warn "当前用的是 Let's Encrypt 测试环境（NODE_ACME_STAGING=1），浏览器不信任该证书；正式使用请改成 0 并删掉 certs/acme 后重启。"
fi
echo "  改配置：编辑 $DIR/.env 后执行 cd $DIR && docker compose up -d"
echo "  看日志：docker logs -f speedtest-node"
echo "  卸载： curl -fsSL <仓库>/main/deploy/uninstall-node.sh | sudo bash -s -- --yes"
