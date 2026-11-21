#!/usr/bin/env bash
# wsld：在 WSL2 distro 內安裝／移除單節點 k3s（--docker，與該 distro 的 dockerd 共用映像檔）。
#
# 用法：03-k3s.sh install | uninstall
# 需以 root 執行（wsld 以 `wsl -d <distro> -u root -- bash -s` 餵入）。冪等。
# install 不啟動也不開機自啟 k3s；啟停由 `wsld k8s on|off` 管理（systemctl）。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/bootstrap-lib.sh
. "$SCRIPT_DIR/lib/bootstrap-lib.sh"

log() { printf '[wsld] %s\n' "$*"; }
die() { printf '[wsld] ERROR: %s\n' "$*" >&2; exit "${2:-1}"; }

[ "$(id -u)" -eq 0 ] || die "需要以 root 執行" 3

DROPIN_DIR=/etc/systemd/system/k3s.service.d
DROPIN="$DROPIN_DIR/10-wsld-docker.conf"

install_k3s() {
  systemctl cat docker.service >/dev/null 2>&1 || die "尚未安裝 docker（先執行 wsld init）" 3
  if wsld_k3s_installed; then
    log "k3s 已安裝"
  else
    command -v curl >/dev/null 2>&1 || die "需要 curl" 3
    install_exec="$(wsld_k3s_install_exec "$(wsld_k3s_node_name)")"
    log "下載並安裝 k3s（$install_exec）"
    # 先下載再執行：不可寫成 `curl … | sh - </dev/null`（</dev/null 會蓋掉管線，sh 讀到空腳本、curl 以 23 結束）
    tmp="$(mktemp)"
    trap 'rm -f "$tmp"' RETURN
    curl -sfL -o "$tmp" https://get.k3s.io || die "下載 k3s 安裝腳本失敗（網路？）" 6
    INSTALL_K3S_SKIP_START=true INSTALL_K3S_EXEC="$install_exec" sh "$tmp" </dev/null
  fi
  mkdir -p "$DROPIN_DIR"
  wsld_render_k3s_dropin > "$DROPIN"
  systemctl daemon-reload </dev/null
  # 啟停交給 wsld（k8s = true 的 machine 在 wsld start 時才啟動）
  systemctl disable k3s </dev/null >/dev/null 2>&1 || true
  log "k3s 安裝完成（unit 未啟用；以 wsld k8s on 啟動）"
}

uninstall_k3s() {
  if [ -x /usr/local/bin/k3s-uninstall.sh ]; then
    log "移除 k3s（k3s-uninstall.sh）"
    /usr/local/bin/k3s-uninstall.sh </dev/null
  else
    log "k3s 未安裝，略過"
  fi
  rm -rf "$DROPIN_DIR"
  systemctl daemon-reload </dev/null
  log "k3s 已移除"
}

case "${1:-}" in
  install) install_k3s ;;
  uninstall) uninstall_k3s ;;
  *) die "用法: 03-k3s.sh install|uninstall" 2 ;;
esac
