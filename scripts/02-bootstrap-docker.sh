#!/usr/bin/env bash
# wsld Phase 1：在 WSL2（Ubuntu/Debian 或 Arch）內安裝 rootful Docker，
# 讓 dockerd 同時監聽 unix socket 與 tcp://127.0.0.1:<port>（只綁 loopback），
# 並啟用 systemd／linger，供 Windows 端 wsld / wsld-gui 使用。
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
    "$@"
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
  debian|arch) ;;
  *) die "目前僅支援 Ubuntu/Debian 或 Arch 系發行版（偵測到: ${DISTRO_ID:-unknown}）" 3 ;;
esac

id "$WSLD_USER" >/dev/null 2>&1 || die "使用者不存在: $WSLD_USER" 2
log "distro=$DISTRO_ID family=$FAMILY user=$WSLD_USER start-port=$WSLD_PORT compose=$WSLD_INSTALL_COMPOSE dry-run=$WSLD_DRY_RUN"

# ---------------------------------------------------------------- 1. wsl.conf systemd
NEED_WSL_RESTART=0
if ! wsld_wsl_conf_has_systemd /etc/wsl.conf; then
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

if [ "$NEED_WSL_RESTART" = "1" ] || ! [ -d /run/systemd/system ]; then
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
  if [ "$WSLD_DRY_RUN" = "1" ]; then
    printf '[dry-run] fetch https://download.docker.com/linux/%s/gpg -> /etc/apt/keyrings/docker.asc\n' "$DISTRO_ID"
    printf '[dry-run] write /etc/apt/sources.list.d/docker.list\n'
  else
    $SUDO curl -fsSL "https://download.docker.com/linux/${DISTRO_ID}/gpg" -o /etc/apt/keyrings/docker.asc
    $SUDO chmod a+r /etc/apt/keyrings/docker.asc
    codename="$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")"
    arch="$(dpkg --print-architecture)"
    echo "deb [arch=${arch} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${DISTRO_ID} ${codename} stable" \
      | $SUDO tee /etc/apt/sources.list.d/docker.list >/dev/null
  fi
  run $SUDO apt-get update -y
  pkgs=(docker-ce docker-ce-cli containerd.io docker-buildx-plugin)
  [ "$WSLD_INSTALL_COMPOSE" = "1" ] && pkgs+=(docker-compose-plugin)
  run $SUDO apt-get install -y "${pkgs[@]}"
}

install_docker_arch() {
  log "安裝 Docker（pacman 官方套件）"
  pkgs=(docker)
  [ "$WSLD_INSTALL_COMPOSE" = "1" ] && pkgs+=(docker-compose)
  command -v docker-buildx >/dev/null 2>&1 || pkgs+=(docker-buildx)
  run $SUDO pacman -Sy --noconfirm --needed "${pkgs[@]}"
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

if ! wsld_docker_engine_installed; then
  case "$FAMILY" in
    debian) install_docker_debian ;;
    arch) install_docker_arch ;;
  esac
else
  log "Docker 引擎已安裝，略過安裝"
  case "$FAMILY" in
    debian) ensure_compose_debian ;;
    arch) ensure_compose_arch ;;
  esac
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

if [ -n "$CURRENT_PORT" ] && [ "$CURRENT_PORT" = "$WSLD_PORT" ]; then
  PORT="$CURRENT_PORT"
  log "沿用既有 daemon.json 埠 $PORT"
elif [ -n "$CURRENT_PORT" ] && systemctl is-active --quiet docker 2>/dev/null; then
  PORT="$CURRENT_PORT"
  log "dockerd 已在埠 $PORT 監聽，沿用"
else
  PORT="$(wsld_pick_free_port "$WSLD_PORT")" || die "找不到可用埠（起始 $WSLD_PORT）" 4
  [ "$PORT" = "$WSLD_PORT" ] || warn "埠 $WSLD_PORT 已被占用，改用 $PORT"
fi

DAEMON_JSON="$(wsld_render_daemon_json "$PORT")"
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
  wsld_render_systemd_override | $SUDO tee /etc/systemd/system/docker.service.d/wsld-override.conf >/dev/null
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

if [ "$WSLD_INSTALL_COMPOSE" = "1" ]; then
  $SUDO docker compose version >/dev/null 2>&1 || warn "docker compose plugin 不可用"
fi

log "完成。Windows 端可用 http://127.0.0.1:${PORT}（WSL2 localhost 轉發）"
log "若剛加入 docker 群組，請重新開啟 WSL shell 後再以一般使用者執行 docker。"
printf 'WSLD_API_PORT=%s\nWSLD_API_ENDPOINT=tcp://127.0.0.1:%s\n' "$PORT" "$PORT"
