#!/usr/bin/env bats
# 測試 scripts/lib/k8s-lib.sh 的純函式（k3s 設定、unit 與掛載小程式的內容）。
# 執行：bats scripts/tests

setup() {
  load '../lib/k8s-lib.sh'
  TMPD="$(mktemp -d)"
}

teardown() {
  rm -rf "$TMPD"
}

@test "state dir lives under the exposure path so both distros resolve the same files" {
  run wsld_k8s_state_dir k8s-engine
  [ "$output" = "/mnt/wsl/distro/k8s-engine/var/lib/wsld-k8s" ]
}

@test "valid_name accepts distro names and rejects shell metacharacters" {
  wsld_k8s_valid_name k8s-engine
  wsld_k8s_valid_name My.K8s_1
  ! wsld_k8s_valid_name ''
  ! wsld_k8s_valid_name 'a b'
  ! wsld_k8s_valid_name 'x;rm'
  ! wsld_k8s_valid_name '-x'
}

@test "k3s config uses cri-dockerd, no CNI, kubelet and storage under the exposure path" {
  run wsld_render_k3s_config k8s-engine 26443
  [ "$status" -eq 0 ]
  [[ "$output" == *"node-name: k8s-engine"* ]]
  [[ "$output" == *"https-listen-port: 26443"* ]]
  [[ "$output" == *"container-runtime-endpoint: unix:///run/wsld-cri-dockerd.sock"* ]]
  [[ "$output" == *"flannel-backend: none"* ]]
  [[ "$output" == *"root-dir=/mnt/wsl/distro/k8s-engine/var/lib/wsld-k8s/kubelet"* ]]
  [[ "$output" == *"default-local-storage-path: /mnt/wsl/distro/k8s-engine/var/lib/wsld-k8s/storage"* ]]
  [[ "$output" == *"cgroups-per-qos=false"* ]]
  [[ "$output" != *"docker: true"* ]]
}

@test "k3s config appends kubelet args and tls-san entries; none by default" {
  run wsld_render_k3s_config k8s-engine 26443
  [[ "$output" != *"tls-san:"* ]]
  run wsld_render_k3s_config k8s-engine 26443 tls-san=my-pc.lan kubelet-arg=max-pods=50 tls-san=192.168.1.20
  [ "$status" -eq 0 ]
  [[ "$output" == *$'  - cgroup-driver=systemd\n  - max-pods=50\ntls-san:\n  - my-pc.lan\n  - 192.168.1.20'* ]]
}

@test "extra k3s values only allow safe characters" {
  wsld_k8s_valid_extra my-pc.lan
  wsld_k8s_valid_extra '*.k8s.localhost'
  wsld_k8s_valid_extra max-pods=50
  ! wsld_k8s_valid_extra 'a b'
  ! wsld_k8s_valid_extra 'x;rm'
  ! wsld_k8s_valid_extra ''
}

@test "cri-dockerd unit talks to the engine socket without a network plugin and needs the mounts" {
  run wsld_render_cri_unit
  [[ "$output" == *"--docker-endpoint unix:///mnt/wsl/wsld/docker.sock"* ]]
  [[ "$output" == *"--network-plugin= "* ]]
  [[ "$output" == *"Requires=wsld-k8s-mounts.service"* ]]
}

@test "k3s drop-in refuses to start without the exposure and publishes the kubeconfig" {
  run wsld_render_k3s_dropin k8s-engine
  [[ "$output" == *"Requires=wsld-k8s-mounts.service wsld-cri-dockerd.service"* ]]
  [[ "$output" == *"ExecStartPre=/usr/bin/mountpoint -q /mnt/wsl/distro/k8s-engine"* ]]
  [[ "$output" == *"/mnt/wsl/wsld/k8s/kubeconfig"* ]]
}

@test "generated helper scripts are valid bash" {
  wsld_render_k8s_mounts_script k8s-engine > "$TMPD/m.sh"
  wsld_render_engine_share_script > "$TMPD/s.sh"
  bash -n "$TMPD/m.sh"
  bash -n "$TMPD/s.sh"
}

