#!/usr/bin/env bash
# wsld Phase 1：在 WSL2（Ubuntu/Debian、Arch 或 openSUSE）內安裝 rootful Docker，
# 讓 dockerd 同時監聽 unix socket 與 tcp://127.0.0.1:<port>（只綁 loopback），
# 並啟用 systemd／linger，供 Windows 端 wsld / wsld-gui 使用。
#
# --role engine：專用 dockerd distro，另在 /mnt/wsl/wsld/docker.sock 提供跨 distro 共用的 socket。
# --role client：只裝 docker CLI/plugin，建立指向 engine socket 的 docker context，不裝 daemon。
#
# 用法見 --help。冪等；可重複執行。需要 sudo（會在需要時提示密碼）。
# 結束時輸出機器可讀行：WSLD_API_PORT=<port> WSLD_API_ENDPOINT=tcp://127.0.0.1:<port>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/bootstrap-lib.sh
. "$SCRIPT_DIR/lib/bootstrap-lib.sh"

log()  { printf '[wsld] %s\n' "$*"; }
warn() { printf '[wsld] WARN: %s\n' "$*" >&2; }
die()  { printf '[wsld] ERROR: %s\n' "$*" >&2; exit "${2:-1}"; }

wsld_parse_args "$@" || exit $?
[ "$WSLD_HELP" = "1" ] && exit 0

run() {
  if [ "$WSLD_DRY_RUN" = "1" ]; then
    printf '[dry-run] %s\n' "$*"
  else
    # bash -s（stdin=腳本）時，systemctl／部分工具會讀 stdin 吞掉後續腳本
    "$@" </dev/null
  fi
}

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 || die "需要 sudo 或以 root 執行" 3
  SUDO="sudo"
fi

# ---------------------------------------------------------------- 0. 環境檢查
wsld_is_wsl || die "此腳本僅支援在 WSL2 發行版內執行（uname -r 未含 microsoft/WSL）" 3

DISTRO_ID="$(wsld_detect_distro_id || true)"
FAMILY="$(wsld_distro_family || true)"
case "$FAMILY" in
  debian|arch|suse) ;;
  *) die "目前僅支援 Ubuntu/Debian、Arch 或 openSUSE 系發行版（偵測到: ${DISTRO_ID:-unknown}，family=${FAMILY:-unknown}）" 5 ;;
esac

id "$WSLD_USER" >/dev/null 2>&1 || die "使用者不存在: $WSLD_USER" 2
log "distro=$DISTRO_ID family=$FAMILY user=$WSLD_USER role=${WSLD_ROLE:-standalone} start-port=$WSLD_PORT compose=$WSLD_INSTALL_COMPOSE dry-run=$WSLD_DRY_RUN"

# ---------------------------------------------------------------- 1. wsl.conf systemd
# client 只裝 docker CLI 與曝露工具，不跑 daemon，不需要 systemd：新建的 distro 不必為此 wsl --shutdown 重開。
NEED_WSL_RESTART=0
if [ "$WSLD_ROLE" != "client" ] && ! wsld_wsl_conf_has_systemd /etc/wsl.conf; then
  log "啟用 /etc/wsl.conf [boot] systemd=true"
  if [ "$WSLD_DRY_RUN" = "1" ]; then
    printf '[dry-run] ensure systemd=true in /etc/wsl.conf\n'
  else
    tmp="$(mktemp)"
    [ -f /etc/wsl.conf ] && cat /etc/wsl.conf > "$tmp"
    wsld_ensure_wsl_conf_systemd "$tmp"
    $SUDO install -m 0644 "$tmp" /etc/wsl.conf
    rm -f "$tmp"
  fi
  NEED_WSL_RESTART=1
fi

# engine：不把 Windows PATH 灌進專用 distro（只影響下次啟動；interop 保留，埠占用偵測仍需 netstat.exe）
if [ "$WSLD_ROLE" = "engine" ]; then
  if [ "$WSLD_DRY_RUN" = "1" ]; then
    printf '[dry-run] ensure [interop] appendWindowsPath=false in /etc/wsl.conf\n'
  else
    tmp="$(mktemp)"
    [ -f /etc/wsl.conf ] && cat /etc/wsl.conf > "$tmp"
    wsld_ensure_ini_key "$tmp" interop appendWindowsPath false
    $SUDO install -m 0644 "$tmp" /etc/wsl.conf
    rm -f "$tmp"
  fi
fi

if [ "$WSLD_ROLE" != "client" ] && { [ "$NEED_WSL_RESTART" = "1" ] || ! [ -d /run/systemd/system ]; }; then
  warn "systemd 尚未在此 session 生效。請在 Windows 執行：wsl --shutdown，重新開啟此發行版後再跑一次本腳本。"
  [ "$WSLD_DRY_RUN" = "1" ] || exit 3
