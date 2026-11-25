#!/usr/bin/env bash
# wsld k8s：k3s 跑在專用 distro（k8s-engine），runtime 用 engine 的 dockerd（cri-dockerd 經 /mnt/wsl/wsld/docker.sock）。
#
# WSL2 各 distro 共用 network namespace，但 mount／PID namespace 各自獨立。dockerd 解析 bind 來源、
# kubelet 讀容器 log 都用「自己那邊」的路徑，所以兩邊用到的路徑必須一樣：
#   - kubelet root-dir 與 local-path 儲存放在 k8s-engine 的曝露路徑 /mnt/wsl/distro/<名稱>/var/lib/wsld-k8s 底下
#     （k8s-engine 的根目錄以非遞迴 bind 掛在這裡，兩邊看到的是同一份磁碟）；
#   - engine 把 /var/lib/docker/containers 唯讀 bind 到 /mnt/wsl/wsld/k8s/containers，k8s-engine 再 bind 回同路徑。
# pod 網路不走 CNI（cri-dockerd 要 /proc/<pid>/ns/net，跨 PID namespace 拿不到），改用 docker 原生網路。
#
# 這裡只放純函式（產生設定檔／小程式內容），由 bats 測試；03-k8s.sh 負責安裝。
# 注意：不可用 cat <<EOF（見 bootstrap-lib.sh 的說明），一律 printf。

# 跨 distro 共用的 k8s 目錄（/mnt/wsl 是所有 WSL2 distro 共用的 tmpfs）。
WSLD_K8S_SHARE="/mnt/wsl/wsld/k8s"
# engine 的 dockerd socket（見 bootstrap-lib.sh 的 WSLD_ENGINE_SOCK）。
WSLD_K8S_DOCKER_SOCK="/mnt/wsl/wsld/docker.sock"
# cri-dockerd 在 k8s-engine 內的 CRI socket。
WSLD_K8S_CRI_SOCK="/run/wsld-cri-dockerd.sock"
# 固定版本（實機驗證過的組合；升級時一併更新 checksum）。03-k8s.sh 使用。
# shellcheck disable=SC2034
WSLD_K3S_VERSION="v1.37.2+k3s1"
# shellcheck disable=SC2034
WSLD_CRI_DOCKERD_VERSION="0.4.11"
# shellcheck disable=SC2034
WSLD_CRI_DOCKERD_SHA256="eb8a965169e82da1e57339a2f0a9296fcaff3202455fd7254080cdb53f4d0522"

# $1 = distro 名稱 → 曝露掛載點。
wsld_k8s_expose_path() { printf '/mnt/wsl/distro/%s\n' "$1"; }

# $1 = distro 名稱 → 兩邊路徑相同的 k8s 狀態目錄（kubelet、local-path 儲存）。
wsld_k8s_state_dir() { printf '%s/var/lib/wsld-k8s\n' "$(wsld_k8s_expose_path "$1")"; }

