#!/usr/bin/env bash
# wsld bootstrap 純函式庫（可被 bats 測試）。
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
  WSLD_HELP=0
  WSLD_BUSY_PORTS=""
  WSLD_ROLE=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --port) shift; WSLD_PORT="${1:?--port needs a value}" ;;
      --busy-ports) shift; WSLD_BUSY_PORTS="${1:?--busy-ports needs a value}" ;;
      --user) shift; WSLD_USER="${1:?--user needs a value}" ;;
      --no-compose) WSLD_INSTALL_COMPOSE=0 ;;
      --role)
        shift
        WSLD_ROLE="${1:?--role needs a value}"
        case "$WSLD_ROLE" in
          engine|client) ;;
          *) echo "unknown role: $WSLD_ROLE (engine|client)" >&2; return 2 ;;
        esac
        ;;
      --dry-run) WSLD_DRY_RUN=1 ;;
      -h|--help) WSLD_HELP=1; wsld_usage; return 0 ;;
      *) echo "unknown argument: $1" >&2; wsld_usage >&2; return 2 ;;
    esac
    shift
  done
  return 0
}

wsld_usage() {
  printf '%s\n' \
    '用法: 02-bootstrap-docker.sh [--port N] [--busy-ports CSV] [--user NAME] [--no-compose] [--role engine|client] [--dry-run]' \
    '  --port N         Docker API 起始埠（只綁 127.0.0.1；占用時自動遞增），預設 23750' \
    '  --busy-ports CSV 額外視為占用的埠（例如其他 machine 已登記的 api_endpoint）' \
    '  --user NAME      要加入 docker 群組並啟用 linger 的使用者，預設目前使用者' \
    '  --no-compose     不安裝 docker-compose-plugin' \
    '  --role ROLE      engine = 專用 dockerd distro（共用 socket 放 /mnt/wsl）；client = 只裝 docker CLI 並連到 engine；省略 = 自帶 dockerd' \
    '  --dry-run        只印出將執行的動作'
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

# --- 發行版分類表（scripts/lib/distros.tsv）-------------------------------

# stdout = 分類表（已去掉註解與空行）。bundle 模式由 WSLD_DISTROS_TABLE 內嵌；
# 否則讀 WSLD_DISTROS_FILE（預設與本檔同目錄的 distros.tsv）。
wsld_distros_table() {
  local f
  if [ -n "${WSLD_DISTROS_TABLE:-}" ]; then
    printf '%s\n' "$WSLD_DISTROS_TABLE"
  else
    f="${WSLD_DISTROS_FILE:-$(dirname "${BASH_SOURCE[0]:-.}")/distros.tsv}"
    [ -r "$f" ] || return 1
    cat "$f"
  fi | sed -e 's/#.*//' -e '/^[[:space:]]*$/d'
}

# $1 = os-release ID。stdout = "<family> <docker_repo> <docker_codename>"；查無 exit 1。
wsld_distro_lookup() {
  local id="$1"
  [ -n "$id" ] || return 1
  wsld_distros_table | awk -v id="$id" '
    $1 == id { print $2, $3, $4; found = 1; exit }
    END { exit found ? 0 : 1 }
  '
}

# $1 = os-release 路徑。stdout = family（debian | arch | suse | rhel | azure）；不明則 unknown 且 exit 1。
# 先查分類表，查無再以 ID／ID_LIKE 推斷衍生版。
wsld_distro_family() {
  local f="${1:-/etc/os-release}" id like row
  id="$(wsld_detect_distro_id "$f" || true)"
  if row="$(wsld_distro_lookup "$id")"; then
    echo "${row%% *}"
    return 0
  fi
  like=""
  [ -r "$f" ] && like="$(sed -n 's/^ID_LIKE=//p' "$f" | tr -d '"' | head -1)"
  for cand in $id $like; do
    case "$cand" in
      ubuntu|debian)
        echo debian
        return 0
        ;;
      arch|archlinux|manjaro|endeavouros)
        echo arch
        return 0
        ;;
      opensuse*|suse|sles|sled|sle-micro)
        echo suse
        return 0
        ;;
    esac
  done
  echo unknown
  return 1
}

