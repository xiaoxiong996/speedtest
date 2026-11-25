#!/usr/bin/env bash
# ============================================================================
#  SpeedTest 非 Docker 安装脚本 —— 普通用户 + systemd --user，全程不需要 root
#
#  主控（控制台，默认只监听 127.0.0.1:3333，前面接你自己的反代）：
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/install-local.sh \
#      | bash -s -- --mode controller --port 3333
#
#  测速节点（监听 8443，自动签发 Let's Encrypt 证书）：
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/install-local.sh \
#      | bash -s -- --mode node --domain node1.example.com --origin https://speed.example.com
#
#  常用参数：
#    --mode     controller | node    默认 controller
#    --port     <端口>               主控默认 3333，节点默认 8443（必须 ≥1024，非 root 绑不了特权端口）
#    --bind     <地址>               主控监听地址，默认 127.0.0.1（给反代用）；直接对外填 0.0.0.0
#    --version  <标签>               默认 latest，也可写 v1.4.5
#    --repo     <owner/repo>         默认 xiaoxiong996/speedtest（fork 后换成你自己的）
#    --binary   <本地文件>           跳过下载，直接用本机已有的二进制
#    --dir      <目录>               默认 ~/.local/share/speedtest（节点为 speedtest-node）
#    --admin-user / --admin-pass     主控首次安装的后台账号（默认 admin，密码不给就随机生成并打印）
#    --domain   <域名>               节点必填，证书域名（需已解析到本机公网 IP）
#    --origin   <来源>               节点必填，允许调用它的主控站，例如 https://speed.example.com
#    --host     <域名>               节点可选，额外允许的 Host，逗号分隔，支持 *.example.com
#    --cidr     <IP 段>              节点可选，允许测速的访客 IP 段，逗号分隔
#    --prefix   <路径前缀>           节点可选，例如 /n1
#    --cf-token <令牌>               节点可选，Cloudflare API Token（填了证书全自动签发续期）
#    --panel-path / --panel-user / --panel-pass   节点可选，启用节点面板
#    --email    <邮箱>               证书到期提醒邮箱（可选）
#    --staging                       用 Let's Encrypt 测试环境（证书不被信任，仅调试）
#    --no-start                      只写文件不启动
#
#  卸载：deploy/uninstall-local.sh（同名参数 --mode）
# ============================================================================
set -euo pipefail

MODE="controller"
PORT=""
BIND=""
VERSION="latest"
REPO="xiaoxiong996/speedtest"
BINARY=""
DIR=""
ADMIN_USER="admin"
ADMIN_PASS=""
DOMAIN=""
ORIGIN=""
HOSTS=""
CIDRS=""
PREFIX=""
PANEL_PATH=""
PANEL_USER=""
PANEL_PASS=""
CF_TOKEN=""
EMAIL=""
STAGING="0"
START="1"

log()  { printf '\033[36m[speedtest]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[speedtest]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[speedtest]\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --mode)       MODE="${2:-}"; shift 2 ;;
    --port)       PORT="${2:-}"; shift 2 ;;
    --bind)       BIND="${2:-}"; shift 2 ;;
    --version)    VERSION="${2:-}"; shift 2 ;;
    --repo)       REPO="${2:-}"; shift 2 ;;
    --binary)     BINARY="${2:-}"; shift 2 ;;
    --dir)        DIR="${2:-}"; shift 2 ;;
    --admin-user) ADMIN_USER="${2:-}"; shift 2 ;;
    --admin-pass) ADMIN_PASS="${2:-}"; shift 2 ;;
    --domain)     DOMAIN="${2:-}"; shift 2 ;;
    --origin)     ORIGIN="${2:-}"; shift 2 ;;
    --host)       HOSTS="${2:-}"; shift 2 ;;
    --cidr)       CIDRS="${2:-}"; shift 2 ;;
    --prefix)     PREFIX="${2:-}"; shift 2 ;;
    --cf-token)   CF_TOKEN="${2:-}"; shift 2 ;;
    --panel-path) PANEL_PATH="${2:-}"; shift 2 ;;
    --panel-user) PANEL_USER="${2:-}"; shift 2 ;;
    --panel-pass) PANEL_PASS="${2:-}"; shift 2 ;;
    --email)      EMAIL="${2:-}"; shift 2 ;;
    --staging)    STAGING="1"; shift ;;
    --no-start)   START="0"; shift ;;
    -h|--help)
      cat <<'USAGE'
