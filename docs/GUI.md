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
│ ▦ wsld v… │ ▣Containers ◈Images ⛁Volumes ⚇Networks │ ▭Machines ··· [● Ubuntu · Connected :2375] │ [📁][>_] ⚙ │
├──────────────────────────────────────────────────────────────┬──────────────────────┤
│ <頁標題> <摘要> ·························· 工具列（搜尋／篩選／prune／Add／Save） │ info drawer（右）    │
│ （標題旁：磁碟用量鈕，點開浮出明細）                             │ 容器／compose／      │
│ main（滿寬，卡片底）                                            │ machine 詳情         │
│  Containers：列表＋下方 Logs│Terminal 分頁                     │                      │
│  Machines：卡片；Settings：分組卡片（含語言、GitHub）              │                      │
│ ┌ InfoBar（錯誤紅／未就緒黃／結果綠，浮在底部）┐                  │                      │
└──────────────────────────────────────────────────────────────┴──────────────────────┘
```

外觀依設計稿（Fluent 語彙：圓角 6–8、Mica 底、單一藍色強調色）；色彩 tokens 在 `frontend/src/style.css` 的 `:root`／深色區塊，文字在其底色上 ≥ 4.5:1。狀態一律「形狀＋文字＋顏色」：實心點＝執行中、空心環＝停止、脈動＝處理中。

- **Top bar（滿寬，0.8 起取代側欄）**：品牌標誌（全方格鯨魚，與 `gen_icon.go` 同一張 16 格主格線）＋版本；**視圖分頁** Containers／Images／Volumes／Networks｜Machines｜Kubernetes（目前分頁底部藍色指示條、`aria-current="page"`；視窗 < 1100px 時只剩圖示，名稱留在 `title`／文字內容）；右側**發行版晶片**（active distro、引擎狀態點與文字 `status-connection`、已連線時顯示 API 埠；點擊前往 Machines；切換中顯示 Loading）、**需要時才出現**的 Start engine、**有 border** 的 Open folder／Open terminal、Settings 齒輪。視圖存 localStorage；切換視圖關閉 Logs／Terminal／Info。**不顯示** API endpoint（tcp）。
- **頁標題列**：內容區頂端，目前視圖標題＋摘要（Containers：執行中／已停止數；其他頁：項目數）＋磁碟用量鈕，與該頁工具列同一列（工具列靠右，窄時換行），省下獨立 header 的高度。
- **Containers**：工具列搜尋（名稱／映像／compose 服務）＋狀態分段篩選（全部／執行中／已停止，附數量）；名稱下方顯示 docker 狀態描述；開啟 Info 時該列標示選取。Logs／Terminal 面板頂部有分頁可切換同一容器（已停止容器不能開終端）。
- **磁碟用量鈕**（`usage-btn`）：Containers／Images／Volumes／Machines 頁標題摘要旁一顆 28px 高的鈕：迷你分段長條（`usage-bar`）＋該頁總量（`usage-total`）＋下拉箭頭，固定一列、不隨項目數變高，不另佔列表上方的高度。點開（`aria-expanded`）浮出明細 `usage-pop`：每項一行（`usage-part`：色塊、名稱、大小、占比），名稱過長截斷並有 title；Esc 收起且焦點回到鈕上，點外面也收起；列表週期刷新與換頁時明細維持／收起。每頁只列自己那一類的每一項：容器頁＝每個 compose 專案（整組加總）與每個未分組容器的可寫層；映像頁＝每個映像；Volume 頁＝每個 volume（未統計者不列）；Machines＝每台機器的 ext4.vhdx。最大的 5 項各一段，其餘併為「其他（N）」；5 項依名稱排序後依序取 `--series-1..5`（dataviz 參考調色盤前 5 槽，light／dark 各一組），長條與明細同一順序，大小些微變動不換色。滑過長條分段有 title（名稱・大小・占比）。視窗 ≤ 1100px 時迷你長條縮為 56px、搜尋欄 220px，≤ 860px 只留總量，避免標題列換行。沒有大小可顯示（引擎未就緒、df 尚未回來）時不顯示。
- **Images**：分段篩選（使用中／未使用／懸空；daemon 未統計 `Containers=-1` 時歸「未統計」不硬猜）＋清除未使用。
- **容器與 Volume 大小**：Containers 列表有 Size 欄（可寫層；title 顯示含映像的虛擬大小 `SizeRootFs`），compose 群組列顯示該組加總；Volumes 列表有 Size 欄（未統計顯示「–」）。總量在頁標題旁的磁碟用量鈕。大小來自 `GET /api/disk`（Docker `/system/df`）；後端快取 15 秒（`/system/df` 會對 volume 跑 du），刪除／prune／啟停後失效；查詢失敗不影響列表。Networks、Settings 不佔磁碟，不顯示。
- **Machines**：每個發行版一張卡片（狀態、WSL 版本、**磁碟**＝ext4.vhdx 在 Windows 上的檔案大小（讀 `HKCU\…\Lxss` 登錄檔，停止中也讀得到、不喚醒）／**上限**（`machine-disk-limit`，讀 VHDX 標頭的虛擬容量）、Engine 單獨一組置頂（不可停止／刪除）、其他發行版在下一組；Engine／Client 標籤（「設為 engine」「共用 engine」按鈕）；**啟動／停止**、Info、資料夾、終端、刪除）。啟動＝`wsl -d <name> -- true`（已納管的由 systemd 帶起 docker.service）；停止＝已納管的先 `systemctl stop docker` 再 `wsl --terminate`，停止 engine 前會確認（Docker 與容器一併停止，且 wsld 不再自動拉起）。**engine 保活**：wsld-gui 替 engine 掛一個常駐 `wsl -d <name> -- sleep infinity`（0.17.2），避免 WSL 閒置約 20 秒後回收；停止主要機器時放掉，之後不自動重新喚醒。
- **Settings**：「外觀」（主題、語言）「行為」（開機自啟動、刷新間隔、終端 shell）「網路」（區網存取）「關於」（版本、GitHub）四組 SettingsCard（左標題＋說明、右控制項）；Save 在頁標題列右側。
- **網路 › 區網存取**（`settings-lan-group`）：
  - 不經 Save，**即時套用**、不跳 wsld 確認框。開關 `settings-lan` → `PUT /api/lan {enabled}`（寫入 `lan_access`）；不提權。已授權防火牆時 wsld-gui 立即在內網 IP 監聽；`firewall` 為 `missing`／`blocked` 時**不監聽**（避免 Windows 自己跳安全性警訊），並接著開啟防火牆說明對話框。wsld-gui 啟動時不提權。
  - 防火牆說明對話框 `fw-dialog`：標題 `fw-dialog-title`；規則 `fw-dialog-rule`（名稱、程式路徑 `exe`、TCP、私人＋公用、僅同網段）；`fw-dialog-method`（`data-method`＝netsh／helper，說明 UAC 會顯示 netsh.exe「網路命令殼層」或 wsld-gui）；「稍後」`fw-dialog-later`、「繼續」`fw-dialog-continue`（盾牌圖示）→ `POST /api/lan/firewall`。
  - 沒有獨立的防火牆列。`GET /api/lan` 一律回報 `firewall`（與開關無關）、`firewall_method`（netsh／helper）、`exe` 與 `firewall_pending`（UAC 等待中）；GET 會重新讀取規則，背景同步沿用 30 秒快取。區網存取開啟、非 mirrored 且未授權／被封鎖／等待中時顯示 `settings-lan-fw-warn`（`data-state`＝missing／blocked／pending，文字說明轉發已暫停）與「授權防火牆」`settings-lan-fw-allow`（先開 `fw-dialog`；等待中停用，前端每 1.5 秒重查）；取消 UAC（409 `canceled`）的原因顯示在 error banner，警告與按鈕保留可重試。
  - 狀態文字 `settings-lan-status`：查詢中／關閉／正在轉發 N 個位址／等待防火牆授權／沒有 published 埠／有位址無法監聽／mirrored 阻擋／還原中。設定頁不列位址。
  - 偵測到 0.9.0 的 mirrored 模式時顯示 `settings-lan-mirrored` 與「還原 NAT」`settings-lan-restore`：
    - 流程：確認框（確定鈕「還原 NAT」，非 danger）→ `POST /api/lan/restore-nat` → UAC → `wsl --shutdown` → 等 API 埠釋放 → 啟動引擎。
    - 取消 UAC 等失敗時，原因顯示在 error banner。
  - 進入設定頁時查詢一次狀態（`GET /api/lan`，會先同步轉發）。背景每 3 秒同步轉發（`Server.RunLAN`）。
- **容器頁的瀏覽器按鈕**：
  - 每個服務列的動作欄在 Terminal 與 Info 之間常駐一顆地球圖示按鈕 `btn-browser-<名稱>`（各列一致，圖示保持對齊）。
  - 開啟區網存取且該容器有成功的轉發：開區網位址（`POST /api/lan/open`，只接受目前轉發中的位址）。否則（區網存取關閉、尚未授權防火牆、轉發失敗）：開 `http://localhost:<published 埠>`（`POST /api/open-local`，只接受執行中容器已發佈的 TCP 埠）。
  - 一個位址：點擊直接以系統瀏覽器開啟。多個位址（多個 published 埠）：跳小選單 `lan-menu`／`lan-menu-item` 選一個，點外面關閉。
  - 沒有 published 埠或容器已停止：按鈕停用。區網轉發全部失敗：按鈕標紅（`warn`），原因放在 title，點擊改開 localhost。
  - 容器 Info（`info-lan`）與 compose 群組 Info 的服務清單以文字列出位址。
  - 資料來自 `GET /api/lan/forwards`，隨容器清單一起刷新。
