#!/usr/bin/env bats
# 測試 scripts/lib/bootstrap-lib.sh 的純函式（TDD）。
# 執行：bats scripts/tests

setup() {
  load '../lib/bootstrap-lib.sh'
  TMPD="$(mktemp -d)"
}

teardown() {
  rm -rf "$TMPD"
}

# ---------- port selection ----------

@test "pick_free_port returns start port when free" {
  wsld_port_in_use() { return 1; }
  run wsld_pick_free_port 23750
  [ "$status" -eq 0 ]
  [ "$output" = "23750" ]
}

@test "pick_free_port increments until a free port is found" {
  wsld_port_in_use() { [ "$1" -lt 23753 ]; }
  run wsld_pick_free_port 23750
  [ "$status" -eq 0 ]
  [ "$output" = "23753" ]
}

@test "pick_free_port fails after max attempts" {
  wsld_port_in_use() { return 0; }
  run wsld_pick_free_port 23750 5
  [ "$status" -ne 0 ]
  [[ "$output" == *"no free port"* ]]
}

@test "pick_free_port rejects non-numeric start" {
  run wsld_pick_free_port abc
  [ "$status" -ne 0 ]
}

@test "port_reserved honours WSLD_BUSY_PORTS csv" {
  WSLD_BUSY_PORTS="23750,23751"
  run wsld_port_reserved 23750
  [ "$status" -eq 0 ]
  run wsld_port_reserved 23751
  [ "$status" -eq 0 ]
  run wsld_port_reserved 23752
  [ "$status" -ne 0 ]
}

@test "port_in_use treats reserved ports as busy even if ss is free" {
  WSLD_BUSY_PORTS="23751"
  wsld_linux_port_in_use() { return 1; }
  wsld_windows_port_in_use() { return 1; }
  run wsld_port_in_use 23751
  [ "$status" -eq 0 ]
  run wsld_port_in_use 23750
  [ "$status" -ne 0 ]
}

@test "port_in_use detects windows listener via wsld_windows_port_in_use" {
  WSLD_BUSY_PORTS=""
  wsld_linux_port_in_use() { return 1; }
  wsld_windows_port_in_use() { [ "$1" = "23751" ]; }
  run wsld_port_in_use 23751
  [ "$status" -eq 0 ]
  run wsld_port_in_use 23750
  [ "$status" -ne 0 ]
}

@test "parse_windows_netstat_listening matches local listen lines" {
  sample=$'  TCP    127.0.0.1:23751        0.0.0.0:0              LISTENING       14676\r\n  TCP    0.0.0.0:445            0.0.0.0:0              LISTENING       4\r\n  TCP    127.0.0.1:23750        127.0.0.1:9            ESTABLISHED     1\r\n'
  run wsld_parse_windows_netstat_listening "$sample" 23751
  [ "$status" -eq 0 ]
  [ "$output" = "14676" ]
  run wsld_parse_windows_netstat_listening "$sample" 23750
  [ "$status" -ne 0 ]
  run wsld_parse_windows_netstat_listening "$sample" 445
  [ "$status" -eq 0 ]
  [ "$output" = "4" ]
}

@test "windows_port_in_use ignores wslrelay but flags foreign apps" {
  wsld_parse_windows_netstat_listening() {
    case "$2" in
      23750) echo 14848; return 0 ;;
      23751) echo 14676; return 0 ;;
      *) return 1 ;;
    esac
  }
  wsld_windows_process_name() {
    case "$1" in
      14848) echo wslrelay ;;
      14676) echo Cursor ;;
      *) echo unknown ;;
    esac
  }
  # bypass real netstat.exe
  netstat.exe() { echo dummy; }
  command() { [ "$1" = "-v" ] && [ "$2" = "netstat.exe" ]; }
  run wsld_windows_port_in_use 23750
  [ "$status" -ne 0 ]
  run wsld_windows_port_in_use 23751
  [ "$status" -eq 0 ]
}

@test "parse_args honours --busy-ports" {
  wsld_parse_args --busy-ports 23750,23751
  [ "$WSLD_BUSY_PORTS" = "23750,23751" ]
}

# ---------- daemon.json ----------