fi

# ---------------------------------------------------------------- 2. 安裝 Docker
install_docker_debian() {
  export DEBIAN_FRONTEND=noninteractive
  log "安裝 Docker CE（官方 apt 來源）"
  run $SUDO apt-get update -y
  run $SUDO apt-get install -y ca-certificates curl gnupg
  run $SUDO install -m 0755 -d /etc/apt/keyrings
  read -r repo_id codename <<< "$(wsld_docker_apt_repo)"
  log "Docker apt 來源：linux/${repo_id} ${codename}"
  if [ "$WSLD_DRY_RUN" = "1" ]; then
    printf '[dry-run] fetch https://download.docker.com/linux/%s/gpg -> /etc/apt/keyrings/docker.asc\n' "$repo_id"
    printf '[dry-run] write /etc/apt/sources.list.d/docker.list\n'
  else
    $SUDO curl -fsSL "https://download.docker.com/linux/${repo_id}/gpg" -o /etc/apt/keyrings/docker.asc
    $SUDO chmod a+r /etc/apt/keyrings/docker.asc
    arch="$(dpkg --print-architecture)"
    echo "deb [arch=${arch} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${repo_id} ${codename} stable" \
      | $SUDO tee /etc/apt/sources.list.d/docker.list >/dev/null
  fi
  run $SUDO apt-get update -y
  if [ "$WSLD_ROLE" = "client" ]; then
    read -r -a pkgs <<< "$(wsld_client_packages debian "$WSLD_INSTALL_COMPOSE")"
  else
    pkgs=(docker-ce docker-ce-cli containerd.io docker-buildx-plugin)
    [ "$WSLD_INSTALL_COMPOSE" = "1" ] && pkgs+=(docker-compose-plugin)
  fi
  run $SUDO apt-get install -y "${pkgs[@]}"
}

install_docker_arch() {
  log "安裝 Docker（pacman 官方套件）"
  pkgs=(docker)
  [ "$WSLD_INSTALL_COMPOSE" = "1" ] && pkgs+=(docker-compose)
  command -v docker-buildx >/dev/null 2>&1 || pkgs+=(docker-buildx)
  run $SUDO pacman -Sy --noconfirm --needed "${pkgs[@]}"
}

install_docker_suse() {
  log "安裝 Docker（zypper 官方套件）"
  pkgs=(docker docker-buildx)
  [ "$WSLD_INSTALL_COMPOSE" = "1" ] && pkgs+=(docker-compose)
  run $SUDO zypper --non-interactive --gpg-auto-import-keys install --no-recommends "${pkgs[@]}"
}

ensure_compose_debian() {
  if [ "$WSLD_INSTALL_COMPOSE" = "1" ] && ! docker compose version >/dev/null 2>&1; then
    log "補安裝 docker-compose-plugin"
    run $SUDO apt-get update -y
    run $SUDO apt-get install -y docker-compose-plugin
  fi
}

ensure_compose_arch() {
  if [ "$WSLD_INSTALL_COMPOSE" = "1" ] && ! docker compose version >/dev/null 2>&1; then
    log "補安裝 docker-compose"
    run $SUDO pacman -Sy --noconfirm --needed docker-compose
  fi
}

ensure_compose_suse() {
  if [ "$WSLD_INSTALL_COMPOSE" = "1" ] && ! docker compose version >/dev/null 2>&1; then
    log "補安裝 docker-compose"
    run $SUDO zypper --non-interactive --gpg-auto-import-keys install --no-recommends docker-compose
  fi
}

if [ "$WSLD_ROLE" = "client" ]; then
  if command -v docker >/dev/null 2>&1; then
    log "docker CLI 已安裝，略過安裝"
  else
    case "$FAMILY" in
      debian) install_docker_debian ;;
      arch|suse)
        read -r -a pkgs <<< "$(wsld_client_packages "$FAMILY" "$WSLD_INSTALL_COMPOSE")"
        if [ "$FAMILY" = "arch" ]; then run $SUDO pacman -Sy --noconfirm --needed "${pkgs[@]}"
        else run $SUDO zypper --non-interactive --gpg-auto-import-keys install --no-recommends "${pkgs[@]}"; fi
        ;;
    esac
  fi
elif ! wsld_docker_engine_installed; then
  case "$FAMILY" in
    debian) install_docker_debian ;;
    arch) install_docker_arch ;;
    suse) install_docker_suse ;;
  esac
else
  log "Docker 引擎已安裝，略過安裝"
  case "$FAMILY" in
    debian) ensure_compose_debian ;;
    arch) ensure_compose_arch ;;
    suse) ensure_compose_suse ;;
  esac
fi