- **輸入元件**（設計稿「Input controls」）：全部自繪、不用瀏覽器預設外觀。文字欄位外框 `.tf`（34px、底邊較深；焦點＝藍色底線、錯誤＝紅色底線、停用＝灰底）；數字隱藏原生 spinner，改欄內 −／＋ 與單位，超出 min／max 標錯；下拉一律用自繪 `dropdown()`（設定頁語言、Add machine 發行版；combobox＋listbox，外框同 `.tf.select`，展開清單也自繪、不用原生 `<select>` 與 OS 彈出清單，目前值以左側強調條＋勾標示，未選時顯示淡色 placeholder，↑↓／Enter／Esc 可操作，對話框內 Esc 只關清單）；搜尋有清除鈕（Esc 亦可）；核取方塊／選項按鈕 18px 以 CSS 畫勾與點；開關 40×20、左側顯示開／關文字；輸入名稱確認在完全相符時顯示綠色勾。鍵盤焦點：小型控制項 2px 外框、文字欄位用底線。
- **引擎未就緒**：資源列表改顯示「引擎尚未啟動」卡片（沒有啟動按鈕：wsld 正在自動帶起，失敗原因顯示在卡片與橫幅）＋由 `/api/status` 推得的檢查清單（找到發行版、執行中、systemd、docker.service、API 可連線）。
- **InfoBar**：錯誤／未就緒／動作結果浮在內容欄底部，不擠動列表；結果 8 秒後自動消失。
- 列表固定顯示全部容器；無手動刷新鈕；列上 actions 無邊框純圖示；**列高／格線與其他欄對齊**。