# Docker 官方 apt 來源只有 ubuntu／debian（及其 codename），衍生版（kali、pop、mint…）要對應回去。
# 先查分類表；表中沒有的衍生版再依 UBUNTU_CODENAME／ID_LIKE／DEBIAN_CODENAME 推斷。
# $1 = os-release 路徑。stdout = "<repo_id> <codename>"，例如 "debian trixie"。
WSLD_DOCKER_DEBIAN_CODENAMES="bullseye bookworm trixie"
WSLD_DOCKER_DEBIAN_FALLBACK="trixie"
wsld_docker_apt_repo() {
  local f="${1:-/etc/os-release}" id like ver ubu deb c
  [ -r "$f" ] || return 1
  _wsld_osr() { sed -n "s/^$1=//p" "$f" | tr -d '"' | head -1; }
  id="$(_wsld_osr ID)"
  like="$(_wsld_osr ID_LIKE)"
  ver="$(_wsld_osr VERSION_CODENAME)"
  ubu="$(_wsld_osr UBUNTU_CODENAME)"
  deb="$(_wsld_osr DEBIAN_CODENAME)"
  unset -f _wsld_osr

  local row t_repo t_code
  if row="$(wsld_distro_lookup "$id")"; then
    read -r _ t_repo t_code <<< "$row"
    if [ "$t_repo" != "-" ]; then
      if [ "$t_code" = "-" ]; then
        if [ "$t_repo" = "ubuntu" ]; then t_code="${ubu:-$ver}"; else t_code="${ver:-$WSLD_DOCKER_DEBIAN_FALLBACK}"; fi
      fi
      echo "$t_repo $t_code"
      return 0
    fi
  fi

  if [ "$id" = "ubuntu" ] || [ -n "$ubu" ] || [[ " $like " == *" ubuntu "* ]]; then
    echo "ubuntu ${ubu:-$ver}"
    return 0
  fi
  if [ "$id" = "debian" ] && [ -n "$ver" ]; then
    echo "debian $ver"
    return 0
  fi
  # Debian 衍生版：DEBIAN_CODENAME → 已知 debian codename → 目前穩定版（kali-rolling 等滾動版）
  for c in $deb $ver; do
    case " $WSLD_DOCKER_DEBIAN_CODENAMES " in
      *" $c "*) echo "debian $c"; return 0 ;;
    esac
  done
  echo "debian $WSLD_DOCKER_DEBIAN_FALLBACK"
}

# dockerd 在 PATH 上即視為引擎已安裝（跨 apt／pacman）。
wsld_docker_engine_installed() {
  command -v dockerd >/dev/null 2>&1
}

# --- 埠選擇 ----------------------------------------------------------------

# $1 = port。WSLD_BUSY_PORTS（逗號分隔）內的埠視為占用。
wsld_port_reserved() {
  local port="$1" csv="${WSLD_BUSY_PORTS:-}" item
  [ -n "$csv" ] || return 1
  IFS=',' read -r -a _wsld_busy <<< "$csv"
  for item in "${_wsld_busy[@]}"; do
    item="${item#"${item%%[![:space:]]*}"}"
    item="${item%"${item##*[![:space:]]}"}"
    [ "$item" = "$port" ] && return 0
  done
  return 1
}

# Linux／WSL 內 ss／netstat。可被測試覆寫。回傳 0 = 占用。
wsld_linux_port_in_use() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -Hltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}\$"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}\$"
  else
    return 1
  fi
}

# 解析 Windows netstat.exe -ano 輸出；$1 = 全文, $2 = port。
# stdout = 佔用該埠 LISTENING 的 PID（可能多行）；exit 0 = 至少一個。
wsld_parse_windows_netstat_listening() {
  local text="$1" port="$2"
  printf '%s\n' "$text" | tr -d '\r' | awk -v p=":$port" '
    BEGIN { found=0 }
    $1 == "TCP" && ($4 == "LISTENING" || $4 == "LISTEN") {
      n = split($2, a, ":")
      if (n >= 2 && (":" a[n]) == p) { print $5; found=1 }
    }
    END { exit found ? 0 : 1 }
  '
}

