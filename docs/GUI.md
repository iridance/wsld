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
┌ header ─ <distro> [📁] [>_] [Connected] ······ [⏻ Start engine] ─┐
│ banner（錯誤，紅）／notice（動作結果，綠）                          │
├──────────┬──────────────────────────────────┬────────────────────┤
│ side nav │ main（目前視圖）                   │ info drawer（右）   │
│ Containers│  Containers：列表＋下方 Logs／Term │ 容器／compose／     │
│ Images   │  Images／Volumes／Networks／       │ machine 詳情       │
│ Volumes  │  Machines／Settings               │                    │
│ Networks │                                    │                    │
│ Machines │                                    │                    │
│ Settings │                                    │                    │
├──────────┴──────────────────────────────────┴────────────────────┤
│ footer ─ v… ··········································· English ▾ │
└──────────────────────────────────────────────────────────────────┘
```

- **Header**：active distro 名稱（無 config 時顯示 wsld）＋ **有 border** 的 Open folder／Open terminal、連線徽章、啟動引擎。**不顯示** API endpoint（tcp）與 Settings（分別在 Machines Info／側欄 Settings）。
- **Side nav**：Containers／Images／Volumes／Networks／Machines／**Settings**；`aria-current="page"`；存 localStorage。上下間距對齊左右間距。切換視圖關閉 Logs／Terminal／Info。
- **Info drawer（右）**：Containers／Compose／Machines 詳情（見下表）。
- **Footer**：左版本（`make build-*` 注入，對齊 git tag／預設 **0.5.2** → 顯示 `v0.5.2`）、右語言下拉（無邊框）。
- 列表固定顯示全部容器；無手動刷新鈕；列上 actions 無邊框純圖示；**列高／格線與其他欄對齊**。

## 功能對照（SPEC §5.5／Roadmap 0.5）

| 區塊 | 行為 | Playwright 覆蓋 |
|---|---|---|
| Tray | 狀態列、啟動引擎、開啟主視窗、結束；按 X 縮 Tray | 實機手測 |
| Header | distro＋有 border 的 folder／terminal＋連線徽章＋Start engine；無 Settings、無 tcp | `list`／`info`（隨 0.5 更新） |
| Side nav | 六視圖（含 Settings）；間距打磨 | `shell.spec.ts` |
| 按鈕圖示 | 列／compose：無邊框純圖示；**header icon：有 border** | `list`／`groups`／`info` |
| Containers | Name／Image／State／操作（Start／Stop／Logs／Term／Info／**Remove**）；右側 Info＝summary | `list`／`info` |
| Info（容器） | Name、ID、Image、State、Status、Created、Started、Command、Ports、Networks（+IP）、Mounts、Labels、Restart policy | `info.spec.ts` |
| Compose | 分組、折疊、整組 Start／Stop／Info 純圖示 | `groups`／`info` |
| Start／Stop／Remove | pending 回饋；Remove 可含 running（確認會停） | `list`（Remove 隨 0.5） |
| Logs／Exec | tail 100；Live；xterm exec | `panels.spec.ts` |
| Images／Volumes／Networks | 同既有；列高打磨 | `views.spec.ts` |
| Machines | 設為目前、folder、terminal（底部 panel、home）、**刪除發行版**（trash、打名稱確認＝unregister）、右側 Info、Add | `views`（隨 0.5） |
| Machines Info | `.wslconfig` memory／processors、`wsl.conf`、**tcp endpoint**、CPU／Mem 摘要；取代 ⓘ dialog | 隨 0.5 |
| Settings | 側欄：refresh、exec_shell、tray_autostart、Theme Light／Dark／System | `settings`（搬遷） |
| i18n | footer 語言下拉 | `list.spec.ts` |
| 多 distro | active 切換作用於全部功能；硬切面板 | 隨 0.5 |

### 破壞性操作的規則

- 移除／清除一律確認對話框；**刪除發行版**（對應 Add）需**輸入 distro 名稱**後才可確定（`wsl --unregister`）。
- 容器 Remove：running 可刪（會先停止）；映像／volume 使用中仍不自動 force（409 → banner）。
- 成功結果顯示在綠色 notice（8 秒後自動消失）。

## 橋接 API（`internal/gui`）

| 端點 | 說明 |
|---|---|
| `GET /api/meta`／`GET /api/status`／`POST /api/engine/start` | 版本、語系訊息、設定；引擎狀態；啟動引擎 |
| `GET /api/machines/{name}/exec` | Machines 底部 xterm（WSL `script` PTY → login bash／user home） |
| `POST /api/distro/{open-folder\|open-terminal}` | Header：開啟 active distro 的 user home 或外部終端 |
| `GET /api/containers?all=1`、`POST …/start|stop`、`DELETE …`（Remove）、`GET …/logs[/stream]`、`GET …/exec` | 容器 |
| `GET /api/images`、`DELETE /api/images/{ref}[?force=1]`、`POST /api/images/prune` | 映像 |
| `GET /api/volumes`、`DELETE /api/volumes/{name}[?force=1]`、`POST /api/volumes/prune` | volume |
| `GET /api/networks` | 網路（唯讀） |
| `GET /api/machines`、…／config、…／online、install、**use／unmanage／unregister**、用量摘要（0.5） | WSL 發行版與多 distro |
| `GET|PUT /api/settings`（含 theme）、`GET /api/doctor`、`POST /api/window/{hide|show|quit}` | 設定、診斷、視窗 |

錯誤格式 `{"error","code"}`，`code` ∈ `not_ready`(503)／`api`(502)／`not_found`(404)／`conflict`(409)／`usage`(400)／`error`。

## 啟動參數

| 參數 | 說明 |
|---|---|
| `--tray` | 隱藏啟動，只顯示系統匣圖示（登入自啟採用此參數） |

## 建置

```bash
make build-frontend   # frontend/dist（tsc + vite）
make build-gui        # dist/wsld-gui.exe（VERSION←git tag／預設 0.5.2 → 左下角 v…）
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
| `POST /__test/list-hold`／`POST /__test/list-release`／`GET /__test/list-held` | 卡住下一次 ListImages（測刷新競態／abort） |
| `POST /__test/image?tag=&size=&containers=` | 新增映像（`containers>0` 視為使用中） |
| `POST /__test/volume?name=&in_use=1` | 新增 volume |
| `POST /__test/network?name=&driver=` | 新增網路 |
| `POST /__test/wsl/fail-install[?on=0]`、`GET /__test/wsl/installed` | 讓 FakeWSL 的 `--install` 失敗；查詢已安裝清單 |