# 名稱只允許 distro 名稱字元（會寫進 unit 與 config）。
wsld_k8s_valid_name() { [[ "${1:-}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; }

# /etc/rancher/k3s/config.yaml。$1 = distro 名稱、$2 = API 埠。
wsld_render_k3s_config() {
  local name="$1" port="$2" state
  state="$(wsld_k8s_state_dir "$name")"
  printf '%s\n' \
    '# 由 wsld 產生：k3s 用 engine 的 dockerd（cri-dockerd），pod 走 docker 原生網路。' \
    "node-name: $name" \
    "https-listen-port: $port" \
    "container-runtime-endpoint: unix://$WSLD_K8S_CRI_SOCK" \
    'flannel-backend: none' \
    'disable-network-policy: true' \
    'disable:' \
    '  - traefik' \
    '  - metrics-server' \
    'write-kubeconfig-mode: "0644"' \
    "default-local-storage-path: $state/storage" \
    'kubelet-arg:' \
    "  - root-dir=$state/kubelet" \
    '  - cgroups-per-qos=false' \
    '  - enforce-node-allocatable=' \
    '  - cgroup-driver=systemd'
}

# k8s-engine 的掛載小程式 /usr/local/sbin/wsld-k8s-mounts（up|down|status）。$1 = distro 名稱。
# up：確認 engine 已分享 containers、曝露本 distro 的根目錄、bind containers（唯讀、private：docker 之後在裡面
#     建立的 shm 掛載不會再傳播進 /mnt/wsl）。曝露路徑若只是 tmpfs 上的殘留空殼才清掉它。
# down：由深到淺卸載 kubelet 留在狀態目錄下的掛載（一般 umount，絕不 -R／-l），再卸載 containers 與曝露。
wsld_render_k8s_mounts_script() {
  local name="$1" expose state
  expose="$(wsld_k8s_expose_path "$name")"
  state="$(wsld_k8s_state_dir "$name")"
  # shellcheck disable=SC2016  # 單引號是刻意的：輸出的是要寫進檔案的腳本樣板
  printf '%s\n' \
    '#!/bin/bash' \
    '# 由 wsld 產生：k8s-engine 與 engine 之間的路徑對應（見 scripts/lib/k8s-lib.sh）。' \
    'set -u' \
    "T='$expose'" \
    "S='$state'" \
    "C='$WSLD_K8S_SHARE/containers'" \
    'up() {' \
    '  mountpoint -q "$C" || { echo "engine has not shared $C (is the engine running?)" >&2; exit 4; }' \
    '  mkdir -p "$T"' \
    '  if ! mountpoint -q "$T"; then' \
    '    [ "$(stat -f -c %T "$T")" = tmpfs ] && rm -rf "$T/var"' \
    '    mount --bind / "$T" || exit 1' \
    '  fi' \
    '  mkdir -p /var/lib/docker/containers /var/lib/wsld-k8s/kubelet /var/lib/wsld-k8s/storage' \
    '  if ! mountpoint -q /var/lib/docker/containers; then' \
    '    mount --bind "$C" /var/lib/docker/containers || exit 1' \
    '  fi' \
    '  mount --make-private /var/lib/docker/containers' \
    '  mountpoint -q "$T" && [ -d "$S/kubelet" ]' \
    '}' \
    'down() {' \
    '  local i m' \
    '  for i in 1 2 3; do' \
    '    findmnt -rn -o TARGET | grep -F "$S/" | sort -r | uniq | while read -r m; do umount "$m" 2>/dev/null; done' \
    '  done' \
    '  if findmnt -rn -o TARGET | grep -qF "$S/"; then echo "mounts still busy under $S" >&2; exit 1; fi' \
    '  if mountpoint -q /var/lib/docker/containers; then umount /var/lib/docker/containers || exit 1; fi' \
    '  if mountpoint -q "$T"; then umount "$T" || exit 1; fi' \
    '}' \
    'case "${1:-}" in' \
    '  up) up ;;' \
    '  down) down ;;' \
    '  status) mountpoint -q "$T" && mountpoint -q /var/lib/docker/containers && echo up || { echo down; exit 1; } ;;' \
    '  *) echo "usage: wsld-k8s-mounts up|down|status" >&2; exit 2 ;;' \
    'esac'
}

# wsld-k8s-mounts.service：不開機自啟，由 cri-dockerd／k3s 的 Requires= 帶起；停止時卸載。
wsld_render_k8s_mounts_unit() {
  printf '%s\n' \
    '[Unit]' \
    'Description=wsld k8s path mapping with the engine (/mnt/wsl)' \
    '[Service]' \
    'Type=oneshot' \
    'RemainAfterExit=yes' \
    'ExecStart=/usr/local/sbin/wsld-k8s-mounts up' \
    'ExecStop=/usr/local/sbin/wsld-k8s-mounts down'
}

# wsld-cri-dockerd.service：CRI → engine 的 dockerd。--network-plugin= 空值 = 不用 CNI（docker 原生網路）。
wsld_render_cri_unit() {
  printf '%s\n' \
    '[Unit]' \
    'Description=wsld cri-dockerd (CRI to the engine dockerd)' \
    'Requires=wsld-k8s-mounts.service' \
    'After=wsld-k8s-mounts.service' \
    '[Service]' \
    "ExecStart=/usr/local/bin/cri-dockerd --docker-endpoint unix://$WSLD_K8S_DOCKER_SOCK --container-runtime-endpoint unix://$WSLD_K8S_CRI_SOCK --network-plugin= --cri-dockerd-root-directory /var/lib/wsld-k8s/cri-dockerd" \
    'Restart=on-failure' \
    'RestartSec=2'
}

# k3s.service drop-in：一定要等掛載與 cri-dockerd；掛載不在就不准啟動（否則 kubelet 會把資料寫進 tmpfs）。
# $1 = distro 名稱。
wsld_render_k3s_dropin() {
  local expose
  expose="$(wsld_k8s_expose_path "$1")"
  printf '%s\n' \
    '[Unit]' \
    'Requires=wsld-k8s-mounts.service wsld-cri-dockerd.service' \
    'After=wsld-k8s-mounts.service wsld-cri-dockerd.service' \
    '[Service]' \
    "ExecStartPre=/usr/bin/mountpoint -q $expose" \
    "ExecStartPost=-/bin/sh -c 'for i in \$(seq 1 60); do [ -s /etc/rancher/k3s/k3s.yaml ] && break; sleep 1; done; mkdir -p $WSLD_K8S_SHARE && install -m 0644 /etc/rancher/k3s/k3s.yaml $WSLD_K8S_SHARE/kubeconfig'"
}

# engine 的分享小程式 /usr/local/sbin/wsld-k8s-share（up|down）：/var/lib/docker/containers 唯讀、private 地分享到 /mnt/wsl。
wsld_render_engine_share_script() {
  # shellcheck disable=SC2016  # 單引號是刻意的：輸出的是要寫進檔案的腳本樣板
  printf '%s\n' \
    '#!/bin/bash' \
    '# 由 wsld 產生：把 dockerd 的容器目錄（log）分享給 k8s-engine（見 scripts/lib/k8s-lib.sh）。' \
    'set -u' \
    "C='$WSLD_K8S_SHARE/containers'" \
    'case "${1:-}" in' \
    '  up)' \
    '    mkdir -p "$C"' \
    '    if ! mountpoint -q "$C"; then mount --bind /var/lib/docker/containers "$C" || exit 1; fi' \
    '    mount -o remount,bind,ro "$C" && mount --make-private "$C" ;;' \
    '  down)' \
    '    if mountpoint -q "$C"; then umount "$C" || exit 1; fi ;;' \
    '  *) echo "usage: wsld-k8s-share up|down" >&2; exit 2 ;;' \
    'esac'
}

# wsld-k8s-share.service：engine 開機（docker 起來後）自動分享；k8s 關閉時由 wsld 停用。
wsld_render_engine_share_unit() {
  printf '%s\n' \
    '[Unit]' \
    'Description=wsld share docker container logs with k8s-engine' \
    'After=docker.service' \
    'Wants=docker.service' \
    '[Service]' \
    'Type=oneshot' \
    'RemainAfterExit=yes' \
    'ExecStart=/usr/local/sbin/wsld-k8s-share up' \
    'ExecStop=/usr/local/sbin/wsld-k8s-share down' \
    '[Install]' \
    'WantedBy=multi-user.target'
}