@test "render_daemon_json binds unix socket and loopback tcp only" {
  run wsld_render_daemon_json 23750
  [ "$status" -eq 0 ]
  [[ "$output" == *'"unix:///var/run/docker.sock"'* ]]
  [[ "$output" == *'"tcp://127.0.0.1:23750"'* ]]
  [[ "$output" != *'0.0.0.0'* ]]
}

@test "render helpers are safe under bash -s (no heredoc swallowing stdin)" {
  lib="$BATS_TEST_DIRNAME/../lib/bootstrap-lib.sh"
  run bash -s -- "$lib" <<'SCRIPT'
set -euo pipefail
. "$1"
wsld_render_daemon_json 23750
wsld_render_systemd_override
echo MARKER_AFTER_RENDER
SCRIPT
  [ "$status" -eq 0 ]
  [[ "$output" == *'"tcp://127.0.0.1:23750"'* ]]
  [[ "$output" == *"ExecStart=/usr/bin/dockerd"* ]]
  [[ "$output" == *MARKER_AFTER_RENDER* ]]
}

@test "validate_loopback_hosts accepts loopback-only config" {
  json='{"hosts":["unix:///var/run/docker.sock","tcp://127.0.0.1:23750"]}'
  run wsld_validate_loopback_hosts "$json"
  [ "$status" -eq 0 ]
}

@test "validate_loopback_hosts rejects 0.0.0.0" {
  json='{"hosts":["unix:///var/run/docker.sock","tcp://0.0.0.0:2375"]}'
  run wsld_validate_loopback_hosts "$json"
  [ "$status" -ne 0 ]
  [[ "$output" == *"0.0.0.0"* ]]
}

@test "validate_loopback_hosts rejects unspecified interface tcp://:port" {
  json='{"hosts":["tcp://:2375"]}'
  run wsld_validate_loopback_hosts "$json"
  [ "$status" -ne 0 ]
}

# ---------- systemd override ----------

@test "render_systemd_override resets ExecStart and drops -H fd://" {
  run wsld_render_systemd_override
  [ "$status" -eq 0 ]
  [[ "$output" == *"[Service]"* ]]
  # 第一行 ExecStart= 為清空，第二行為不帶 -H 的 dockerd
  [[ "$output" == *$'ExecStart=\n'* ]]
  [[ "$output" == *"ExecStart=/usr/bin/dockerd"* ]]
  [[ "$output" != *"-H fd://"* ]]
  [[ "$output" == *"--containerd=/run/containerd/containerd.sock"* ]]
}

@test "render_systemd_override for suse lets dockerd manage containerd" {
  run wsld_render_systemd_override suse
  [ "$status" -eq 0 ]
  [[ "$output" == *$'ExecStart=\nExecStart=/usr/bin/dockerd'* ]]
  [[ "$output" != *"--containerd"* ]]
}

# ---------- wsl.conf ----------

@test "wsl_conf_has_systemd detects [boot] systemd=true" {
  printf '[boot]\nsystemd=true\n' > "$TMPD/wsl.conf"
  run wsld_wsl_conf_has_systemd "$TMPD/wsl.conf"
  [ "$status" -eq 0 ]
}

@test "wsl_conf_has_systemd fails when missing or false" {
  printf '[boot]\nsystemd=false\n' > "$TMPD/wsl.conf"
  run wsld_wsl_conf_has_systemd "$TMPD/wsl.conf"
  [ "$status" -ne 0 ]
  run wsld_wsl_conf_has_systemd "$TMPD/none.conf"
  [ "$status" -ne 0 ]
}

@test "ensure_wsl_conf_systemd creates file when absent" {
  wsld_ensure_wsl_conf_systemd "$TMPD/wsl.conf"
  run wsld_wsl_conf_has_systemd "$TMPD/wsl.conf"
  [ "$status" -eq 0 ]
}

@test "ensure_wsl_conf_systemd appends boot section preserving other sections" {
  printf '[user]\ndefault=irice\n' > "$TMPD/wsl.conf"
  wsld_ensure_wsl_conf_systemd "$TMPD/wsl.conf"
  run wsld_wsl_conf_has_systemd "$TMPD/wsl.conf"
  [ "$status" -eq 0 ]
  grep -q '^default=irice' "$TMPD/wsl.conf"
}