# ---------------------------------------------------------------- 2b. client：只建立 context
if [ "$WSLD_ROLE" = "client" ]; then
  if command -v dockerd >/dev/null 2>&1 && systemctl is-enabled --quiet docker </dev/null 2>/dev/null; then
    warn "此 distro 仍有本機 dockerd（docker.service 已啟用）；為避免兩個引擎並存，請遷移資料後執行：sudo systemctl disable --now docker docker.socket"
  fi
  if [ "$WSLD_DRY_RUN" = "1" ]; then
    printf '[dry-run] docker context create wsld --docker host=unix://%s (user %s)\n' "$WSLD_ENGINE_SOCK" "$WSLD_USER"
    printf '[dry-run] install /usr/local/sbin/wsld-expose, /etc/sudoers.d/wsld-expose%s\n' "$([ "$WSLD_INSTALL_SHIM" = 1 ] && echo ', /usr/local/bin/docker (shim)')"
    log "dry-run 完成"
    exit 0
  fi
  user_home="$(getent passwd "$WSLD_USER" | cut -d: -f6)"
  # lifecycle 以 root 執行：用 runuser 切到目標使用者，HOME 指向其家目錄（context 存在 ~/.docker）
  as_user() { runuser -u "$WSLD_USER" -- env HOME="$user_home" "$@" </dev/null; }
  if ! as_user docker context inspect wsld >/dev/null 2>&1; then
    as_user docker context create wsld --docker "host=unix://$WSLD_ENGINE_SOCK" >/dev/null
  fi
  as_user docker context use wsld >/dev/null
  [ -S "$WSLD_ENGINE_SOCK" ] || warn "engine socket 尚未出現（$WSLD_ENGINE_SOCK）；請先啟動 engine distro"
  # 曝露工具：讓 engine 以相同路徑存取本 distro 的檔案（bind mount 用），以及自動切換路徑的 docker shim
  if [ -z "${WSL_DISTRO_NAME:-}" ] || ! wsld_valid_distro_name "$WSL_DISTRO_NAME"; then
    warn "取不到合法的 WSL_DISTRO_NAME，略過曝露工具與 docker shim（bind mount 本機路徑將無法使用）"
  elif [ ! -x /usr/bin/docker ]; then
    warn "找不到 /usr/bin/docker，略過 docker shim"
  else
    wsld_render_expose_script "$WSL_DISTRO_NAME" | $SUDO tee /usr/local/sbin/wsld-expose >/dev/null
    $SUDO chmod 0755 /usr/local/sbin/wsld-expose
    sudoers_tmp="$(mktemp)"
    wsld_render_sudoers "$WSLD_USER" > "$sudoers_tmp" || die "無法產生 sudoers（使用者 $WSLD_USER）" 1
    if command -v visudo >/dev/null 2>&1 && ! visudo -cf "$sudoers_tmp" >/dev/null 2>&1; then
      rm -f "$sudoers_tmp"; die "產生的 sudoers 未通過 visudo 檢查" 1
    fi
    $SUDO install -d -m 0750 /etc/sudoers.d
    $SUDO install -m 0440 "$sudoers_tmp" /etc/sudoers.d/wsld-expose
    rm -f "$sudoers_tmp"
    if [ "$WSLD_INSTALL_SHIM" = "1" ]; then
      wsld_render_shim "$WSL_DISTRO_NAME" /usr/bin/docker | $SUDO tee /usr/local/bin/docker >/dev/null
      $SUDO chmod 0755 /usr/local/bin/docker
      log "已安裝 docker shim（/usr/local/bin/docker）；WSLD_NO_SHIM=1 可暫時關閉"
    fi
    log "已安裝曝露工具 /usr/local/sbin/wsld-expose"
  fi
  log "完成。docker context 'wsld' 已啟用，指向 $WSLD_ENGINE_SOCK"
  exit 0
fi

# ---------------------------------------------------------------- 3. docker 群組
if ! getent group docker >/dev/null 2>&1; then
  log "建立 docker 群組"
  run $SUDO groupadd --system docker
fi
if id -nG "$WSLD_USER" | tr ' ' '\n' | grep -qx docker; then
  log "$WSLD_USER 已在 docker 群組"
else
  log "將 $WSLD_USER 加入 docker 群組（需重新登入 shell 才生效）"
  run $SUDO usermod -aG docker "$WSLD_USER"
fi

# ---------------------------------------------------------------- 4. 選埠 + daemon.json
CURRENT_PORT=""
if [ -r /etc/docker/daemon.json ]; then
  CURRENT_PORT="$(grep -oE 'tcp://127\.0\.0\.1:[0-9]+' /etc/docker/daemon.json | head -1 | sed 's/.*://')" || true
fi

if [ -n "$CURRENT_PORT" ] && systemctl is-active --quiet docker </dev/null 2>/dev/null && ! wsld_windows_port_in_use "$CURRENT_PORT"; then
  PORT="$CURRENT_PORT"
  log "dockerd 已在埠 $PORT 監聽，沿用"
