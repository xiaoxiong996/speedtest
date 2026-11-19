#!/usr/bin/env bash
# ============================================================================
#  SpeedTest 纯测速节点 —— 一键卸载
#
#    curl -fsSL https://raw.githubusercontent.com/<用户名>/<仓库>/main/deploy/uninstall-node.sh \
#      | sudo bash -s -- --yes
#
#  参数：
#    --dir <目录>    安装目录，默认 /opt/speedtest-node
#    --yes           不再交互确认
#    --purge-image   同时删除本地镜像
# ============================================================================
set -euo pipefail

DIR="/opt/speedtest-node"
YES="0"
PURGE="0"

log()  { printf '\033[36m[node]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[node]\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --dir)         DIR="${2:-}"; shift 2 ;;
    --yes|-y)      YES="1"; shift ;;
    --purge-image) PURGE="1"; shift ;;
    -h|--help)
      cat <<'USAGE'
用法：uninstall-node.sh [选项]

  --dir <目录>    安装目录，默认 /opt/speedtest-node
  --yes, -y       不再交互确认
  --purge-image   同时删除本地镜像
  -h, --help      显示本帮助
USAGE
      exit 0 ;;
    *)             die "未知参数：$1" ;;
  esac
done

[ "$(id -u)" = "0" ] || die "请用 root 运行（或前面加 sudo）"

IMAGE=""
if [ -f "$DIR/.env" ]; then
  IMAGE="$(grep -m1 '^IMAGE=' "$DIR/.env" | cut -d= -f2- || true)"
fi

if [ "$YES" != "1" ]; then
  printf '将停止并删除节点容器，并删除目录 %s（回车继续，Ctrl+C 取消）：' "$DIR"
  read -r _ || true
fi

if [ -d "$DIR" ]; then
  cd "$DIR"
  if command -v docker >/dev/null 2>&1; then
    log "停止并移除容器 …"
    docker compose down --remove-orphans 2>/dev/null || docker rm -f speedtest-node 2>/dev/null || true
  fi
  log "删除目录 $DIR …"
  cd /
  rm -rf "$DIR"
else
  docker rm -f speedtest-node 2>/dev/null || true
fi

if [ "$PURGE" = "1" ] && [ -n "$IMAGE" ]; then
  log "删除镜像 $IMAGE …"
  docker image rm "$IMAGE" 2>/dev/null || true
fi

log "已卸载。"
