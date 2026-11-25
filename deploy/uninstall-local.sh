#!/usr/bin/env bash
# ============================================================================
#  SpeedTest 非 Docker 卸载（对应 install-local.sh）
#
#    bash deploy/uninstall-local.sh --mode node
#    bash deploy/uninstall-local.sh --mode controller --purge   # 连同数据与证书一起删
# ============================================================================
set -euo pipefail

MODE="controller"
DIR=""
PURGE="0"
YES="0"

log()  { printf '\033[36m[speedtest]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[speedtest]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[speedtest]\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --mode)  MODE="${2:-}"; shift 2 ;;
    --dir)   DIR="${2:-}"; shift 2 ;;
    --purge) PURGE="1"; shift ;;
    --yes)   YES="1"; shift ;;
    -h|--help)
      echo "用法：uninstall-local.sh [--mode controller|node] [--dir 目录] [--purge] [--yes]"
      exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

case "$MODE" in
  controller) SERVICE="speedtest";      DIR="${DIR:-$HOME/.local/share/speedtest}" ;;
  node)       SERVICE="speedtest-node"; DIR="${DIR:-$HOME/.local/share/speedtest-node}" ;;
  *) die "--mode 只能是 controller 或 node" ;;
esac

if [ -z "${XDG_RUNTIME_DIR:-}" ]; then
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR
fi

UNIT="$HOME/.config/systemd/user/$SERVICE.service"

if command -v systemctl >/dev/null 2>&1 && [ -f "$UNIT" ]; then
  log "停止并禁用 $SERVICE"
  systemctl --user disable --now "$SERVICE" >/dev/null 2>&1 || true
  rm -f "$UNIT"
  systemctl --user daemon-reload >/dev/null 2>&1 || true
else
  warn "没找到 $UNIT，跳过服务处理"
fi

if [ "$PURGE" = "1" ]; then
  if [ -d "$DIR" ]; then
    if [ "$YES" != "1" ]; then
      warn "将删除 $DIR（含数据与证书），10 秒后继续，Ctrl+C 取消"
      sleep 10
    fi
    rm -rf -- "$DIR"
    log "已删除 $DIR"
  fi
else
  log "保留 $DIR（配置、数据、证书都在这里）；要一并删除加 --purge"
fi

log "卸载完成"
