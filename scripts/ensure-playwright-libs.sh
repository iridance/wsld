#!/usr/bin/env bash
# 無 sudo 的 WSL／Linux：為 Playwright Chromium 補齊系統 .so（libnss3 等）。
# 由 Makefile／npm test source；會 export LD_LIBRARY_PATH。
set -euo pipefail

DEST="${PLAYWRIGHT_LIBS:-$HOME/.local/lib/pw}"
TMP="${TMPDIR:-/tmp}/wsld-pw-debs-$$"
PKGS=(libnspr4 libnss3 libasound2t64 libasound2-data libsqlite3-0)

find_chrome() {
  ls -1d "$HOME"/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell 2>/dev/null | tail -n1 || true
}

chrome_missing_libs() {
  local chrome="$1"
  [[ -z "$chrome" || ! -x "$chrome" ]] && return 1
  ldd "$chrome" 2>/dev/null | grep -q 'not found'
}

install_libs() {
  mkdir -p "$DEST"
  mkdir -p "$TMP"
  # shellcheck disable=SC2064
  trap 'rm -rf "$TMP"' RETURN
  (
    cd "$TMP"
    apt-get download "${PKGS[@]}" >/dev/null
    for deb in ./*.deb; do
      dpkg-deb -x "$deb" root
    done
    find root -type f \( -name '*.so' -o -name '*.so.*' \) -exec cp -a {} "$DEST/" \;
  )
}

CHROME="$(find_chrome)"
export LD_LIBRARY_PATH="${DEST}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

need=0
if [[ ! -f "$DEST/libnspr4.so" ]]; then
  need=1
elif [[ -n "$CHROME" ]] && chrome_missing_libs "$CHROME"; then
  need=1
fi

if [[ "$need" -eq 1 ]]; then
  echo "Playwright Chromium 缺少系統函式庫；安裝到 $DEST …" >&2
  install_libs
  export LD_LIBRARY_PATH="${DEST}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
fi

if [[ -n "$CHROME" ]] && chrome_missing_libs "$CHROME"; then
  echo "仍有缺函式庫。請執行：cd frontend && npx playwright install-deps chromium（需 sudo）" >&2
  ldd "$CHROME" | grep 'not found' >&2 || true
  return 1 2>/dev/null || exit 1
fi
