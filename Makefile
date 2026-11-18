SHELL := /bin/bash
export PATH := $(HOME)/.local/bin:$(HOME)/.local/go/bin:$(PATH)

# 版號對齊 git tag（v0.5.1 → 注入 main.version → GUI 左下角 v0.5.1）；未在 exact tag 上時預設 0.5.1
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null | sed 's/^v//' | grep . || echo 0.8.1)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: test test-go test-scripts test-scripts-bash test-scripts-ps lint-scripts build-win build-gui build-frontend test-ui readme-shots fmt icon winres

test: test-scripts test-go test-ui

test-go:
	gofmt -l ./internal ./cmd ./scripts | tee /dev/stderr | test -z "$$(cat)"
	go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...
	go test ./...

fmt:
	gofmt -w ./internal ./cmd ./scripts

build-win: build-gui
	mkdir -p dist
	@echo "building wsld.exe VERSION=$(VERSION)"
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/wsld.exe ./cmd/wsld

# 前端（Vite）→ frontend/dist，供 go:embed 與 Playwright 使用
build-frontend:
	cd frontend && npm ci --no-audit --no-fund && npm run build

# Wails v2 GUI（Windows；WebView2 執行期由 Wails 自帶 bootstrapper 處理）
build-gui: build-frontend
	mkdir -p dist
	@echo "building wsld-gui.exe VERSION=$(VERSION)"
	GOOS=windows GOARCH=amd64 go build -trimpath -tags desktop,production \
	  -ldflags "$(LDFLAGS) -H windowsgui" -o dist/wsld-gui.exe ./cmd/wsld-gui

# 圖示（tray.ico／icon.png）與 Windows 資源（.syso：圖示、manifest、版本）
icon:
	go run ./cmd/wsld-gui/assets/gen_icon.go

winres: icon
	go run github.com/tc-hib/go-winres@latest make --in cmd/wsld-gui/winres/winres.json --out cmd/wsld-gui/rsrc --arch amd64

# Playwright UI 測試：以 wsld-gui-testhost（fake backend）驅動同一套前端
# WSL 無 sudo 時 scripts/ensure-playwright-libs.sh 會把 libnss3 等解到 ~/.local/lib/pw
test-ui: build-frontend
	@set -euo pipefail; \
	  . ./scripts/ensure-playwright-libs.sh; \
	  cd frontend && npm test

# README 截圖 → docs/images/*.png（testhost 假資料，版號取 frontend/package.json）
readme-shots: build-frontend
	@set -euo pipefail; \
	  . ./scripts/ensure-playwright-libs.sh; \
	  cd frontend && README_SHOTS=1 npx playwright test tests/readme-shots.spec.ts

test-scripts: lint-scripts test-scripts-bash test-scripts-ps

lint-scripts:
	shellcheck -x -P SCRIPTDIR scripts/02-bootstrap-docker.sh scripts/lib/bootstrap-lib.sh

test-scripts-bash:
	bats scripts/tests

# 透過 WSL interop 呼叫 Windows PowerShell 5.1 內建的 Pester 3.x
test-scripts-ps:
	@if command -v powershell.exe >/dev/null 2>&1; then \
	  win="$$(wslpath -w "$$PWD/scripts/tests")"; \
	  powershell.exe -NoProfile -ExecutionPolicy Bypass -Command \
	    "Import-Module Pester; \$$r = Invoke-Pester -Path '$$win' -PassThru -Quiet; \$$r.TestResult | Where-Object { -not \$$_.Passed } | ForEach-Object { 'FAIL: ' + \$$_.Describe + ' / ' + \$$_.Name + ' :: ' + \$$_.FailureMessage }; Write-Output ('Pester PASSED=' + \$$r.PassedCount + ' FAILED=' + \$$r.FailedCount); if (\$$r.FailedCount -gt 0) { exit 1 }" \
	    | tr -d '\r'; \
	else echo "skip Pester (powershell.exe not found)"; fi
