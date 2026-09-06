#!/usr/bin/env bash
# frontend/npm-test.sh — 確保 Playwright libs 後轉呼叫 playwright test
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck disable=SC1091
. "$ROOT/scripts/ensure-playwright-libs.sh"
cd "$ROOT/frontend"
exec npx playwright test "$@"
