#!/usr/bin/env bash
# ============================================================================
#  SpeedTest 主控版（网页 + 后台 + 报告）—— 一键安装脚本
#
#  方式一：远程一键装（推荐，IMAGE 换成你自己的仓库）
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/install.sh \
#      | sudo bash -s -- --image ghcr.io/<用户名>/<仓库>:latest
#
#  方式二：在源码目录里直接跑，自动本地构建
#    sudo ./deploy/install.sh --dir /app/speed
#
#  常用参数：
#    --image   <镜像>     例如 ghcr.io/用户名/仓库:1.4.0；不给则在源码目录本地构建
#    --dir     <目录>     安装目录，默认 /opt/speedtest
#    --port    <端口>     宿主机发布端口（反代的目标），默认 3333
#    --bind    <地址>     端口绑定地址，默认 127.0.0.1（只给反代用，安全）
#    --url     <站点地址> 例如 https://speed.example.com，用于生成分享/报告链接
#    --admin-user <账号>  默认 admin
#    --admin-pass <密码>  默认 Speedtest@2026（首次登录会强制改密）
#    --admin-path <路径>  后台入口，默认 /st-panel
#    --site-name <名称>   默认 SpeedTest
#    --subnet  <网段>     桥接网段，默认 172.31.240.0/24
#    --gateway <网关>     桥接网关，默认 172.31.240.1
#    --yes                完全非交互（全部用默认值/传入值）
#    --no-start           只写文件，不启动容器
#
#  卸载：同目录 uninstall.sh
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/uninstall.sh | sudo bash -s -- --yes
# ============================================================================
set -euo pipefail

IMAGE=""
DIR="/opt/speedtest"
HOST_PORT="3333"
BIND="127.0.0.1"
PUBLIC_URL=""
ADMIN_USER="admin"
ADMIN_PASS=""
ADMIN_PATH=""
SITE_NAME=""
SUBNET=""
GATEWAY=""
START="1"
YES="0"

log()  { printf '\033[36m[speed]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m[speed]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[speed]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[speed]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
用法：install.sh [选项]
（参数同时支持环境变量方式：curl ... | sudo bash -s -- --image ...）

  --image   <镜像>     例如 ghcr.io/用户名/仓库:1.4.0；不给则在源码目录本地构建
  --dir     <目录>     安装目录，默认 /opt/speedtest
  --port    <端口>     宿主机发布端口（反代目标），默认 3333
  --bind    <地址>     端口绑定地址，默认 127.0.0.1
  --url     <站点地址> 例如 https://speed.example.com
  --admin-user <账号>  默认 admin
  --admin-pass <密码>  默认 Speedtest@2026（首次登录强制改密）
  --admin-path <路径>  后台入口，默认 /st-panel
  --site-name <名称>   默认 SpeedTest
  --subnet  <网段>     桥接网段，默认 172.31.240.0/24
  --gateway <网关>     桥接网关，默认 172.31.240.1
  --yes, -y            完全非交互
  --no-start           只写文件，不启动容器
  -h, --help           显示本帮助
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --image)      IMAGE="${2:-}"; shift 2 ;;
    --dir)        DIR="${2:-}"; shift 2 ;;
    --port)       HOST_PORT="${2:-}"; shift 2 ;;
    --bind)       BIND="${2:-}"; shift 2 ;;
    --url)        PUBLIC_URL="${2:-}"; shift 2 ;;
    --admin-user) ADMIN_USER="${2:-}"; shift 2 ;;
    --admin-pass) ADMIN_PASS="${2:-}"; shift 2 ;;
    --admin-path) ADMIN_PATH="${2:-}"; shift 2 ;;
    --site-name)  SITE_NAME="${2:-}"; shift 2 ;;
    --subnet)     SUBNET="${2:-}"; shift 2 ;;
    --gateway)    GATEWAY="${2:-}"; shift 2 ;;
    --yes|-y)     YES="1"; shift ;;
    --no-start)   START="0"; shift ;;
    -h|--help)    usage; exit 0 ;;
    *)            die "未知参数：$1（--help 看用法）" ;;
  esac
done

# ---------------------------------------------------------------- 环境检查
[ "$(id -u)" = "0" ] || die "请用 root 运行（或前面加 sudo）"
command -v docker >/dev/null 2>&1 || die "未检测到 docker，请先安装 Docker Engine"
docker compose version >/dev/null 2>&1 || die "未检测到 docker compose 插件（v2），请先安装 docker-compose-plugin"

# 交互读取：curl | bash 时 stdin 是脚本本身，必须走 /dev/tty
ask() {
  local prompt="$1" def="$2" ans=""
  if [ "$YES" = "1" ] || [ ! -r /dev/tty ]; then
    printf '%s' "$def"
    return 0
  fi
  printf '%s' "$prompt" >/dev/tty
  read -r ans </dev/tty || ans=""
  [ -n "$ans" ] && printf '%s' "$ans" || printf '%s' "$def"
}