# Windows 主機上「非 WSL 轉發」的佔用（Cursor／其他 App）。可被測試覆寫。
# wslrelay／wsl／wslservice 視為本產品的 localhost 轉發，不算衝突。
wsld_windows_port_in_use() {
  local port="$1" out pids pid name
  command -v netstat.exe >/dev/null 2>&1 || return 1
  out="$(netstat.exe -ano 2>/dev/null)" || return 1
  pids="$(wsld_parse_windows_netstat_listening "$out" "$port")" || return 1
  while IFS= read -r pid; do
    [ -n "$pid" ] || continue
    case "$pid" in
      *[!0-9]*) continue ;;
    esac
    name="$(wsld_windows_process_name "$pid")"
    case "$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')" in
      wslrelay|wsl|wslservice|wsld|wsld-gui) ;;
      '') return 0 ;; # 有 LISTEN 但查不到行程名 → 寧可視為占用
      *) return 0 ;;
    esac
  done <<< "$pids"
  return 1
}

# $1 = PID。stdout = 行程名（無副檔名）。可被測試覆寫。
wsld_windows_process_name() {
  local pid="$1" line
  command -v tasklist.exe >/dev/null 2>&1 || return 0
  line="$(tasklist.exe /FI "PID eq ${pid}" /FO CSV /NH 2>/dev/null | tr -d '\r' | head -1)" || return 0
  # "Cursor.exe","14676","..." 
  line="${line#\"}"
  line="${line%%\"*}"
  line="${line%.exe}"
  line="${line%.EXE}"
  printf '%s' "$line"
}

# 可被測試覆寫。回傳 0 = 埠已被占用（Linux／Windows／busy 清單任一）。
wsld_port_in_use() {
  local port="$1"
  wsld_port_reserved "$port" && return 0
  wsld_linux_port_in_use "$port" && return 0
  wsld_windows_port_in_use "$port" && return 0
  return 1
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
# 注意：不可用 cat <<EOF——bundle 以 `bash -s`（stdin=腳本）執行時 heredoc 會吞掉後續腳本。
# $2（可選）= 共用 unix socket 路徑（engine 模式，放在跨 distro 共用的 /mnt/wsl tmpfs）。
wsld_render_daemon_json() {
  local port="$1" shared="${2:-}" hosts='"unix:///var/run/docker.sock"'
  [ -n "$shared" ] && hosts="$hosts, \"unix://$shared\""
  printf '%s\n' '{'
  printf '  "hosts": [%s, "tcp://127.0.0.1:%s"]\n' "$hosts" "$port"
  printf '%s\n' '}'
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
# $1 = family（debian|arch|suse）。suse 的 containerd.service 與 docker.service 互斥（Conflicts=），
# dockerd 須自行管理 containerd，故不帶 --containerd。
wsld_render_systemd_override() {
  local exec='/usr/bin/dockerd --containerd=/run/containerd/containerd.sock'
  [ "${1:-}" = "suse" ] && exec='/usr/bin/dockerd'
  printf '%s\n' \
    '[Service]' \
    'ExecStart=' \
    "ExecStart=$exec"
}

# --- 共用 engine（專用 dockerd distro）-----------------------------------

# /mnt/wsl 是所有 WSL2 distro 共用的 tmpfs；engine 在此放 socket，client 的 docker context 指向它。
WSLD_ENGINE_SOCK="/mnt/wsl/wsld/docker.sock"

# engine 的 docker.service drop-in：沿用 wsld_render_systemd_override，另在啟動前建立共用目錄、
# 清掉殘留 socket（tmpfs 於 VM 重啟後清空），啟動後開放 0666（各 distro 的 docker GID 不同）。
# $1 = family、$2 = 共用 socket 路徑。
wsld_render_engine_override() {
  local family="${1:-}" sock="${2:-$WSLD_ENGINE_SOCK}" dir
  dir="$(dirname "$sock")"
  wsld_render_systemd_override "$family"
  printf '%s\n' \
    "ExecStartPre=/bin/mkdir -p $dir" \
    "ExecStartPre=/bin/rm -f $sock" \
    "ExecStartPost=/bin/chmod 0666 $sock"
}

# client 要安裝的套件（只要 CLI 與 plugin，不裝 daemon）。$1 = family、$2 = 1 則含 compose。
# Arch／openSUSE 沒有獨立 CLI 套件：裝完整 docker，但不 enable 服務。
wsld_client_packages() {
  local family="$1" compose="${2:-1}" pkgs
  case "$family" in
    debian)
      pkgs="docker-ce-cli docker-buildx-plugin"
      [ "$compose" = "1" ] && pkgs="$pkgs docker-compose-plugin"
      ;;
    arch)
      pkgs="docker docker-buildx"
      [ "$compose" = "1" ] && pkgs="$pkgs docker-compose"
      ;;
    suse)
      pkgs="docker docker-buildx"
      [ "$compose" = "1" ] && pkgs="$pkgs docker-compose"
      ;;
    *) echo "unsupported family: $family" >&2; return 1 ;;
  esac
  printf '%s\n' "$pkgs"
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

