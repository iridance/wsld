# GUI（wsld-gui）

應用程式名稱一律顯示 **wsld**（視窗標題、Tray、版本資源）；全名 *Windows Subsystem Linux with Docker* 僅在文件中說明。

對應規格：[SPEC.md](SPEC.md) §5.5。Wails v2（WebView2）+ Vite/TypeScript 前端 + `fyne.io/systray`。

## 架構

```
┌─ wsld-gui.exe ───────────────────────────────────────────────┐
│ Wails 視窗（WebView2） ──HTTP/WS──▶ internal/gui.Server         │
│ systray（專屬 OS 執行緒）             │  Bearer token、僅 127.0.0.1  │
│                                      ▼                        │
│                       internal/{lifecycle,dockerapi,config,…} │
└──────────────────────────────────────────────────────────────┘
                 ▲ 同一套前端、同一個 API
┌─ wsld-gui-testhost（Linux/CI）────────┐
│ 服務 frontend/dist + FakeEngine       │◀── Playwright
│ /__test/* 控制情境                    │
└──────────────────────────────────────┘
```

- 前端**不**用 Wails binding，只透過 `internal/gui` 的 HTTP + WebSocket 橋接（`/api/*`）。這讓 Wails 應用與 Playwright 測試主機走**完全相同**的路徑，UI 邏輯零分歧。
- 橋接只監聽 loopback 隨機埠，每次啟動產生新 token；前端由 Wails asset middleware 注入 `/wsld-config.js` 取得 base URL 與 token。
- Go core 與 CLI 共用（`internal/*`），GUI 不重複實作任何 Docker／WSL 邏輯。

## 版面

```
┌ header ─ <distro> [Connected] tcp://127.0.0.1:23750 ↻3s ······ [⏻ Start engine] [⚙ Settings] ┐
│ banner（錯誤，紅）／notice（動作結果，綠）                                                    │
├──────────┬─────────────────────────────────────────────────────────────────────────────────┤
│ side nav │ main（目前視圖）                                                                  │
│ 大圖示+字 │  Containers：列表 + 下方 Logs／Terminal 面板                                       │
│ Containers│  Images／Volumes／Networks／Machines：工具列 + 固定版面表格                           │
│ Images   │                                                                                 │
│ Volumes  │                                                                                 │
│ Networks │                                                                                 │
│ Machines │                                                                                 │
├──────────┴─────────────────────────────────────────────────────────────────────────────────┤
│ footer ─ v1.0.0 ·································································· English ▾ │
└────────────────────────────────────────────────────────────────────────────────────────────┘
```

- **Header**：連線中的 WSL distro 名稱（無 config 時顯示 wsld）、連線徽章、API endpoint、刷新間隔（失焦時顯示「已暫停刷新」）、啟動引擎、設定。
- **Side nav**（`nav-<view>`）：五個視圖；目前視圖以 `aria-current="page"` 標示，選擇存 localStorage。切換視圖會關閉開著的 Logs／Terminal 面板。
- **Footer**：左為版本、右為語言下拉（無邊框，僅以顏色／游標／hover 底線表示可點）。
- 已移除「顯示已停止」勾選（列表固定顯示全部容器）與手動「刷新」鈕（只靠自動刷新）。

## 功能對照（SPEC §5.5）

