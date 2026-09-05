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

# ---------- daemon.json ----------

@test "render_daemon_json binds unix socket and loopback tcp only" {
  run wsld_render_daemon_json 23750
  [ "$status" -eq 0 ]
  [[ "$output" == *'"unix:///var/run/docker.sock"'* ]]
  [[ "$output" == *'"tcp://127.0.0.1:23750"'* ]]
  [[ "$output" != *'0.0.0.0'* ]]
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