非 Docker 安装（systemd 用户服务，不需要 root）

  --mode     controller | node    默认 controller
  --port     <端口>               主控默认 3333，节点默认 8443（必须 ≥1024）
  --bind     <地址>               主控监听地址，默认 127.0.0.1
  --version  <标签>               默认 latest，也可写 v1.4.5
  --repo     <owner/repo>         默认 xiaoxiong996/speedtest
  --binary   <本地文件>           跳过下载，用本机已有二进制
  --dir      <目录>               默认 ~/.local/share/speedtest{,-node}
  --admin-user / --admin-pass     主控首次安装的后台账号
  --domain   <域名>               节点必填，证书域名
  --origin   <来源>               节点必填，允许的主控站
  --host / --cidr / --prefix      节点可选：允许的 Host、访客 IP 段、路径前缀
  --cf-token <令牌>               节点可选，Cloudflare API Token
  --panel-path / --panel-user / --panel-pass   节点可选，启用面板
  --email <邮箱> / --staging      节点可选：证书邮箱、用测试环境
  --no-start                      只写文件不启动
USAGE
      exit 0 ;;
    *)            die "未知参数：$1" ;;
  esac
done

# ---------- 环境检查 ----------
[ "$(id -u)" != "0" ] || warn "当前是 root，systemd --user 会装到 root 名下；一般应当用普通用户跑这个脚本"
command -v systemctl >/dev/null 2>&1 || die "没有 systemctl，本脚本依赖 systemd --user 用户服务"
[ -d /run/systemd/system ] || die "systemd 没有以 init 方式运行（容器里？），无法使用用户服务"

case "$MODE" in
  controller) SERVICE="speedtest";        DEF_DIR="$HOME/.local/share/speedtest";      DEF_PORT="3333" ;;
  node)       SERVICE="speedtest-node";   DEF_DIR="$HOME/.local/share/speedtest-node"; DEF_PORT="8443" ;;
  *) die "--mode 只能是 controller 或 node" ;;
esac

PORT="${PORT:-$DEF_PORT}"
DIR="${DIR:-$DEF_DIR}"

case "$PORT" in
  ''|*[!0-9]*) die "--port 必须是数字" ;;
esac
[ "$PORT" -ge 1024 ] && [ "$PORT" -le 65535 ] || die "--port 必须 ≥1024（非 root 无法绑定 1024 以下的端口）"

if [ "$MODE" = "node" ]; then
  [ -n "$DOMAIN" ] || die "节点必须用 --domain 指定证书域名（需已解析到本机公网 IP）"
  [ -n "$ORIGIN" ] || warn "没给 --origin，节点会接受任意来源的调用（带宽可能被别人盗用）"
fi

# systemd --user 需要一个可用的运行时目录
if [ -z "${XDG_RUNTIME_DIR:-}" ]; then
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR
fi
[ -d "$XDG_RUNTIME_DIR" ] || die "缺少 $XDG_RUNTIME_DIR，当前会话不是登录会话；请直接用 SSH 登录后再运行"
SB="systemctl --user"

# ---------- 架构 ----------
case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "不支持的架构：$(uname -m)（只提供 amd64 / arm64）" ;;
esac

# ---------- 目录 ----------
mkdir -p "$DIR/bin" "$DIR/data"
if [ "$MODE" = "node" ]; then
  mkdir -p "$DIR/certs"
  chmod 700 "$DIR/certs"
fi
chmod 700 "$DIR"

# ---------- 二进制 ----------
BIN="$DIR/bin/speedtest"
if [ -n "$BINARY" ]; then
  [ -f "$BINARY" ] || die "找不到二进制：$BINARY"
  log "使用本地二进制 $BINARY"
  install -m 0755 "$BINARY" "$BIN"
else
  if [ "$VERSION" = "latest" ]; then
    URL="https://github.com/$REPO/releases/latest/download/speedtest-linux-$ARCH"
  else
    URL="https://github.com/$REPO/releases/download/$VERSION/speedtest-linux-$ARCH"
  fi
  log "下载二进制 $URL"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --connect-timeout 15 -o "$BIN.tmp" "$URL" || die "下载失败。可先用 --binary 指定本地文件，或检查网络能否访问 github.com"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$BIN.tmp" "$URL" || die "下载失败。可先用 --binary 指定本地文件"
  else
    die "没有 curl 也没有 wget，请用 --binary 指定本地二进制"
  fi
  chmod 0755 "$BIN.tmp"
  mv "$BIN.tmp" "$BIN"
fi
chmod 0755 "$BIN"

VER="$("$BIN" --version 2>/dev/null | head -1 || true)"
[ -n "$VER" ] || die "二进制无法执行（架构不符或文件损坏）：$BIN"
log "二进制就绪：$VER ($ARCH)"

# ---------- 配置 ----------
if [ "$MODE" = "controller" ]; then
  if [ -z "$ADMIN_PASS" ]; then
    if [ -f "$DIR/.env" ] && grep -q '^ADMIN_PASSWORD=.\+' "$DIR/.env" 2>/dev/null; then
      ADMIN_PASS="$(sed -n 's/^ADMIN_PASSWORD=//p' "$DIR/.env" | head -1)"
    else
      ADMIN_PASS="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 16)"
    fi
  fi
  cat > "$DIR/.env" <<EOF