# 密码/路径里的字符要能安全写进 .env（单引号包裹，不允许再出现单引号）
check_safe() {
  case "$1" in
    *"'"*) die "内容不能包含单引号：$1" ;;
  esac
}

# ---------------------------------------------------------------- 分支：本地构建 vs 拉镜像
LOCAL_BUILD="0"
if [ -z "$IMAGE" ]; then
  if [ -f "$PWD/go.mod" ] && [ -f "$PWD/Dockerfile" ]; then
    LOCAL_BUILD="1"
    IMAGE="speedtest-site:latest"
    DIR="$PWD"
    log "检测到源码目录，使用本地构建（$IMAGE）"
  else
    die "缺少 --image。示例：
  curl -fsSL .../install.sh | sudo bash -s -- --image ghcr.io/你的用户名/你的仓库:latest"
  fi
fi

# ---------------------------------------------------------------- 交互补全
if [ "$YES" != "1" ]; then
  [ -n "$PUBLIC_URL" ] || PUBLIC_URL="$(ask '站点对外域名（如 https://speed.example.com，回车跳过）: ' '')"
  [ -n "$ADMIN_PATH" ] || ADMIN_PATH="$(ask '后台入口路径（回车用默认 /st-panel）: ' '')"
  if [ -z "$ADMIN_PASS" ]; then
    local_pass="$(ask '后台密码（回车用默认 Speedtest@2026，首次登录强制改密）: ' '')"
    ADMIN_PASS="$local_pass"
  fi
fi

[ -n "$ADMIN_PASS" ] || ADMIN_PASS="Speedtest@2026"
[ -n "$ADMIN_PATH" ] || ADMIN_PATH="/st-panel"

