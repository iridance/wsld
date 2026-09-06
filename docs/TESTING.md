# 測試（TESTING）

一鍵：

```bash
make test          # scripts + Go + UI
make test-go       # 只跑 Go（含 windows 交叉 vet）
make test-scripts  # bats + shellcheck + Pester
make test-ui       # 建前端 + Playwright（見 docs/GUI.md）
```

前置：Go 1.22+（本機用 `~/.local/go`）、bats-core、shellcheck；Pester 用 Windows PowerShell 5.1 內建版本（透過 WSL interop 呼叫 `powershell.exe`）；Node 20+ 與 `npx playwright install chromium`。

Playwright 的 Chromium 在無 sudo 的 WSL 可能缺 `libnss3` `libnspr4` `libasound2t64`。`make test-ui`／`cd frontend && npm test` 會自動跑 `scripts/ensure-playwright-libs.sh`：以 `apt-get download` + `dpkg-deb -x` 解到 `~/.local/lib/pw` 並設定 `LD_LIBRARY_PATH`。有 sudo 時也可改跑 `npx playwright install-deps chromium`。

## 層級

| 層 | 工具 | 範圍 | 說明 |
|---|---|---|---|
| Bootstrap 腳本 | bats、shellcheck、Pester | `scripts/lib/*`、`scripts/tests/*` | 純函式：埠選擇、daemon.json、wsl.conf、參數、`wsl -l -v` 解析 |
| Go core | `go test ./...` | `internal/{config,dockerapi,wsl,lifecycle,doctor,i18n,cli,gui}`、`scripts`（embed bundle） | 全部以 fake／httptest，不碰真 WSL 或 Docker |
| GUI 橋接 | `go test ./internal/gui` | `/api/*` HTTP + WebSocket（token、CORS、logs 串流、exec、images／volumes／networks／machines） | `FakeEngine`／`FakeLifecycle`／`FakeWSL` |
| GUI 畫面 | Playwright | `frontend/tests/*.spec.ts`（`list`、`columns`、`groups`、`panels`、`settings`、`shell`、`views`、`info`） | `wsld-gui-testhost` 服務 `frontend/dist` + fake；見 `docs/GUI.md` |
| CLI／GUI 端到端 | 手動／CI Windows runner | `dist/wsld.exe`、`dist/wsld-gui.exe` | 見下方「實機驗收」與 `docs/GUI.md` |

## Go 測試設計

- **`wsl.Runner`**：所有 `wsl.exe` 呼叫走介面；測試以 `fakeRunner` 依 `args` 串接鍵回應（含 UTF-16LE 輸出）。
- **`dockerapi`**：`httptest.Server` 模擬 Engine API；exec 以 `http.Hijacker` 模擬 101 Upgrade。
- **`lifecycle.Manager`**：`Sleep`／`PingInterval` 可注入，避免重試等待。
- **`doctor.Deps`**：`Processes`（tasklist）與 `Dial`（埠探測）可注入。
- **`cli.App`**：`NewAPI`、`Exec` 注入；測試斷言退出碼 0／1／2／3／4 與輸出片段。
- **i18n**：訊息鍵集中在 `internal/i18n/messages.go`；測試保證每個鍵有英文；`WSLD_LANG=en|zh-TW` 可強制。

### 退出碼對應（SPEC §5.4）

| 碼 | 觸發 |
|---|---|
| 0 | 成功；`status`／`doctor` 全就緒 |
| 1 | 一般錯誤（如容器 404） |
| 2 | 未知命令、旗標錯誤、缺參數 |
| 3 | `config` 不存在、`lifecycle.ErrNotReady`、doctor engine 類 Fail |
| 4 | `lifecycle.ErrAPI`／`dockerapi.ErrUnreachable`、doctor 僅 api 類 Fail |

## 實機驗收（Windows）

```bash
make build-win                        # dist/wsld.exe + wsld-gui.exe（VERSION←git exact tag，預設 0.6.0）
./dist/wsld.exe init                  # 在 WSL Ubuntu 內安裝 Docker、寫 %APPDATA%\wsld\config.toml
curl.exe http://127.0.0.1:23750/_ping # OK
./dist/wsld.exe status && ./dist/wsld.exe doctor
DOCKER_HOST=tcp://127.0.0.1:23750 docker run -d --name t -v "$PWD:/app" alpine sleep 600
./dist/wsld.exe ps && ./dist/wsld.exe logs t && ./dist/wsld.exe stop-ct t && ./dist/wsld.exe start-ct t
```

2026-09-06 於 Ubuntu-26.04（WSL2）實測：Docker CE 29.8.0、Compose v5.5.1、埠 23750、doctor 11 OK。
同日 `wsld-gui.exe` 實測：WebView2 開窗、已連線、列出 `nginx:alpine` 容器（埠去重）、按 X 縮 Tray、`--tray` 隱藏啟動。

## 規範

- TDD：先寫失敗測試再實作（`.cursor/rules/tdd-and-ui-testing.mdc`）。
- 新增 `wsl.exe` 互動 → 先加 fake 回應；新增 API 端點 → 先加 httptest 案例。
- UI 改動必附 Playwright 測試；需要新情境時先擴充 `FakeEngine`／`/__test/*` 再寫 spec。