@test "ensure_wsl_conf_systemd is idempotent" {
  wsld_ensure_wsl_conf_systemd "$TMPD/wsl.conf"
  wsld_ensure_wsl_conf_systemd "$TMPD/wsl.conf"
  [ "$(grep -c '^systemd=true' "$TMPD/wsl.conf")" -eq 1 ]
}

# ---------- args ----------

@test "parse_args defaults" {
  wsld_parse_args
  [ "$WSLD_PORT" = "23750" ]
  [ "$WSLD_INSTALL_COMPOSE" = "1" ]
  [ "$WSLD_DRY_RUN" = "0" ]
}

@test "parse_args honours --port --no-compose --dry-run --user" {
  wsld_parse_args --port 24000 --no-compose --dry-run --user bob
  [ "$WSLD_PORT" = "24000" ]
  [ "$WSLD_INSTALL_COMPOSE" = "0" ]
  [ "$WSLD_DRY_RUN" = "1" ]
  [ "$WSLD_USER" = "bob" ]
}

@test "parse_args --help sets WSLD_HELP and prints usage" {
  wsld_parse_args --help > "$TMPD/out"
  [ "$WSLD_HELP" = "1" ]
  grep -q -- '--port' "$TMPD/out"
}

@test "main script --help exits 0 without side effects" {
  run bash "$BATS_TEST_DIRNAME/../02-bootstrap-docker.sh" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"--dry-run"* ]]
  [[ "$output" != *"distro="* ]]
}

@test "parse_args rejects unknown flag" {
  run wsld_parse_args --bogus
  [ "$status" -eq 2 ]
}

# ---------- distro detection ----------

@test "is_wsl detects WSL kernel string" {
  run wsld_is_wsl "6.6.87.2-microsoft-standard-WSL2"
  [ "$status" -eq 0 ]
  run wsld_is_wsl "6.8.0-45-generic"
  [ "$status" -ne 0 ]
}

@test "detect_distro_id reads ID from os-release" {
  printf 'NAME="Ubuntu"\nID=ubuntu\nVERSION_CODENAME=resolute\n' > "$TMPD/os-release"
  run wsld_detect_distro_id "$TMPD/os-release"
  [ "$output" = "ubuntu" ]
}

@test "distro_family maps debian/arch/suse families (ID then ID_LIKE)" {
  printf 'ID=ubuntu\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$status" -eq 0 ]
  [ "$output" = "debian" ]

  printf 'ID=debian\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$status" -eq 0 ]
  [ "$output" = "debian" ]

  printf 'ID=arch\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$status" -eq 0 ]
  [ "$output" = "arch" ]

  printf 'ID=archlinux\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$status" -eq 0 ]
  [ "$output" = "arch" ]

  printf 'NAME="openSUSE Tumbleweed"\nID="opensuse-tumbleweed"\nID_LIKE="opensuse suse"\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$status" -eq 0 ]
  [ "$output" = "suse" ]

  printf 'ID="opensuse-leap"\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$output" = "suse" ]

  printf 'ID=pop\nID_LIKE="ubuntu debian"\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$status" -eq 0 ]
  [ "$output" = "debian" ]

  printf 'ID=fedora\n' > "$TMPD/os-release"
  run wsld_distro_family "$TMPD/os-release"
  [ "$status" -ne 0 ]
  [ "$output" = "unknown" ]
}

@test "docker_engine_installed is true when dockerd is on PATH" {
  PATH="$TMPD/bin:$PATH"
  mkdir -p "$TMPD/bin"
  printf '#!/bin/sh\n' > "$TMPD/bin/dockerd"
  chmod +x "$TMPD/bin/dockerd"
  run wsld_docker_engine_installed
  [ "$status" -eq 0 ]
}

@test "docker_engine_installed is false without dockerd" {
  PATH="/usr/bin:/bin"
  command -v dockerd >/dev/null 2>&1 && skip "host has dockerd"
  run wsld_docker_engine_installed
  [ "$status" -ne 0 ]
}