else
  start="$WSLD_PORT"
  if [ -n "$CURRENT_PORT" ] && ! wsld_port_in_use "$CURRENT_PORT"; then
    start="$CURRENT_PORT"
  fi
  PORT="$(wsld_pick_free_port "$start")" || die "找不到可用埠（起始 $start）" 4
  if [ -n "$CURRENT_PORT" ] && [ "$PORT" != "$CURRENT_PORT" ]; then
    warn "既有埠 $CURRENT_PORT 不可用，改用 $PORT"
  elif [ "$PORT" != "$WSLD_PORT" ]; then
    warn "埠 $WSLD_PORT 已被占用，改用 $PORT"
  fi
fi

SHARED_SOCK=""
[ "$WSLD_ROLE" = "engine" ] && SHARED_SOCK="$WSLD_ENGINE_SOCK"
DAEMON_JSON="$(wsld_render_daemon_json "$PORT" "$SHARED_SOCK")"
wsld_validate_loopback_hosts "$DAEMON_JSON" || die "daemon.json 含非 loopback host，拒絕寫入" 1

if [ "$WSLD_DRY_RUN" = "1" ]; then
  printf '[dry-run] write /etc/docker/daemon.json:\n%s\n' "$DAEMON_JSON"
  printf '[dry-run] write /etc/systemd/system/docker.service.d/wsld-override.conf\n'
else
  $SUDO install -d -m 0755 /etc/docker /etc/systemd/system/docker.service.d
  if [ -f /etc/docker/daemon.json ] && ! grep -q '"hosts"' /etc/docker/daemon.json && [ "$(tr -d '[:space:]' < /etc/docker/daemon.json)" != "{}" ]; then
    warn "既有 /etc/docker/daemon.json 含其他設定且沒有 hosts；備份為 daemon.json.wsld.bak 後覆寫（請手動合併）"
    $SUDO cp /etc/docker/daemon.json /etc/docker/daemon.json.wsld.bak
  fi
  printf '%s\n' "$DAEMON_JSON" | $SUDO tee /etc/docker/daemon.json >/dev/null
  if [ "$WSLD_ROLE" = "engine" ]; then
    wsld_render_engine_override "$FAMILY" "$WSLD_ENGINE_SOCK"
  else
    wsld_render_systemd_override "$FAMILY"
  fi | $SUDO tee /etc/systemd/system/docker.service.d/wsld-override.conf >/dev/null
fi

# ---------------------------------------------------------------- 5. 啟動服務 + linger
run $SUDO systemctl daemon-reload
run $SUDO systemctl enable docker
run $SUDO systemctl restart docker
run $SUDO loginctl enable-linger "$WSLD_USER"

# ---------------------------------------------------------------- 6. 驗證
if [ "$WSLD_DRY_RUN" = "1" ]; then
  log "dry-run 完成"
  printf 'WSLD_API_PORT=%s\nWSLD_API_ENDPOINT=tcp://127.0.0.1:%s\n' "$PORT" "$PORT"
  exit 0
fi

for _ in $(seq 1 20); do
  $SUDO docker info >/dev/null 2>&1 && break
  sleep 0.5
done
$SUDO docker info >/dev/null 2>&1 || die "docker info 失敗；請查看: journalctl -u docker -n 50" 4

if command -v curl >/dev/null 2>&1; then
  ping="$(curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/_ping" || true)"
  [ "$ping" = "OK" ] || die "API http://127.0.0.1:${PORT}/_ping 未回 OK（得到: '${ping}'）" 4
fi

if ss -Hltn 2>/dev/null | awk '{print $4}' | grep -E ":${PORT}\$" | grep -vqE '^(127\.0\.0\.1|\[::1\])'; then
  die "dockerd 在埠 ${PORT} 綁到了非 loopback 位址，請檢查 daemon.json" 1
fi

if [ "$WSLD_ROLE" = "engine" ] && ! [ -S "$WSLD_ENGINE_SOCK" ]; then
  die "共用 socket 未建立: $WSLD_ENGINE_SOCK（journalctl -u docker -n 50）" 4
fi

if [ "$WSLD_INSTALL_COMPOSE" = "1" ]; then
  $SUDO docker compose version >/dev/null 2>&1 || warn "docker compose plugin 不可用"
fi

log "完成。Windows 端可用 http://127.0.0.1:${PORT}（WSL2 localhost 轉發）"
log "若剛加入 docker 群組，請重新開啟 WSL shell 後再以一般使用者執行 docker。"
printf 'WSLD_API_PORT=%s\nWSLD_API_ENDPOINT=tcp://127.0.0.1:%s\n' "$PORT" "$PORT"