| 區塊 | 行為 | Playwright 覆蓋 |
|---|---|---|
| Tray | 狀態列（5s ping）、啟動引擎、開啟主視窗、結束；按 X 只縮到 Tray | 實機手測（見下） |
| Header | distro 名稱 + 連線徽章 + endpoint + 刷新間隔；啟動引擎、設定 | `list.spec.ts` |
| Side nav | 大型 icon + 文字，五視圖切換、active 標示、貼齊 header／footer 之間 | `shell.spec.ts` |
| 按鈕圖示 | 所有按鈕帶內嵌 SVG 語義圖示（`frontend/src/icons.ts`；`aria-hidden`）；列表操作鈕為**純圖示**（無文字、無邊框），名稱由 `aria-label`／`title` 提供 | `list.spec.ts`、`groups.spec.ts` |
| Containers | 名稱／映像／狀態／埠（IPv4/IPv6 去重）／操作（▶ ■ ≡ >_）；固定顯示 running + stopped；前景 3s 刷新，失焦或隱藏時暫停 | `list.spec.ts` |
| 欄寬 | `table-layout: fixed`：內容變動不改變欄寬（省略號截斷）；標頭右緣可拖曳調整，寬度存 localStorage（每個視圖各自一組） | `columns.spec.ts` |
| Compose 分組 | 依 `com.docker.compose.project` 分組：專案標頭（running/total、折疊、整組 Start／Stop）、組內顯示 service 名；未分組容器放在標頭「-」下（只有計數，沒有整組操作）；無 compose 容器時維持平面列表 | `groups.spec.ts` |
| Start／Stop | 呼叫 `/api/containers/{id}/start|stop`，列狀態即時顯示 Starting…／Stopping… | `list.spec.ts` |
| Logs | 預設 tail 100；「即時」勾選走 WebSocket 串流 | `panels.spec.ts` |
| Exec 終端 | xterm.js ↔ `/api/containers/{id}/exec`（WS、hijack）；shell 依 `exec_shell` 或 bash→sh | `panels.spec.ts` |
| Images | repo:tag（dangling 顯示 `<none>`）、短 ID、大小、建立時間、使用中容器數；🗑 移除（確認對話框；使用中 → 409 顯示於 banner）；「清除未使用」= `docker image prune -a`，完成後 notice 顯示數量與回收空間 | `views.spec.ts` |
| Volumes | 名稱、驅動、掛載點、建立時間；🗑 移除（使用中 → 409）；「清除未使用」= `docker volume prune -a`（含具名 volume，確認文字提醒資料會遺失） | `views.spec.ts` |
| Networks | 名稱、驅動、範圍、短 ID、建立時間；內建 bridge／host／none 標示 `data-builtin`；**唯讀**（無刪除） | `views.spec.ts` |
| Machines | 本機 WSL 發行版（`wsl -l -v`）：名稱、狀態、WSL 版本、角色標籤（`wsld` = config 的 distro、`default`）；ⓘ 顯示 `/etc/wsl.conf` 原文與 systemd 判定；「新增發行版」列出 `wsl --list --online` 並以 `wsl --install -d <name> --no-launch` 安裝，成功後提示先 `wsl -d <name>` 建使用者再 `wsld init`。此視圖不依賴 Docker 引擎 | `views.spec.ts` |
| Footer | 版本（左）、語言下拉（右，無邊框） | `list.spec.ts`、`shell.spec.ts` |
| 設定 | 刷新間隔、終端 shell、登入時啟動 Tray（HKCU Run，預設關）；錯誤顯示於 banner | `settings.spec.ts` |
| i18n | 預設英文；右下角下拉切換 en／繁體中文 → `PUT /api/settings {lang}` 寫入 `config.toml`，後端訊息與 Tray 選單同步切換；`WSLD_LANG` 環境變數可強制覆寫 | `list.spec.ts`、Go `TestSettingsLangSwitch…` |
| 尚無 config | 主視窗可開，顯示「請先執行 wsld init」，啟動引擎回傳 not_ready | Go 單測（`configGate`） |

### 破壞性操作的規則

- 移除／清除一律先開確認對話框（`confirm-dialog`），取消不動作。
- 後端不自動 `force`：使用中的映像／volume 由 Docker 回 409，橋接轉成 `code: "conflict"`，前端顯示在 banner，讓使用者自行停容器再刪。
- 成功結果顯示在綠色 notice（8 秒後自動消失）。

## 橋接 API（`internal/gui`）

| 端點 | 說明 |
|---|---|
| `GET /api/meta`／`GET /api/status`／`POST /api/engine/start` | 版本、語系訊息、設定；引擎狀態；啟動引擎 |
| `GET /api/containers?all=1`、`POST /api/containers/{id}/start|stop`、`GET …/logs[/stream]`、`GET …/exec` | 容器 |
| `GET /api/images`、`DELETE /api/images/{ref}[?force=1]`、`POST /api/images/prune` | 映像（prune = `dangling=false`，即所有未使用） |
| `GET /api/volumes`、`DELETE /api/volumes/{name}[?force=1]`、`POST /api/volumes/prune` | volume（prune = `all=true`） |
| `GET /api/networks` | 網路（唯讀） |
| `GET /api/machines`、`GET /api/machines/{name}/config`、`GET /api/machines/online`、`POST /api/machines/install {name}` | WSL 發行版；沒有 `wsl.exe`（testhost 未注入）時 `machines.available=false` |
| `GET|PUT /api/settings`、`GET /api/doctor`、`POST /api/window/{hide|show|quit}` | 設定、診斷、視窗 |

錯誤格式 `{"error","code"}`，`code` ∈ `not_ready`(503)／`api`(502)／`not_found`(404)／`conflict`(409)／`usage`(400)／`error`。

## 啟動參數