FakeEngine 預設資料：容器 web（running）／db（exited）；映像 nginx:1.27（使用中）／redis:7／一個 dangling；volume pgdata（使用中）／orphan；網路 bridge／host／none／shop_default。FakeWSL 預設：Ubuntu-26.04（Running、預設、systemd）與 docker-desktop（Stopped）。

### 實機（Windows）手測項目

Playwright 無法涵蓋 Wails／systray 原生行為；Windows 手測（基底已於 Ubuntu-26.04 + WebView2 實測；0.5 項隨實作補）：

1. `dist\wsld-gui.exe` 開啟後標題列「已連線」，列表顯示容器；側欄切視圖。
2. 按 X → 縮 Tray；Tray「開啟主視窗」可還原；`--tray`／「結束」行為同既有。
3. Settings（側欄，0.5）→「開機自啟動」toggle → HKCU Run；Theme radio 切換可見。
4. Machines：設為目前、Info（tcp／wsl 設定）、folder／terminal；unregister 需打名稱（0.5）。

## 圖示與 Windows 資源

- `cmd/wsld-gui/assets/gen_icon.go`（`go run`／`make icon`）產生 `tray.ico`（16/20/24/32/40/48/64/256；≤64 BMP、256 PNG）與 `icon.png`；Tray 與視窗共用同一圖示。
- `cmd/wsld-gui/winres/winres.json` 以 [go-winres](https://github.com/tc-hib/go-winres) 產生 `rsrc_windows_amd64.syso`（圖示群組 ID 3 = Wails 的 `AppIconID`、Per-Monitor v2 DPI manifest、Common Controls v6、版本資源）。`make winres` 重新產生；`.syso` 已入版控，`go build` 會自動連結。

## 已知限制

- Theme：Light／Dark／System（`data-theme`／`prefers-color-scheme`）。
- Images 的「使用中」欄依 Docker `/images/json` 的 `Containers`；舊版 daemon 可能回 -1，顯示為「–」。
- Machines「新增發行版」只執行 `wsl --install --no-launch`：首次進入 distro 建立使用者、以及 Docker bootstrap（`wsld init`）仍需使用者操作；以「設為目前」＋ `wsld init` 納管。
- Networks 為唯讀；建立／刪除網路請用 `docker network`。
- npiperelay／自動更新／arm64／**首次 Setup 一鍵**：Roadmap Extra（不綁 0.5）。

## 0.5 實機抽樣驗收

1. 多 distro：`wsld use`／GUI「設為目前」切換；不停舊引擎；Logs／Terminal／Info 硬切；未 init 顯示未就緒，**Start engine 自動 init 後連線**。
2. Unregister：需輸入發行版名稱確認；profile 刪除。
3. Containers：Remove running（確認後刪除）；Info summary 欄位齊全。
4. Settings 側欄 Theme；header 無 Settings／tcp；folder／terminal 有 border。
