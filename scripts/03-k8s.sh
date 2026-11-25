#!/usr/bin/env bash
# wsld：單節點 k3s 跑在專用 distro（k8s-engine），runtime 用 engine 的 dockerd。架構見 scripts/lib/k8s-lib.sh。
#
# 用法（以 root 執行；冪等；wsld 把 bundle 寫到 /tmp 後執行）：
#   03-k8s.sh install <distro> <api-port>   在 k8s-engine 內：套件、cri-dockerd、k3s、掛載與 unit（不啟動、不開機自啟）
#   03-k8s.sh share-engine                  在 engine 內：安裝並啟用 wsld-k8s-share（分享容器 log 目錄）
#   03-k8s.sh unshare-engine                在 engine 內：停用並移除 wsld-k8s-share
# 結束碼：2 用法錯誤、3 systemd 尚未生效（wsl --terminate 後重跑）、6 下載失敗、7 checksum 不符。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/bootstrap-lib.sh
. "$SCRIPT_DIR/lib/bootstrap-lib.sh"
# shellcheck source=scripts/lib/k8s-lib.sh
. "$SCRIPT_DIR/lib/k8s-lib.sh"

log() { printf '[wsld] %s\n' "$*"; }
die() { printf '[wsld] ERROR: %s\n' "$1" >&2; exit "${2:-1}"; }

[ "$(id -u)" -eq 0 ] || die "需要以 root 執行" 3

# 寫檔：內容不同才寫（避免每次都 daemon-reload 觸發重啟）。$1 = 路徑、$2 = 權限；內容由 stdin。
write_file() {
  local path="$1" mode="$2" tmp
  tmp="$(mktemp)"
  cat > "$tmp"
  if [ -f "$path" ] && cmp -s "$tmp" "$path"; then
    rm -f "$tmp"
    return 0
  fi
  mkdir -p "$(dirname "$path")"
  install -m "$mode" "$tmp" "$path"
  rm -f "$tmp"
}

ensure_systemd() {
  local tmp need=0
  if ! wsld_wsl_conf_has_systemd /etc/wsl.conf; then
    tmp="$(mktemp)"
    [ -f /etc/wsl.conf ] && cat /etc/wsl.conf > "$tmp"
    wsld_ensure_wsl_conf_systemd "$tmp"
    install -m 0644 "$tmp" /etc/wsl.conf
    rm -f "$tmp"
    need=1
  fi
  # 專用 distro：不把 Windows PATH 灌進來（只影響下次啟動）
  tmp="$(mktemp)"
  [ -f /etc/wsl.conf ] && cat /etc/wsl.conf > "$tmp"
  wsld_ensure_ini_key "$tmp" interop appendWindowsPath false
  install -m 0644 "$tmp" /etc/wsl.conf
  rm -f "$tmp"
  if [ "$need" = 1 ] || ! [ -d /run/systemd/system ]; then
    log "systemd 尚未生效：需要重新啟動此 distro"
    exit 3
  fi
}

install_packages() {
  local missing=() p
  for p in curl ca-certificates iptables socat conntrack; do
    dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p")
  done
  [ "${#missing[@]}" -eq 0 ] && return 0
  log "安裝套件：${missing[*]}"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y </dev/null >/dev/null
  apt-get install -y "${missing[@]}" </dev/null >/dev/null
}

install_cri_dockerd() {
  local want="$WSLD_CRI_DOCKERD_VERSION" tmp url
  if [ -x /usr/local/bin/cri-dockerd ] && /usr/local/bin/cri-dockerd --version 2>&1 | grep -qF "cri-dockerd $want "; then
    log "cri-dockerd $want 已安裝"
    return 0
  fi
  url="https://github.com/Mirantis/cri-dockerd/releases/download/v$want/cri-dockerd-$want.amd64.tgz"
  log "下載 cri-dockerd $want"
  tmp="$(mktemp -d)"
  curl -sfL -o "$tmp/cri.tgz" "$url" || { rm -rf "$tmp"; die "下載 cri-dockerd 失敗（網路？）" 6; }
  printf '%s  %s\n' "$WSLD_CRI_DOCKERD_SHA256" "$tmp/cri.tgz" | sha256sum -c --status \
    || { rm -rf "$tmp"; die "cri-dockerd checksum 不符" 7; }
  tar -xzf "$tmp/cri.tgz" -C "$tmp"
  install -m 0755 "$tmp/cri-dockerd/cri-dockerd" /usr/local/bin/cri-dockerd
  rm -rf "$tmp"
}