## 功能對照（SPEC §5.5／Roadmap 0.5）

| 區塊 | 行為 | Playwright 覆蓋 |
|---|---|---|
| Tray | 狀態列、開啟主視窗、結束；按 X 縮 Tray | 實機手測 |
| Top bar | 品牌＋版本、六個視圖分頁（窄時只剩圖示）、發行版晶片（引擎狀態，點擊到 Machines）、有 border 的 folder／terminal、Settings 齒輪；無 tcp | `shell.spec.ts`／`list` |
| 頁標題列 | 頁標題＋摘要與工具列同一列 | `shell`／`list` |
| 輸入元件 | 自繪 checkbox／radio／select／dropdown 清單（滑鼠＋鍵盤）／number（−／＋）／搜尋清除／開關狀態文字／確認勾 | `inputs.spec.ts` |
| 按鈕圖示 | 列／compose：無邊框純圖示；**header icon：有 border** | `list`／`groups`／`info` |
| Containers | Name／Image／State／操作（Start／Stop／Logs／Term／Info／**Remove**）；右側 Info＝summary | `list`／`info` |
| Info（容器） | Name、ID、Image、State、Created、Started、Command、Ports、Networks（+IP）、Mounts、Labels、Restart policy | `info.spec.ts` |
| Compose | 分組、折疊、整組 Start／Stop／Info 純圖示 | `groups`／`info` |
| Start／Stop／Remove | pending 回饋；Remove 可含 running（確認會停） | `list`（Remove 隨 0.5） |
| Logs／Exec | tail 100；Live；xterm exec | `panels.spec.ts` |
| Images／Volumes／Networks | 同既有；列高打磨 | `views.spec.ts` |
| Machines | Engine／Client 標籤與「設為 engine」「共用 engine」、**啟動／停止**（play／stop；停止 engine 需確認，停止後 wsld 不會自動拉起）、folder、terminal（底部 panel、home）、**刪除發行版**（trash、打名稱確認＝unregister）、右側 Info、Add | `views`（隨 0.5；啟動／停止隨 0.12） |
| Machines Info | `.wslconfig` memory／processors、`wsl.conf`（停止中不讀——`wsl -d` 會把機器開起來——改顯示「啟動機器後才能查看」，且停止中不輪詢用量；啟動／停止後自動更新）、**tcp endpoint**、CPU／Mem 摘要、磁碟（執行中：`df /` 長條；VHDX 大小）、**磁碟上限**＋調整鈕（對話框：GB；需要關閉 WSL 時列出會被停止的機器、按鈕改為「關閉 WSL 並套用」——目標執行中時一開始就顯示，已停止但 VHD 仍被占用時於第一次套用後顯示；執行中顯示「最小可設約 N GB」（`resize-info`）並擋下更小的值，伺服器回 `too_small` 時也記住最小值；縮小時的備份提醒；wsl 錯誤與「從 WSL 內執行」的拒絕訊息留在對話框；調整期間暫停 Info 輪詢）；取代 ⓘ dialog | 隨 0.5；磁碟 `disk.spec.ts`；上限 `vhd.spec.ts` |
| Machines › 釋放磁碟空間 | 卡片的縮小箭頭鈕（`btn-machine-compact-<name>`）：磁碟未被占用直接壓縮；執行中先確認（列出會被停止的機器，「關閉 WSL 並釋放空間」），停止但仍被占用則於第一次嘗試後確認；完成通知釋放量與 Windows 上的佔用；調整大小成功後也會一併壓縮並在通知中報告。這兩種結果通知**不自動消失**（作業常要將近一分鐘，使用者可能已切走），按右側 ✕ 關閉；其他通知仍 8 秒後消失 | `vhd.spec.ts` |
| Add machine › 磁碟上限 | 與自訂名稱同列的 GB 欄（預設 60；留空＝WSL 預設 1 TB；8–16384 以外顯示錯誤且不能安裝） | `vhd.spec.ts` |
| 磁碟用量 | 四頁的用量鈕（不加高度、點開／Esc／點外面、刷新時維持；明細每項大小與占比、前 5 大＋其他、總計）、容器／Volume Size 欄與副標總計、compose 群組加總、Machines 卡片磁碟與合計、Info 磁碟長條 | `disk.spec.ts` |
| Settings | refresh、exec_shell、tray_autostart、Theme Light／Dark／System、語言、GitHub | `settings`／`inputs` |
| Settings › 網路／容器頁 | 區網存取開關（即時、不重啟 WSL）、狀態、防火牆說明對話框（netsh／helper、稍後、等待 UAC、取消 UAC）、mirrored 警告＋還原 NAT（含取消 UAC）、zh-TW；容器列的地球按鈕（常駐；區網關閉時開 localhost、點擊開瀏覽器、多埠選單、停用／標紅原因）、Info 區網欄 | `lan.spec.ts` |
| Kubernetes | 頂部分頁 Machines 右邊另一條分隔線之後；預設關閉＝hero（說明會建立 k8s-engine、`btn-k8s-enable`）；開啟中＝步驟清單 `k8s-steps`（engine → distro（含下載進度）→ bootstrap → share → start → ready → kubeconfig；✓／…／○／✕）、失敗時 `k8s-error`＋Retry；開啟＝「Kubernetes engine」群組的 `k8s-card`，版面與資訊對齊 Machines 的 Docker engine 卡（State、Kubernetes version、Disk＝ext4.vhdx 大小／上限、Endpoint；右側按鈕同 Docker engine 卡：「關閉」`btn-k8s-disable`（取代停止，需確認）、Info、資料夾、終端（底部 panel）、釋放磁碟空間，全部作用在 k8s distro；資料夾經 `POST /api/distro/open-folder?distro=<名稱>`），卡片下方是限制說明；Windows／其他 distro 的 kubeconfig 路徑（可複製）收在 k8s distro 的 Info 裡（Endpoint 欄改顯示 k8s API），再下面是 Pods 群組表格（namespace、name、status、ready、restarts、IP、age）。k8s distro **不出現在 Machines 頁**（`/api/machines` 略過 `role = "k8s"`）。Containers 頁把 pod 容器收成「Kubernetes」群組（顯示「容器 · pod（namespace）」、隱藏 pause 容器並把它的 published 埠併到應用容器、沒有整組啟停） | `k8s.spec.ts` |
| i18n | Settings「外觀」語言下拉（立即生效） | `list.spec.ts` |
| 多 distro | active 切換作用於全部功能；硬切面板 | 隨 0.5 |