| 參數 | 說明 |
|---|---|
| `--tray` | 隱藏啟動，只顯示系統匣圖示（登入自啟採用此參數） |

## 建置

```bash
make build-frontend   # frontend/dist（tsc + vite）
make build-gui        # dist/wsld-gui.exe（GOOS=windows，-tags desktop,production，-H windowsgui）
make build-win        # 上述 + dist/wsld.exe
```

WebView2 執行期：Windows 11 內建；缺少時 Wails 會提示下載。

## 測試

```bash
make test-ui          # 建前端 → Playwright（自動啟動 wsld-gui-testhost）
cd frontend && npx playwright test --ui   # 互動除錯
```

Playwright 在 WSL 需要 Chromium 相依套件；本機若無 sudo，可將 `libnss3` `libnspr4` `libasound2t64` 解到 `~/.local/lib/pw` 並以 `LD_LIBRARY_PATH` 帶入（詳見 [TESTING.md](TESTING.md)）。

### 測試主機控制端點（僅 testhost）

| 端點 | 用途 |
|---|---|
| `POST /__test/reset` | 還原 fake 狀態（web running、db exited） |
| `POST /__test/engine {"down":bool}` | 模擬 API 不可達 |
| `POST /__test/lifecycle {...}` | 設定 `Status()`／`Start()` 回應 |
| `POST /__test/logs {"name","lines":[]}` | 推送即時日誌 |
| `POST /__test/lang {"lang":"zh-TW"}` | 切換語系 |
| `POST /__test/container?name=&image=&state=` | 新增容器；帶 `project=&service=` 則為 compose 容器 |
| `POST /__test/action-delay?ms=` | 讓 Start／Stop 延遲回應（測 pending 回饋） |
| `POST /__test/image?tag=&size=&containers=` | 新增映像（`containers>0` 視為使用中） |
| `POST /__test/volume?name=&in_use=1` | 新增 volume |
| `POST /__test/network?name=&driver=` | 新增網路 |
| `POST /__test/wsl/fail-install[?on=0]`、`GET /__test/wsl/installed` | 讓 FakeWSL 的 `--install` 失敗；查詢已安裝清單 |

FakeEngine 預設資料：容器 web（running）／db（exited）；映像 nginx:1.27（使用中）／redis:7／一個 dangling；volume pgdata（使用中）／orphan；網路 bridge／host／none／shop_default。FakeWSL 預設：Ubuntu-26.04（Running、預設、systemd）與 docker-desktop（Stopped）。

### 實機（Windows）手測項目

Playwright 無法涵蓋 Wails／systray 原生行為，v1 以下列步驟在 Windows 驗證（已於 Ubuntu-26.04 + WebView2 實測）：

1. `dist\wsld-gui.exe` 開啟後標題列「已連線」，列表顯示 `docker run` 的容器；側欄切到 Images／Volumes／Networks 顯示真實資源，Machines 顯示 `wsl -l -v` 的發行版並可開 wsl.conf。
2. 按 X → 視窗隱藏、程序仍在（Tray 圖示保留）；Tray「開啟主視窗」可還原。
3. `dist\wsld-gui.exe --tray` → 不顯示視窗，只有 Tray。
4. Tray「結束」→ 程序退出。
5. 設定 → 勾「登入時啟動 Tray」→ `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\wsld` 出現；取消勾即刪除。

## 圖示與 Windows 資源

- `cmd/wsld-gui/assets/gen_icon.go`（`go run`）產生 `tray.ico`（16/24/32/48/256）與 `icon.png`；Tray 與視窗共用同一圖示。
- `cmd/wsld-gui/winres/winres.json` 以 [go-winres](https://github.com/tc-hib/go-winres) 產生 `rsrc_windows_amd64.syso`（圖示群組 ID 3 = Wails 的 `AppIconID`、Per-Monitor v2 DPI manifest、Common Controls v6、版本資源）。`make winres` 重新產生；`.syso` 已入版控，`go build` 會自動連結。

## 已知限制（v1）

- 深色主題跟隨系統（`windows.SystemDefault`），但前端配色僅淺色。
- Images 的「使用中」欄依 Docker `/images/json` 的 `Containers`；舊版 daemon 可能回 -1，顯示為「–」。
- Machines 的「新增發行版」只執行 `wsl --install --no-launch`：首次進入 distro 建立使用者、以及 Docker bootstrap（`wsld init`）仍需使用者操作；GUI 不會自動切換 `config.toml` 的 distro。
- Networks 為唯讀；建立／刪除網路請用 `docker network`。