install_k3s() {
  local want="$WSLD_K3S_VERSION" tmp
  if [ -x /usr/local/bin/k3s ] && /usr/local/bin/k3s --version 2>/dev/null | grep -qF "k3s version $want "; then
    log "k3s $want 已安裝"
    return 0
  fi
  log "下載並安裝 k3s $want"
  tmp="$(mktemp)"
  # 先下載再執行：不可寫成 `curl … | sh - </dev/null`（</dev/null 會蓋掉管線）
  curl -sfL -o "$tmp" https://get.k3s.io || { rm -f "$tmp"; die "下載 k3s 安裝腳本失敗（網路？）" 6; }
  INSTALL_K3S_VERSION="$want" INSTALL_K3S_SKIP_START=true INSTALL_K3S_SKIP_ENABLE=true sh "$tmp" </dev/null
  rm -f "$tmp"
}

install_k8s() {
  local name="${1:-}" port="${2:-}"
  wsld_k8s_valid_name "$name" || die "用法: 03-k8s.sh install <distro> <api-port>" 2
  [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1024 ] && [ "$port" -le 65535 ] || die "api-port 必須是 1024–65535" 2
  ensure_systemd
  install_packages
  install_cri_dockerd
  wsld_render_k8s_mounts_script "$name" | write_file /usr/local/sbin/wsld-k8s-mounts 0755
  wsld_render_k8s_mounts_unit | write_file /etc/systemd/system/wsld-k8s-mounts.service 0644
  wsld_render_cri_unit | write_file /etc/systemd/system/wsld-cri-dockerd.service 0644
  wsld_render_k3s_config "$name" "$port" | write_file /etc/rancher/k3s/config.yaml 0644
  wsld_render_k3s_dropin "$name" | write_file /etc/systemd/system/k3s.service.d/10-wsld.conf 0644
  install_k3s
  systemctl daemon-reload </dev/null
  # 啟停交給 wsld（k8s 開啟時才啟動）
  systemctl disable k3s wsld-cri-dockerd </dev/null >/dev/null 2>&1 || true
  log "k8s 安裝完成（distro=$name api-port=$port）"
}

share_engine() {
  systemctl cat docker.service >/dev/null 2>&1 || die "此 distro 沒有 docker（不是 engine？）" 3
  wsld_render_engine_share_script | write_file /usr/local/sbin/wsld-k8s-share 0755
  wsld_render_engine_share_unit | write_file /etc/systemd/system/wsld-k8s-share.service 0644
  systemctl daemon-reload </dev/null
  systemctl enable wsld-k8s-share </dev/null >/dev/null 2>&1
  systemctl restart wsld-k8s-share </dev/null
  log "engine 已分享容器目錄到 $WSLD_K8S_SHARE/containers"
}

unshare_engine() {
  if [ -f /etc/systemd/system/wsld-k8s-share.service ]; then
    systemctl disable --now wsld-k8s-share </dev/null >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/wsld-k8s-share.service
    systemctl daemon-reload </dev/null
  fi
  [ -x /usr/local/sbin/wsld-k8s-share ] && /usr/local/sbin/wsld-k8s-share down
  rm -f /usr/local/sbin/wsld-k8s-share
  log "engine 已停止分享"
}

case "${1:-}" in
  install) install_k8s "${2:-}" "${3:-}" ;;
  share-engine) share_engine ;;
  unshare-engine) unshare_engine ;;
  *) die "用法: 03-k8s.sh install <distro> <api-port> | share-engine | unshare-engine" 2 ;;
esac