APP_MODE=controller
HTTP_HOST=${BIND:-127.0.0.1}
HTTP_PORT=$PORT
DATA_DIR=$DIR/data
TZ=Asia/Shanghai
ADMIN_USER=$ADMIN_USER
ADMIN_PASSWORD=$ADMIN_PASS
SITE_NAME=SpeedTest
LOG_CLIENT_IP=0
EOF
  chmod 600 "$DIR/.env"
else
  cat > "$DIR/.env" <<EOF
APP_MODE=node
HTTP_HOST=0.0.0.0
HTTP_PORT=$PORT
NODE_PORT=$PORT
DATA_DIR=$DIR/data
TZ=Asia/Shanghai
NODE_ACME_DOMAINS=$DOMAIN
NODE_ACME_EMAIL=$EMAIL
NODE_CERT_DIR=$DIR/certs
NODE_ACME_DIR=$DIR/certs/acme
NODE_ACME_STAGING=$STAGING
NODE_CERT_RENEW_DAYS=30
NODE_ALLOW_ORIGINS=$ORIGIN
NODE_ALLOW_HOSTS=$HOSTS
NODE_ALLOW_CIDRS=$CIDRS
NODE_PATH_PREFIX=$PREFIX
NODE_PANEL_PATH=$PANEL_PATH
NODE_PANEL_USER=$PANEL_USER
NODE_PANEL_PASS=$PANEL_PASS
NODE_HTTP_BOOTSTRAP_MINUTES=10
CF_DNS_API_TOKEN=$CF_TOKEN
EOF
  chmod 600 "$DIR/.env"
fi

# ---------- systemd 用户服务 ----------
UNIT_DIR="$HOME/.config/systemd/user"
mkdir -p "$UNIT_DIR"
if [ "$MODE" = "controller" ]; then
  DESC="SpeedTest 主控 (controller)"
else
  DESC="SpeedTest 测速节点 (node)"
fi

cat > "$UNIT_DIR/$SERVICE.service" <<EOF
[Unit]
Description=$DESC
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
WorkingDirectory=$DIR
EnvironmentFile=$DIR/.env
ExecStart=$BIN
Restart=always
RestartSec=3
# 非 root 进程能收紧的几项都收紧；数据目录在 home 下，不能再上 ProtectHome
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
LimitNOFILE=65535

[Install]
WantedBy=default.target
EOF

$SB daemon-reload

if [ "$START" != "1" ]; then
  log "已写入 $DIR 与 $UNIT_DIR/$SERVICE.service（未启动）"
  log "启动：systemctl --user enable --now $SERVICE"
  exit 0
fi

$SB enable --now "$SERVICE" >/dev/null 2>&1 || $SB restart "$SERVICE"

# ---------- 等端口就绪 ----------
ok="0"
for _ in $(seq 1 20); do
  if (command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ":$PORT ") \
     || (command -v netstat >/dev/null 2>&1 && netstat -ltn 2>/dev/null | grep -q ":$PORT "); then
    ok="1"; break
  fi
  sleep 1
done
[ "$ok" = "1" ] || warn "端口 $PORT 还没监听，看日志：journalctl --user -u $SERVICE -n 50 --no-pager"

# ---------- 开机自启 ----------
LINGER="$(loginctl show-user "$(id -un)" -p Linger 2>/dev/null | cut -d= -f2 || echo unknown)"
if [ "$LINGER" != "yes" ]; then
  warn "当前用户未开启 linger：注销后服务会停，重启机器也不会自启。"
  warn "有 sudo 的话执行一次即可（只需一次）：sudo loginctl enable-linger $(id -un)"
fi

# ---------- 结果 ----------
echo
log "安装完成：$SERVICE（用户 $(id -un)，端口 $PORT）"
echo "  目录    $DIR"
echo "  配置    $DIR/.env"
echo "  查看日志  journalctl --user -u $SERVICE -f"
echo "  重启      systemctl --user restart $SERVICE"
echo "  停止      systemctl --user stop $SERVICE"
echo "  卸载      bash deploy/uninstall-local.sh --mode $MODE"
if [ "$MODE" = "controller" ]; then
  echo "  后台账号  $ADMIN_USER / $ADMIN_PASS"
  echo "  访问      http://$( [ "${BIND:-127.0.0.1}" = "0.0.0.0" ] && echo "127.0.0.1" || echo "${BIND:-127.0.0.1}" ):$PORT/   （对外请在前面加 HTTPS 反代）"
else
  echo "  节点地址  https://$DOMAIN:$PORT"
  echo "  首次签发证书可能需要一两分钟，进度看日志"
  if [ -n "$PANEL_PATH" ]; then
    echo "  节点面板  https://$DOMAIN:$PORT/$PANEL_PATH"
  fi
fi