### 破壞性操作的規則

- 移除／清除一律確認對話框；**刪除發行版**（對應 Add）需**輸入 distro 名稱**後才可確定（`wsl --unregister`）。
- 容器 Remove：running 可刪（會先停止）；映像／volume 使用中仍不自動 force（409 → banner）。
- 成功結果顯示在綠色 notice（8 秒後自動消失）。

## 橋接 API（`internal/gui`）

| 端點 | 說明 |
|---|---|
| `GET /api/meta`／`GET /api/status` | 版本、語系訊息、設定；引擎狀態（含自動帶起的 `start_error`） |
| `GET /api/machines/{name}/exec` | Machines 底部 xterm（WSL `script` PTY → login bash／user home） |
| `POST /api/distro/{open-folder\|open-terminal}` | Header：開啟 active distro 的 user home 或外部終端 |
| `GET /api/containers?all=1`、`POST …/start|stop`、`DELETE …`（Remove）、`GET …/logs[/stream]`、`GET …/exec` | 容器 |
| `GET /api/images`、`DELETE /api/images/{ref}[?force=1]`、`POST /api/images/prune` | 映像 |
| `GET /api/volumes`、`DELETE /api/volumes/{name}[?force=1]`、`POST /api/volumes/prune` | volume |
| `GET /api/networks`、`DELETE /api/networks/{id}`、`POST /api/networks/prune` | 網路（內建 bridge／host／none 不可刪） |
| `GET /api/disk` | Docker 磁碟用量：`summary`（四類 count／size／reclaimable、total、reclaimable）＋逐容器 `size_rw`／`size_root_fs`、逐 volume `size`／`ref_count`；快取 15 秒 |
| `GET /api/machines`（含 `disk_bytes`、`disk_limit_bytes`）、…／config（含 `disk_bytes`、`disk_limit_bytes`、執行中 `disk_used_bytes`／`disk_total_bytes`）、…／online、install（`vhd_size_gb`，0＝WSL 預設）、install/defaults（`vhd_size_gb`／`vhd_min_gb`／`vhd_max_gb`）、**use／start／stop／unmanage／unregister**、`POST …/{name}/compact {"shutdown"}`（流程同 resize，回 `before_bytes`／`after_bytes`）、`POST …/{name}/resize {"size_gb","shutdown"}`（成功後一併壓縮，回 `compacted`／`before_bytes`／`after_bytes`）（VHD 被占用且未同意 → 409 `needs_shutdown`＋`running`；同意 → `wsl --shutdown`、調整、重啟原本執行中的機器，回 `shutdown`／`restarted`；wsld-gui 位於 WSL 內 → 409 `self_in_wsl`）、用量摘要（0.5） | WSL 發行版與多 distro |
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
make build-gui        # dist/wsld-gui.exe（VERSION←git tag／預設 0.17.1 → 頂部列 v…）
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
| `POST /__test/container?name=&image=&state=` | 新增容器；帶 `project=&service=` 則為 compose 容器；帶 `pod=&ns=&container=&image=&port=` 則為一個 pod 的 cri-dockerd 容器（sandbox＋應用容器） |
| `POST /__test/k8s?installed=1&running=1&fail=<step>&delay=<ms>&pods=1` | FakeK8s 場景：已安裝／執行中（`running=1` 同時把設定的 k8s 打開）、在某一步失敗、每步延遲、放入示範 pod |
| `POST /__test/action-delay?ms=` | 讓 Start／Stop 延遲回應（測 pending 回饋） |
| `POST /__test/list-hold`／`POST /__test/list-release`／`GET /__test/list-held` | 卡住下一次 ListImages（測刷新競態／abort） |
| `POST /__test/image?tag=&size=&containers=` | 新增映像（`containers>0` 視為使用中） |
| `POST /__test/volume?name=&in_use=1` | 新增 volume |
| `POST /__test/network?name=&driver=` | 新增網路 |
| `POST /__test/wsl/fail-install[?on=0]`、`GET /__test/wsl/installed` | 讓 FakeWSL 的 `--install` 失敗；查詢已安裝清單 |
| `POST /__test/wsl/fail-resize[?on=0]`、`GET /__test/wsl/last-install`、`POST /__test/wsl/self-in-wsl[?on=0]`、`POST /__test/wsl/fs-min?distro=&blocks=` | 讓 FakeWSL 的 `--manage --resize` 失敗；回傳最後一次 `--install` 的參數；模擬 wsld-gui 從 WSL 內執行；設定 distro 檔案系統的最小區塊數（預設 2 GiB）（FakeWSL 磁碟上限預設 1 TB；啟動過的機器 VHD 保持掛載直到 `--shutdown`） |