# $1 = ini 路徑、$2 = section、$3 = key、$4 = value。冪等；保留其他區段；key 只在指定 section 內比對。
wsld_ensure_ini_key() {
  local f="$1" sec="$2" key="$3" val="$4" tmp
  [ -f "$f" ] || : > "$f"
  tmp="$(mktemp)"
  awk -v sec="[$sec]" -v key="$key" -v val="$val" '
    function flush() { if (cur == sec && !done) { print key "=" val; done = 1 } }
    /^[[:space:]]*\[/ { flush(); cur = $0; gsub(/[[:space:]]/, "", cur); seen = seen || (cur == sec) }
    cur == sec && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" { if (!done) { print key "=" val; done = 1 } ; next }
    { print }
    END {
      flush()
      if (!seen) { if (NR > 0) print ""; print sec; print key "=" val }
    }
  ' "$f" > "$tmp"
  cat "$tmp" > "$f"
  rm -f "$tmp"
}

# --- k3s -----------------------------------------------------------------

# k3s 安裝參數（PoC 定案）：與 dockerd 共用映像檔（--docker）、不啟用 traefik（避免搶 80/443）、
# kubeconfig 對一般使用者可讀（WSL 內免 sudo 即可 kubectl）。
# 可帶一個 node 名稱參數：node 以 WSL machine（distro）命名，而不是 k3s 預設的 hostname
# （WSL2 的 hostname 預設沿用 Windows 電腦名稱，每台 machine 都一樣，無法分辨）。
# 第二個參數為 API server 埠：避開 6443（Docker Desktop／minikube／kind 都用它）；
# 只接受 1024–65535 的整數，其餘忽略（沿用 k3s 預設 6443）。
wsld_k3s_install_exec() {
  local node="${1:-}" port="${2:-}" exec_args='--docker --disable traefik --write-kubeconfig-mode 644'
  if [ -n "$node" ]; then exec_args="$exec_args --node-name $node"; fi
  if [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1024 ] && [ "$port" -le 65535 ]; then
    exec_args="$exec_args --https-listen-port $port"
  fi
  printf '%s\n' "$exec_args"
}

# k3s node 名稱：distro 名稱轉成合法的 k8s node 名稱（小寫 DNS 子網域：英數、-、.；頭尾須為英數；≤63）。
# 取自參數，缺省時用 WSL 提供的 $WSL_DISTRO_NAME；轉完是空字串就不指定（交給 k3s 用 hostname）。
wsld_k3s_node_name() {
  local n="${1:-${WSL_DISTRO_NAME:-}}"
  n="$(printf '%s' "$n" | tr '[:upper:]' '[:lower:]' | sed -e 's/[^a-z0-9.-]/-/g' -e 's/^[^a-z0-9]*//' -e 's/[^a-z0-9]*$//')"
  n="${n:0:63}"
  n="$(printf '%s' "$n" | sed -e 's/[^a-z0-9]*$//')"
  printf '%s\n' "$n"
}

# k3s.service drop-in：必須排在 docker.service 之後。
# PoC 發現：開機時 k3s 早於 dockerd 啟動，agent 不會啟動 kubelet／cri-dockerd（node NotReady）。
wsld_render_k3s_dropin() {
  printf '%s\n' \
    '[Unit]' \
    'After=docker.service' \
    'Requires=docker.service'
}

# 已安裝 k3s？（$1 可覆寫二進位路徑，測試用）
wsld_k3s_installed() {
  [ -x "${1:-/usr/local/bin/k3s}" ]
}
