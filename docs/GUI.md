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
┌ top bar ────────────────────────────────────────────────────────────────────────────┐
│ ▦ wsld v… │ ▣Containers ◈Images ⛁Volumes ⚇Networks │ ▭Machines ··· [● Ubuntu · Connected :2375] [⏻ Start engine*] │ [📁][>_] ⚙ │
├──────────────────────────────────────────────────────────────┬──────────────────────┤
│ <頁標題> <摘要> ·························· 工具列（搜尋／篩選／prune／Add／Save） │ info drawer（右）    │
│ （Images：磁碟用量卡片獨佔一列）                                 │ 容器／compose／      │
│ main（滿寬，卡片底）                                            │ machine 詳情         │
│  Containers：列表＋下方 Logs│Terminal 分頁                     │                      │
│  Machines：卡片；Settings：分組卡片（含語言、GitHub）              │                      │
│ ┌ InfoBar（錯誤紅／未就緒黃／結果綠，浮在底部）┐                  │                      │
└──────────────────────────────────────────────────────────────┴──────────────────────┘
```

外觀依設計稿（Fluent 語彙：圓角 6–8、Mica 底、單一藍色強調色）；色彩 tokens 在 `frontend/src/style.css` 的 `:root`／深色區塊，文字在其底色上 ≥ 4.5:1。狀態一律「形狀＋文字＋顏色」：實心點＝執行中、空心環＝停止、脈動＝處理中。

- **Top bar（滿寬，0.8 起取代側欄）**：品牌標誌（全方格鯨魚，與 `gen_icon.go` 同一張 16 格主格線）＋版本；**視圖分頁** Containers／Images／Volumes／Networks｜Machines（目前分頁底部藍色指示條、`aria-current="page"`；視窗 < 1100px 時只剩圖示，名稱留在 `title`／文字內容）；右側**發行版晶片**（active distro、引擎狀態點與文字 `status-connection`、已連線時顯示 API 埠；點擊前往 Machines；切換中顯示 Loading）、**需要時才出現**的 Start engine、**有 border** 的 Open folder／Open terminal、Settings 齒輪。視圖存 localStorage；切換視圖關閉 Logs／Terminal／Info。**不顯示** API endpoint（tcp）。
- **頁標題列**：內容區頂端，目前視圖標題＋摘要（Containers：執行中／已停止數；資源頁：項目數，映像含總大小）與該頁工具列同一列（工具列靠右，窄時換行），省下獨立 header 的高度。
- **Containers**：工具列搜尋（名稱／映像／compose 服務）＋狀態分段篩選（全部／執行中／已停止，附數量）；名稱下方顯示 docker 狀態描述；開啟 Info 時該列標示選取。Logs／Terminal 面板頂部有分頁可切換同一容器（已停止容器不能開終端）。
- **Images**：磁碟用量長條（使用中／未使用／懸空；daemon 未統計 `Containers=-1` 時歸「未統計」不硬猜）＋分段篩選＋清除未使用。
- **Machines**：每個發行版一張卡片（狀態、WSL 版本、角色標籤、目前那台顯示 API 端點；設為主要、**啟動／停止**、Info、資料夾、終端、刪除）。啟動＝`wsl -d <name> -- true`（已納管的由 systemd 帶起 docker.service）；停止＝已納管的先 `systemctl stop docker` 再 `wsl --terminate`，停止主要發行版前會確認（Docker 與容器一併停止）。
- **Settings**：「外觀」（主題、語言）「行為」（開機自啟動、刷新間隔、終端 shell）「網路」（區網存取）「關於」（版本、GitHub）四組 SettingsCard（左標題＋說明、右控制項）；Save 在頁標題列右側。
- **網路 › 區網存取**（`settings-lan-group`）：
  - 不經 Save，**即時套用**、不跳 wsld 確認框。開關 `settings-lan` → `PUT /api/lan {enabled}`（寫入 `lan_access`）→ wsld-gui 立即在內網 IP 監聽；只改設定，不提權。防火牆授權在 wsld-gui 啟動時請求（`Server.EnsureFirewall`：`firewall` 為 `missing`／`blocked` 時以 UAC 執行 `wsld-gui.exe --allow-firewall`；一般啟動在 DOM ready、`--tray` 在第一次顯示視窗，每次執行一次）。
  - 沒有獨立的防火牆列。`GET /api/lan` 一律回報 `firewall`（與開關無關）與 `firewall_pending`（UAC 等待中）。區網存取開啟、非 mirrored 且未授權／被封鎖／等待中時顯示 `settings-lan-fw-warn`（`data-state`＝missing／blocked／pending）與「授權防火牆」`settings-lan-fw-allow`（`POST /api/lan/firewall`；等待中停用，前端每 1.5 秒重查）；取消 UAC（409 `canceled`）的原因顯示在 error banner，警告與按鈕保留可重試。
  - 狀態文字 `settings-lan-status`：查詢中／關閉／正在轉發 N 個位址／沒有 published 埠／有位址無法監聽／mirrored 阻擋／還原中。設定頁不列位址。
  - 偵測到 0.9.0 的 mirrored 模式時顯示 `settings-lan-mirrored` 與「還原 NAT」`settings-lan-restore`：
    - 流程：確認框（確定鈕「還原 NAT」，非 danger）→ `POST /api/lan/restore-nat` → UAC → `wsl --shutdown` → 等 API 埠釋放 → 啟動引擎。
    - 取消 UAC 等失敗時，原因顯示在 error banner。
  - 進入設定頁時查詢一次狀態（`GET /api/lan`，會先同步轉發）。背景每 3 秒同步轉發（`Server.RunLAN`）。
- **容器頁的瀏覽器按鈕**：
  - 開啟區網存取後，每個服務列的動作欄在 Terminal 與 Info 之間多一顆地球圖示按鈕 `btn-lan-<名稱>`（關閉時各列都不顯示，圖示保持對齊）。
  - 一個區網位址：點擊直接以系統瀏覽器開啟（`POST /api/lan/open`，只接受目前轉發中的位址）。多個位址（多個 published 埠）：跳小選單 `lan-menu`／`lan-menu-item` 選一個，點外面關閉。
  - 沒有 published 埠：按鈕停用。全部監聽失敗：按鈕停用並標紅（`warn`），原因放在 title。
  - 容器 Info（`info-lan`）與 compose 群組 Info 的服務清單以文字列出位址。
  - 資料來自 `GET /api/lan/forwards`，隨容器清單一起刷新。
- **輸入元件**（設計稿「Input controls」）：全部自繪、不用瀏覽器預設外觀。文字欄位外框 `.tf`（34px、底邊較深；焦點＝藍色底線、錯誤＝紅色底線、停用＝灰底）；數字隱藏原生 spinner，改欄內 −／＋ 與單位，超出 min／max 標錯；下拉一律用自繪 `dropdown()`（設定頁語言、Add machine 發行版；combobox＋listbox，外框同 `.tf.select`，展開清單也自繪、不用原生 `<select>` 與 OS 彈出清單，目前值以左側強調條＋勾標示，未選時顯示淡色 placeholder，↑↓／Enter／Esc 可操作，對話框內 Esc 只關清單）；搜尋有清除鈕（Esc 亦可）；核取方塊／選項按鈕 18px 以 CSS 畫勾與點；開關 40×20、左側顯示開／關文字；輸入名稱確認在完全相符時顯示綠色勾。鍵盤焦點：小型控制項 2px 外框、文字欄位用底線。
- **引擎未就緒**：資源列表改顯示「引擎尚未啟動」卡片：啟動引擎按鈕＋由 `/api/status` 推得的檢查清單（找到發行版、執行中、systemd、docker.service、API 可連線）。
- **InfoBar**：錯誤／未就緒／動作結果浮在內容欄底部，不擠動列表；結果 8 秒後自動消失。
- 列表固定顯示全部容器；無手動刷新鈕；列上 actions 無邊框純圖示；**列高／格線與其他欄對齊**。

## 功能對照（SPEC §5.5／Roadmap 0.5）

| 區塊 | 行為 | Playwright 覆蓋 |
|---|---|---|
| Tray | 狀態列、啟動引擎、開啟主視窗、結束；按 X 縮 Tray | 實機手測 |
| Top bar | 品牌＋版本、五個視圖分頁（窄時只剩圖示）、發行版晶片（引擎狀態，點擊到 Machines）、需要時才出現的 Start engine、有 border 的 folder／terminal、Settings 齒輪；無 tcp | `shell.spec.ts`／`list` |
| 頁標題列 | 頁標題＋摘要與工具列同一列 | `shell`／`list` |
| 輸入元件 | 自繪 checkbox／radio／select／dropdown 清單（滑鼠＋鍵盤）／number（−／＋）／搜尋清除／開關狀態文字／確認勾 | `inputs.spec.ts` |
| 按鈕圖示 | 列／compose：無邊框純圖示；**header icon：有 border** | `list`／`groups`／`info` |
| Containers | Name／Image／State／操作（Start／Stop／Logs／Term／Info／**Remove**）；右側 Info＝summary | `list`／`info` |
| Info（容器） | Name、ID、Image、State、Created、Started、Command、Ports、Networks（+IP）、Mounts、Labels、Restart policy | `info.spec.ts` |
| Compose | 分組、折疊、整組 Start／Stop／Info 純圖示 | `groups`／`info` |
| Start／Stop／Remove | pending 回饋；Remove 可含 running（確認會停） | `list`（Remove 隨 0.5） |
| Logs／Exec | tail 100；Live；xterm exec | `panels.spec.ts` |
| Images／Volumes／Networks | 同既有；列高打磨 | `views.spec.ts` |
| Machines | 設為主要、**啟動／停止**（play／stop；停止主要需確認）、folder、terminal（底部 panel、home）、**刪除發行版**（trash、打名稱確認＝unregister）、右側 Info、Add | `views`（隨 0.5；啟動／停止隨 0.12） |
| Machines Info | `.wslconfig` memory／processors、`wsl.conf`、**tcp endpoint**、CPU／Mem 摘要；取代 ⓘ dialog | 隨 0.5 |
| Settings | refresh、exec_shell、tray_autostart、Theme Light／Dark／System、語言、GitHub | `settings`／`inputs` |
| Settings › 網路／容器頁 | 區網存取開關（即時、不重啟 WSL）、狀態、mirrored 警告＋還原 NAT（含取消 UAC）、zh-TW；容器列的地球按鈕（點擊開瀏覽器、多埠選單、停用／標紅原因）、Info 區網欄 | `lan.spec.ts` |
| i18n | Settings「外觀」語言下拉（立即生效） | `list.spec.ts` |
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
| `GET /api/networks`、`DELETE /api/networks/{id}`、`POST /api/networks/prune` | 網路（內建 bridge／host／none 不可刪） |
| `GET /api/machines`、…／config、…／online、install、**use／start／stop／unmanage／unregister**、用量摘要（0.5） | WSL 發行版與多 distro |
| `GET|PUT /api/settings`（含 theme）、`GET /api/doctor`、`POST /api/window/{hide|show|quit}` | 設定、診斷、視窗 |

錯誤格式 `{"error","code"}`，`code` ∈ `not_ready`(503)／`api`(502)／`not_found`(404)／`conflict`(409)／`usage`(400)／`error`。

## 啟動參數

| 參數 | 說明 |
|---|---|
| `--tray` | 隱藏啟動，只顯示系統匣圖示（登入自啟採用此參數） |

單例：`wsld-gui` 以命名 Mutex 保證只跑一個程序。再次啟動（非 `--tray`）會喚醒既有主視窗並立刻退出，不會再出現第二個視窗／systray。

## 建置

```bash
make build-frontend   # frontend/dist（tsc + vite）
make build-gui        # dist/wsld-gui.exe（VERSION←git tag／預設 0.11.1 → 頂部列 v…）
make build-win        # 上述 + dist/wsld.exe
```

WebView2 執行期：Windows 11 內建；缺少時 Wails 會提示下載。

## 測試

```bash
make test-ui          # 建前端 → Playwright（自動啟動 wsld-gui-testhost）
cd frontend && npx playwright test --ui   # 互動除錯
```

Playwright 在 WSL 需要 Chromium 相依套件；`make test-ui` 會透過 `scripts/ensure-playwright-libs.sh` 在無 sudo 時把 `libnss3` 等解到 `~/.local/lib/pw`（詳見 [TESTING.md](TESTING.md)）。有 sudo 也可 `npx playwright install-deps chromium`。

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

1. `dist\wsld-gui.exe` 開啟後標題列「已連線」，列表顯示容器；頂部分頁切視圖。
2. 按 X → 縮 Tray；Tray「開啟主視窗」可還原；`--tray`／「結束」行為同既有。
3. Settings（頂部齒輪）→「開機自啟動」toggle → HKCU Run；Theme radio 切換可見。
4. Machines：設為主要、Info（tcp／wsl 設定）、folder／terminal；unregister 需打名稱（0.5）。

## 圖示與 Windows 資源

- `cmd/wsld-gui/assets/gen_icon.go`（`go run`／`make icon`）產生 `tray.ico`（16/20/24/32/40/48/64/256；≤64 BMP、256 PNG）與 `icon.png`；Tray 與視窗共用同一圖示。
- `cmd/wsld-gui/winres/winres.json` 以 [go-winres](https://github.com/tc-hib/go-winres) 產生 `rsrc_windows_amd64.syso`（圖示群組 ID 3 = Wails 的 `AppIconID`、Per-Monitor v2 DPI manifest、Common Controls v6、版本資源）。`make winres` 重新產生；`.syso` 已入版控，`go build` 會自動連結。

## 已知限制

- Theme：Light／Dark／System（`data-theme`／`prefers-color-scheme`）。
- Images 的「使用中」欄依 Docker `/images/json` 的 `Containers`；舊版 daemon 可能回 -1，顯示為「–」。
- Machines「新增機器」執行 `wsl --install --no-launch`，並依對話框的 Linux 帳號／密碼建立使用者（密碼經 stdin 交給 `chpasswd`，不保存；留空則不設密碼）、設為 `/etc/wsl.conf` 的預設使用者。安裝完直接「設為主要」納管，同一流程 init + start。「啟動引擎」／「設為主要」失敗原因留在 banner 直到引擎就緒。
- 安裝中對話框輪詢 `GET /api/machines/install/progress`：觀察 `%TEMP%\<distribution>.wsl` 大小，總量取自官方 `DistributionInfo.json`＋HEAD；查不到總量時改為不定進度＋經過時間。同時只允許一個安裝（第二個回 409）。
- Networks：列表＋個別 Remove（內建 disabled）＋清除未使用；使用中回 409。
- npiperelay／自動更新／arm64／**首次 Setup 一鍵**：Roadmap Extra（不綁 0.5）。

## 0.5 實機抽樣驗收

1. 多 distro：`wsld use`／GUI「設為主要」切換；不停舊引擎；Logs／Terminal／Info 硬切；**首次設為主要自動 init + start 後連線**。
2. Unregister：需輸入發行版名稱確認；profile 刪除。
3. Containers：Remove running（確認後刪除）；Info summary 欄位齊全。
4. Settings 側欄 Theme；header 無 Settings／tcp；folder／terminal 有 border。
