#!/usr/bin/env bash
# ============================================================================
#  SpeedTest 主控版 —— 一键卸载
#
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/uninstall.sh \
#      | sudo bash -s -- --yes
#
#  参数：
#    --dir <目录>     安装目录，默认 /opt/speedtest
#    --yes            不再交互确认
#    --purge-data     连 data/（配置 + 报告历史）一起删；不加则保留
#    --purge-image    同时删除本地镜像
# ============================================================================
set -euo pipefail

DIR="/opt/speedtest"
YES="0"
PURGE_DATA="0"
PURGE_IMAGE="0"

log()  { printf '\033[36m[speed]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[speed]\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --dir)         DIR="${2:-}"; shift 2 ;;
    --yes|-y)      YES="1"; shift ;;
    --purge-data)  PURGE_DATA="1"; shift ;;
    --purge-image) PURGE_IMAGE="1"; shift ;;
    -h|--help)
      cat <<'USAGE'
用法：uninstall.sh [选项]

  --dir <目录>     安装目录，默认 /opt/speedtest
  --yes, -y        不再交互确认
  --purge-data     删除 data/（配置与报告历史）；不加则保留
  --purge-image    同时删除本地镜像
  -h, --help       显示本帮助
USAGE
      exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

[ "$(id -u)" = "0" ] || die "请用 root 运行（或前面加 sudo）"

IMAGE=""
if [ -f "$DIR/.env" ]; then
  IMAGE="$(grep -m1 '^IMAGE=' "$DIR/.env" | cut -d= -f2- || true)"
fi

if [ "$YES" != "1" ]; then
  printf '将停止并删除主控容器 %s；data/ %s。回车继续，Ctrl+C 取消：' \
    "$DIR" "$([ "$PURGE_DATA" = "1" ] && echo '会一起删除（不可恢复）' || echo '会保留')"
  read -r _ </dev/tty || true
fi

if [ -d "$DIR" ]; then
  cd "$DIR"
  if command -v docker >/dev/null 2>&1; then
    log "停止并移除容器 …"
    docker compose down --remove-orphans 2>/dev/null || docker rm -f speedtest 2>/dev/null || true
  fi

  if [ "$PURGE_DATA" = "1" ]; then
    log "删除目录 $DIR（含 data/）…"
    cd /
    rm -rf "$DIR"
  else
    log "删除编排与配置，保留 $DIR/data …"
    rm -f "$DIR/docker-compose.yml" "$DIR/.env" "$DIR"/.env.bak.*
  fi
else
  docker rm -f speedtest 2>/dev/null || true
  log "目录 $DIR 不存在，已尝试移除同名容器"
fi

if [ "$PURGE_IMAGE" = "1" ] && [ -n "$IMAGE" ]; then
  log "删除镜像 $IMAGE …"
  docker image rm "$IMAGE" 2>/dev/null || true
fi

log "已卸载。"
[ "$PURGE_DATA" = "1" ] || log "数据仍在 $DIR/data（要彻底删除：rm -rf $DIR）"