case "$ADMIN_PATH" in
  /*) : ;;
  *) die "后台路径必须以 / 开头：$ADMIN_PATH" ;;
esac

if [ ${#ADMIN_PASS} -lt 8 ]; then
  die "密码至少 8 位"
fi
check_safe "$ADMIN_PASS"
check_safe "$ADMIN_PATH"
check_safe "$SITE_NAME"
check_safe "$PUBLIC_URL"

# ---------------------------------------------------------------- 网络参数
if [ -n "$SUBNET" ] || [ -n "$GATEWAY" ]; then
  [ -n "$SUBNET" ]  || die "--subnet 与 --gateway 需要一起给"
  [ -n "$GATEWAY" ] || die "--subnet 与 --gateway 需要一起给"
fi
SUBNET="${SUBNET:-172.31.240.0/24}"
GATEWAY="${GATEWAY:-172.31.240.1}"
TRUSTED="127.0.0.1,::1,${GATEWAY}"

# ---------------------------------------------------------------- 准备目录
mkdir -p "$DIR"
cd "$DIR"

if [ "$LOCAL_BUILD" != "1" ]; then
  # ------------------------------------------------------------ 远程镜像：写 compose + .env
  cat > docker-compose.yml <<'YAML'
# 由 deploy/install.sh 生成。改完 docker compose up -d 生效。
services:
  speedtest:
    image: "${IMAGE}"
    container_name: speedtest
    restart: unless-stopped
    ports:
      - "${BIND_ADDR:-127.0.0.1}:${HOST_PORT:-3333}:${CONTAINER_PORT:-3333}"
    networks:
      - default
    environment:
      APP_MODE: "controller"
      DATA_DIR: /data
      HTTP_HOST: "0.0.0.0"
      HTTP_PORT: "${CONTAINER_PORT:-3333}"
      TRUSTED_PROXIES: "${TRUSTED_PROXIES:-127.0.0.1,::1,172.31.240.1}"
      TRUSTED_IP_HEADERS: "${TRUSTED_IP_HEADERS:-CF-Connecting-IP,True-Client-IP,Ali-Cdn-Real-Ip,Ali-Real-Ip,EO-Connecting-IP,X-Forwarded-For,X-Real-IP}"
      PUBLIC_BASE_URL: "${PUBLIC_BASE_URL:-}"
      ADMIN_COOKIE_DOMAIN: "${ADMIN_COOKIE_DOMAIN:-}"
      ADMIN_USER: "${ADMIN_USER:-admin}"
      ADMIN_PASSWORD: "${ADMIN_PASSWORD:-}"
      ADMIN_PATH: "${ADMIN_PATH:-}"
      SITE_NAME: "${SITE_NAME:-SpeedTest}"
      SITE_TITLE_SUFFIX: "${SITE_TITLE_SUFFIX:-}"
      LOG_CLIENT_IP: "${LOG_CLIENT_IP:-0}"
      TZ: "${TZ:-Asia/Shanghai}"
    volumes:
      - ./data:/data
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
      test: ["CMD-SHELL", "wget -q -O- http://127.0.0.1:${CONTAINER_PORT:-3333}/healthz >/dev/null 2>&1 || exit 1"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
networks:
  default:
    driver: bridge
    ipam:
      config:
        - subnet: "${DOCKER_SUBNET:-172.31.240.0/24}"
          gateway: "${DOCKER_GATEWAY:-172.31.240.1}"
YAML
  chmod 644 docker-compose.yml
fi

# .env（600，含密码）。已存在则先备份，避免覆盖掉已经改过的配置
if [ -f .env ]; then
  cp -a .env ".env.bak.$(date +%Y%m%d%H%M%S)"
  log "已备份原 .env"
fi

cat > .env <<EOF
# 由 deploy/install.sh 生成于 $(date '+%Y-%m-%d %H:%M:%S')
IMAGE=${IMAGE}
BIND_ADDR=${BIND}
HOST_PORT=${HOST_PORT}
CONTAINER_PORT=3333
PUBLIC_BASE_URL=${PUBLIC_URL}
ADMIN_COOKIE_DOMAIN=
ADMIN_USER=${ADMIN_USER}
ADMIN_PASSWORD='${ADMIN_PASS}'
ADMIN_PATH='${ADMIN_PATH}'
SITE_NAME='${SITE_NAME}'
SITE_TITLE_SUFFIX=
LOG_CLIENT_IP=0
TZ=Asia/Shanghai
DOCKER_SUBNET=${SUBNET}
DOCKER_GATEWAY=${GATEWAY}
TRUSTED_PROXIES=${TRUSTED}
TRUSTED_IP_HEADERS=CF-Connecting-IP,True-Client-IP,Ali-Cdn-Real-Ip,Ali-Real-Ip,EO-Connecting-IP,X-Forwarded-For,X-Real-IP
EOF
chmod 600 .env
mkdir -p data
chmod 700 data 2>/dev/null || true
ok "配置已写入 $DIR/.env"

if [ "$START" != "1" ]; then
  warn "按 --no-start 要求未启动。手动启动：cd $DIR && docker compose up -d"
  exit 0
fi

# ---------------------------------------------------------------- 启动
log "启动容器 …"
if [ "$LOCAL_BUILD" = "1" ]; then
  docker compose up -d --build
else
  docker compose pull -q || warn "拉取镜像失败，若为私有仓库请先 docker login ghcr.io"
  docker compose up -d
fi

# ---------------------------------------------------------------- 健康检查（最多 30 秒）
log "等待健康检查 …"
status=""
for _ in $(seq 1 15); do
  status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' speedtest 2>/dev/null || true)"
  [ "$status" = "healthy" ] && break
  sleep 2
done

case "$status" in
  healthy) ok "容器状态：healthy" ;;
  starting) warn "容器还在启动中，稍后 docker compose logs -f 查看" ;;
  *) warn "容器状态：${status:-未知}，请查看日志：cd $DIR && docker compose logs --tail=80" ;;
esac

# ---------------------------------------------------------------- 输出摘要
HOSTSHOW="$([ "$BIND" = "0.0.0.0" ] && echo "<服务器IP>" || echo "127.0.0.1")"
if [ -n "$PUBLIC_URL" ]; then
  SITE_SHOW="${PUBLIC_URL%/}"
else
  SITE_SHOW="http://${HOSTSHOW}:${HOST_PORT}"
fi

cat <<EOF

────────────────────────────────────────────────────────────
 主控已安装：$DIR

 站点地址     ${SITE_SHOW}
 后台地址     ${SITE_SHOW}${ADMIN_PATH}
 后台账号     ${ADMIN_USER}
 后台密码     ${ADMIN_PASS}
 反代目标     http://127.0.0.1:${HOST_PORT}

 ⚠ 首次登录会强制改密；改完密码以 data/config.json 里的哈希为准，
   .env 里的 ADMIN_PASSWORD 只在首次初始化时用一次。

 常用命令
   cd $DIR && docker compose ps
   cd $DIR && docker compose logs -f --tail=100
   cd $DIR && docker compose restart
   cd $DIR && docker compose pull && docker compose up -d      # 升级

 反向代理（Nginx 示例）
   location / {
       proxy_pass http://127.0.0.1:${HOST_PORT};
       proxy_set_header Host \$host;
       proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
       proxy_set_header X-Forwarded-Proto \$scheme;
       proxy_http_version 1.1;
   }

 卸载
   cd $DIR && docker compose down && cd / && rm -rf $DIR
   或 curl -fsSL <仓库>/main/deploy/uninstall.sh | sudo bash -s -- --yes
────────────────────────────────────────────────────────────
EOF