FakeEngine 預設資料：容器 web（running）／db（exited）；映像 nginx:1.27（使用中）／redis:7／一個 dangling；volume pgdata（使用中）／orphan；網路 bridge／host／none／shop_default。FakeWSL 預設：Ubuntu-26.04（Running、預設、systemd）與 docker-desktop（Stopped）。

### 實機（Windows）手測項目

Playwright 無法涵蓋 Wails／systray 原生行為；Windows 手測（基底已於 Ubuntu-26.04 + WebView2 實測；0.5 項隨實作補）：

1. `dist\wsld-gui.exe` 開啟後標題列「已連線」，列表顯示容器；頂部分頁切視圖。
2. 按 X → 縮 Tray；Tray「開啟主視窗」可還原；`--tray`／「結束」行為同既有。
3. Settings（頂部齒輪）→「開機自啟動」toggle → HKCU Run；Theme radio 切換可見。
4. Machines：設為 engine／client、Info（tcp／wsl 設定）、folder／terminal；unregister 需打名稱（0.5）。

## 圖示與 Windows 資源

- `cmd/wsld-gui/assets/gen_icon.go`（`go run`／`make icon`）產生 `tray.ico`（16/20/24/32/40/48/64/256；≤64 BMP、256 PNG）與 `icon.png`；Tray 與視窗共用同一圖示。
- `cmd/wsld-gui/winres/winres.json` 以 [go-winres](https://github.com/tc-hib/go-winres) 產生 `rsrc_windows_amd64.syso`（圖示群組 ID 3 = Wails 的 `AppIconID`、Per-Monitor v2 DPI manifest、Common Controls v6、版本資源）。`make winres` 重新產生；`.syso` 已入版控，`go build` 會自動連結。

## 已知限制

- Theme：Light／Dark／System（`data-theme`／`prefers-color-scheme`）。
- Images 的「使用中」欄依 Docker `/images/json` 的 `Containers`；舊版 daemon 可能回 -1，顯示為「–」。
- Machines「新增機器」執行 `wsl --install --no-launch`，並依對話框的 Linux 帳號／密碼建立使用者（密碼經 stdin 交給 `chpasswd`，不保存；留空則不設密碼）、設為 `/etc/wsl.conf` 的預設使用者。安裝完直接「設為主要」納管，同一流程 init + start。「啟動引擎」／「設為主要」失敗原因留在 banner 直到引擎就緒。
- 安裝中對話框輪詢 `GET /api/machines/install/progress`：觀察 `%TEMP%\<distribution>.wsl` 大小，總量取自官方 `DistributionInfo.json`＋HEAD；查不到總量時改為不定進度＋經過時間。同時只允許一個安裝（第二個回 409）。對話框的取消是標題列右上角的 ✕（安裝中停用）；按下安裝後隱藏安裝鈕與密碼提示，把高度讓給進度區塊，1000×640 視窗內不出現捲軸；失敗時兩者恢復可重試。
- Networks：列表＋個別 Remove（內建 disabled）＋清除未使用；使用中回 409。
- npiperelay／自動更新／arm64／**首次 Setup 一鍵**：Roadmap Extra（不綁 0.5）。

## 0.5 實機抽樣驗收

1. 單一 engine：沒有切換；**首次啟動自動建立 docker-engine、init、start 後連線**；其他 distro 以 client 共用。
2. Unregister：需輸入發行版名稱確認；profile 刪除。
3. Containers：Remove running（確認後刪除）；Info summary 欄位齊全。
4. Settings 側欄 Theme；header 無 Settings／tcp；folder／terminal 有 border。
