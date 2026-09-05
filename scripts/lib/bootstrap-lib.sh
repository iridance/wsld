#!/usr/bin/env bash
# wsl-docker bootstrap 純函式庫（可被 bats 測試）。
# 由 scripts/02-bootstrap-docker.sh source；不要在此檔執行副作用。

WSLD_DEFAULT_PORT=23750
WSLD_MAX_PORT_ATTEMPTS=50

# --- 參數 -----------------------------------------------------------------

# 設定：WSLD_PORT WSLD_USER WSLD_INSTALL_COMPOSE WSLD_DRY_RUN（由主腳本使用）
# 回傳 2 = 用法錯誤
# shellcheck disable=SC2034
wsld_parse_args() {
  WSLD_PORT="$WSLD_DEFAULT_PORT"
  WSLD_USER="${SUDO_USER:-${USER:-$(id -un)}}"
  WSLD_INSTALL_COMPOSE=1
  WSLD_DRY_RUN=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --port) shift; WSLD_PORT="${1:?--port needs a value}" ;;
      --user) shift; WSLD_USER="${1:?--user needs a value}" ;;
      --no-compose) WSLD_INSTALL_COMPOSE=0 ;;
      --dry-run) WSLD_DRY_RUN=1 ;;
      -h|--help) wsld_usage; return 0 ;;
      *) echo "unknown argument: $1" >&2; wsld_usage >&2; return 2 ;;
    esac
    shift
  done
  return 0
}

wsld_usage() {
  cat <<'EOF'
用法: 02-bootstrap-docker.sh [--port N] [--user NAME] [--no-compose] [--dry-run]
  --port N       Docker API 起始埠（只綁 127.0.0.1；占用時自動遞增），預設 23750
  --user NAME    要加入 docker 群組並啟用 linger 的使用者，預設目前使用者
  --no-compose   不安裝 docker-compose-plugin
  --dry-run      只印出將執行的動作
EOF
}

# --- 環境偵測 -------------------------------------------------------------

# $1 = kernel release 字串（預設 uname -r）
wsld_is_wsl() {
  local rel="${1:-$(uname -r)}"
  case "$rel" in
    *[Mm]icrosoft*|*WSL*) return 0 ;;
    *) return 1 ;;
  esac
}

# $1 = os-release 路徑（預設 /etc/os-release）
wsld_detect_distro_id() {
  local f="${1:-/etc/os-release}"
  [ -r "$f" ] || return 1
  sed -n 's/^ID=//p' "$f" | tr -d '"' | head -1
}

# --- 埠選擇 ----------------------------------------------------------------

# 可被測試覆寫。回傳 0 = 埠已被占用。
wsld_port_in_use() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -Hltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}\$"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}\$"
  else
    return 1
  fi
}

# $1 = 起始埠, $2 = 最大嘗試次數。stdout = 選到的埠。
wsld_pick_free_port() {
  local start="$1" max="${2:-$WSLD_MAX_PORT_ATTEMPTS}" i port
  case "$start" in
    ''|*[!0-9]*) echo "invalid start port: $start" >&2; return 2 ;;
  esac
  i=0
  while [ "$i" -lt "$max" ]; do
    port=$((start + i))
    if ! wsld_port_in_use "$port"; then
      echo "$port"
      return 0
    fi
    i=$((i + 1))
  done
  echo "no free port in range ${start}-$((start + max - 1))" >&2
  return 1
}

# --- 設定檔渲染 ----------------------------------------------------------

# $1 = port
wsld_render_daemon_json() {
  local port="$1"
  cat <<EOF
{
  "hosts": ["unix:///var/run/docker.sock", "tcp://127.0.0.1:${port}"]
}
EOF
}

# $1 = JSON 字串。拒絕任何非 loopback 的 tcp host。
wsld_validate_loopback_hosts() {
  local json="$1" host
  while read -r host; do
    [ -n "$host" ] || continue
    case "$host" in
      unix://*) ;;
      tcp://127.0.0.1:*|tcp://localhost:*|tcp://\[::1\]:*) ;;
      *)
        echo "non-loopback docker host is not allowed: $host" >&2
        return 1
        ;;
    esac
  done < <(printf '%s' "$json" | grep -oE '(unix|tcp)://[^"]*')
  return 0
}

# systemd drop-in：清掉 -H fd://，改由 daemon.json 決定 hosts。
wsld_render_systemd_override() {
  cat <<'EOF'
[Service]
ExecStart=
ExecStart=/usr/bin/dockerd --containerd=/run/containerd/containerd.sock
EOF
}

# --- wsl.conf ------------------------------------------------------------

# $1 = wsl.conf 路徑
wsld_wsl_conf_has_systemd() {
  local f="$1"
  [ -r "$f" ] || return 1
  awk '
    /^\[/ { sec=$0 }
    sec=="[boot]" && $0 ~ /^[[:space:]]*systemd[[:space:]]*=[[:space:]]*true[[:space:]]*$/ { found=1 }
    END { exit found ? 0 : 1 }
  ' "$f"
}

# $1 = wsl.conf 路徑。冪等。
wsld_ensure_wsl_conf_systemd() {
  local f="$1"
  if wsld_wsl_conf_has_systemd "$f" 2>/dev/null; then
    return 0
  fi
  if [ -f "$f" ] && grep -q '^\[boot\]' "$f"; then
    # 已有 [boot] 區段：在該行後插入或修正 systemd
    if grep -Eq '^[[:space:]]*systemd[[:space:]]*=' "$f"; then
      sed -i -E 's/^[[:space:]]*systemd[[:space:]]*=.*/systemd=true/' "$f"
    else
      sed -i '/^\[boot\]/a systemd=true' "$f"
    fi
  else
    local prefix=""
    if [ -s "$f" ]; then prefix=$'\n'; fi
    printf '%s[boot]\nsystemd=true\n' "$prefix" >> "$f"
  fi
}