# 以 PATH 上的假命令執行產生的小程式，記錄它呼叫了哪些 mount／umount。
stub_mounts() {
  mkdir -p "$TMPD/bin"
  # mountpoint：$TMPD/mounted 裡列出的路徑才算掛載點
  printf '%s\n' '#!/bin/bash' 'p="${@: -1}"; grep -qxF "$p" "$MOUNTED"' > "$TMPD/bin/mountpoint"
  printf '%s\n' '#!/bin/bash' 'echo "mount $*" >> "$LOG"; [ "$1" = --bind ] && echo "$3" >> "$MOUNTED"; exit 0' > "$TMPD/bin/mount"
  printf '%s\n' '#!/bin/bash' 'echo "umount $*" >> "$LOG"; grep -vxF "$1" "$MOUNTED" > "$MOUNTED.n"; mv "$MOUNTED.n" "$MOUNTED"' > "$TMPD/bin/umount"
  printf '%s\n' '#!/bin/bash' 'cat "$MOUNTED"' > "$TMPD/bin/findmnt"
  printf '%s\n' '#!/bin/bash' 'echo ext2/ext3' > "$TMPD/bin/stat"
  printf '%s\n' '#!/bin/bash' 'exit 0' > "$TMPD/bin/mkdir"
  chmod +x "$TMPD/bin/"*
  export LOG="$TMPD/log" MOUNTED="$TMPD/mounted"
  : > "$LOG"
  : > "$MOUNTED"
}

@test "mounts up refuses to run before the engine shares its container directory" {
  stub_mounts
  wsld_render_k8s_mounts_script k8s-engine > "$TMPD/m.sh"
  PATH="$TMPD/bin:$PATH" run bash "$TMPD/m.sh" up
  [ "$status" -eq 4 ]
  [[ "$output" == *"engine has not shared"* ]]
  [ ! -s "$LOG" ]
}

@test "mounts up exposes the root and binds the shared containers dir privately" {
  stub_mounts
  echo /mnt/wsl/wsld/k8s/containers >> "$MOUNTED"
  wsld_render_k8s_mounts_script k8s-engine > "$TMPD/m.sh"
  PATH="$TMPD/bin:$PATH" run bash "$TMPD/m.sh" up
  grep -qxF "mount --bind / /mnt/wsl/distro/k8s-engine" "$LOG"
  grep -qxF "mount --bind /mnt/wsl/wsld/k8s/containers /var/lib/docker/containers" "$LOG"
  grep -qxF "mount --make-private /var/lib/docker/containers" "$LOG"
}

@test "mounts down unmounts kubelet mounts deepest first, never lazily or recursively" {
  stub_mounts
  S=/mnt/wsl/distro/k8s-engine/var/lib/wsld-k8s
  printf '%s\n' /mnt/wsl/distro/k8s-engine /var/lib/docker/containers \
    "$S/kubelet/pods/a" "$S/kubelet/pods/a/volumes/x" >> "$MOUNTED"
  wsld_render_k8s_mounts_script k8s-engine > "$TMPD/m.sh"
  PATH="$TMPD/bin:$PATH" run bash "$TMPD/m.sh" down
  [ "$status" -eq 0 ]
  run cat "$LOG"
  [ "${lines[0]}" = "umount $S/kubelet/pods/a/volumes/x" ]
  [ "${lines[1]}" = "umount $S/kubelet/pods/a" ]
  [ "${lines[2]}" = "umount /var/lib/docker/containers" ]
  [ "${lines[3]}" = "umount /mnt/wsl/distro/k8s-engine" ]
  ! grep -qE -- ' -[a-zA-Z]*[lR]' "$LOG"
}

@test "engine share mounts read-only and private" {
  stub_mounts
  wsld_render_engine_share_script > "$TMPD/s.sh"
  PATH="$TMPD/bin:$PATH" run bash "$TMPD/s.sh" up
  [ "$status" -eq 0 ]
  grep -qxF "mount --bind /var/lib/docker/containers /mnt/wsl/wsld/k8s/containers" "$LOG"
  grep -qxF "mount -o remount,bind,ro /mnt/wsl/wsld/k8s/containers" "$LOG"
  grep -qxF "mount --make-private /mnt/wsl/wsld/k8s/containers" "$LOG"
}
