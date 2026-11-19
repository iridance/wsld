# WinUI3 GUI（Phase 6，v2.0 設計文件）

> **2026-11-19 註記**：本文的 UI 技術路線（`windows::UI::Xaml`）與階段順序已部分過時，另有「保留 Go 後端、只換 WinUI 3 介面」的方案，見 [GUI-NATIVE-EVAL.md](GUI-NATIVE-EVAL.md)。

**狀態**：設計階段（Phase 6a 尚未開工）。本文解決 [SPEC.md](SPEC.md) §7 原列的兩個開放項（v2 與 Go core 整合方式、WinAppDriver／UIA 測試策略）——兩者皆已在下方定案。對應 Roadmap 見 [ROADMAP.md](ROADMAP.md) Phase 6a–6f。

**已鎖定的方向**：核心（取代 Go `internal/*`）與 GUI 都改寫成 **純 Rust**；GUI 透過 `windows-rs` 的 WinRT 投影直接寫 **WinUI3／XAML**（不採 C#/.NET 殼）；核心與 GUI／CLI 之間沿用現有的**本機 HTTP+WebSocket 橋接 API**模式（即今天 `internal/gui/server.go` 的角色），不改為 in-process FFI；遷移採**漸進式並行**——現有 Go/Wails 產品持續發布，新架構在同一 repo 旁邊長出來，功能與測試對等後才切換發布管道。

## 1. Workspace／crate 對應

### 1.1 Cargo workspace 佈局

```text
wsld/
  Cargo.toml                 # workspace root（新增，與現有 go.mod 並存）
  crates/
    wsld-bridge/              # lib：共用 API contract 型別（server 與 GUI client 共用）
    wsld-core/                # lib：lifecycle／dockerapi／wsl／config／doctor／i18n／host／procutil
    wsld-bridge-server/       # lib：axum router，對 Backend trait 泛型（真實 core 或 fake）
    wsld-cli/                 # bin：wsld.exe（並行期間為 opt-in 二進位）
    wsld-gui/                 # bin：wsld-gui.exe（WinUI3 殼＋內嵌 bridge server）
    wsld-testhost/            # bin：wsld-gui-testhost 的 Rust 版本
  cmd/、internal/、frontend/  # 現有 Go／TS 樹，並行期間不動
```

新增 `wsld-bridge` 是相對今天 Go/TS 架構的結構性差異：今天 Go server 與 TS 前端各自宣告一份型別（JSON 序列化溝通，編譯期無連結）；Rust 的 server 與（新）Rust GUI client 可以共用同一份 `serde` struct，見 §2.3。

### 1.2 Go package → Rust 對應

| Go package（現況 LOC） | Rust 對應 | 說明 |
|---|---|---|
| `internal/dockerapi`（~1.3k） | `wsld-core::dockerapi` | 沿用手刻 HTTP client（不引入 `bollard`）：`hyper` 1.x + `tokio::net::TcpStream`，需要的介面本來就小（容器 list/start/stop/remove/inspect、log demux、exec 用 hijack 過的 raw socket）。8-byte log frame 解析是協定本身，1:1 照搬。images／networks／volumes（`resources.rs`）同樣手刻。 |
| `internal/host`（~250，Windows-only） | `wsld-core::host`（`#[cfg(windows)]`） | 開 Explorer／Windows Terminal、互動 shell。`net.Pipe` → `tokio::io::duplex()`（語意完全對應）。 |
| `internal/wsl`（~886） | `wsld-core::wsl` | `Runner` trait 直接對應：`trait WslRunner { async fn run(...) -> Result<...>; }`，真實實作包 `tokio::process::Command`。UTF-16LE 解碼改用 `encoding_rs`。distro 列舉／解析／安裝／`RunIn`／使用者建立／`/proc` CPU-mem 取樣皆為純邏輯，直接搬。 |
| `internal/lifecycle`（~768） | `wsld-core::lifecycle` | 狀態機（status／start／init）改成吃 `&dyn WslRunner`、`&dyn Pinger` 的 async 函式／方法——與 Go 今天靠介面做依賴注入、可測試的形狀一致，這個模式是刻意保留、不重新設計的。 |
| `internal/config`（~952） | `wsld-core::config` | `serde` + `toml` crate 取代 `BurntSushi/toml`。`%APPDATA%\wsld\config.toml` 路徑直接用 `std::env::var("APPDATA")`（不需引入 `dirs` 之類的跨平台抽象——本專案本來就只支援 windows/amd64）。多 machine／active、port 分配（`NextAPIPort`／`BusyAPIPorts`）、驗證、舊扁平格式遷移，都是純邏輯，照搬。這是 Go 測試覆蓋最好的套件之一（23 個），適合列為 Phase 6a 第一個移植對象。 |
| `internal/doctor`（~623） | `wsld-core::doctor` | 檢查項目不變（WSL 安裝、distro 版本、systemd、docker.service、API 可達性、只綁 loopback、`tasklist.exe` 偵測 Desktop 衝突）。`tasklist.exe` 一樣用 `tokio::process::Command` shell 出去，CSV 解析照搬。 |
| `internal/procutil`（~85，Windows-only） | `wsld-core::procutil` | `CREATE_NO_WINDOW` 透過 `std::os::windows::process::CommandExt::creation_flags`——這一項其實不需要 `windows-rs`，標準庫就夠。 |
| `internal/cli`（~1.3k） | `wsld-cli`（bin，直接呼叫 `wsld-core`） | 用 `clap`（derive）取代手寫 dispatch。子命令不變：`help version init start status ps logs start-ct stop-ct exec doctor use unmanage unregister`；退出碼 0–4 沿用。CLI 一樣是**直接呼叫 core**（in-process），不透過 HTTP 橋接——橋接 API 是給 GUI／testhost 用的，這點不變。 |
| `internal/i18n`（~500） | `wsld-core::i18n` | key→string 表；`Detect()` 改用 `windows-rs` 的 `GetUserDefaultUILanguage`（型別化呼叫，比 Go 手動 `syscall.NewLazyDLL` 可讀）。 |
| `internal/gui/server.go`＋`resources.go` | `wsld-bridge-server` | 見 §2。 |
| `internal/gui/fake.go`、`fakewsl.go` | `wsld-core::fake`（或獨立 `wsld-core-fakes` crate） | `FakeEngine`／`FakeLifecycle`／`FakeAutostart`／`FakeDistroShell`／`FakeWsl` 逐一對應為 trait 實作。**不放進 `#[cfg(test)]`**——跟今天 Go 的 `fake.go` 一樣是正式檔案，因為 `wsld-testhost` 需要它當 production 依賴。 |
| `internal/gui/configgate.go`、`autostart_windows.go`、`shell.go` | `wsld-core::{configgate, autostart, shell}` | autostart 改用 `windows-rs` 寫 `HKCU\...\Run` 登錄檔；single-instance mutex 改用 `windows-rs` 的 `CreateMutexW`／`OpenMutexW`。 |

### 1.3 外部依賴對應

| Go 依賴 | Rust 對應 | 說明 |
|---|---|---|
| `net/http`（server） | `axum`（+ `tower`／`hyper`） | Router 放在 `wsld-bridge-server`，對一個 `Backend` trait 泛型，讓真實 core 與 `wsld-core::fake` 都能接上——對應今天 Go 的 `Server` 可以用真的 `dockerapi.Client` 或 `FakeEngine` 建構。 |
| `coder/websocket`／`gorilla/websocket` | `axum::extract::ws`（底層 `tokio-tungstenite`） | 不需要額外的 WS crate。 |
| `BurntSushi/toml` | `toml` + `serde` | `Config`／`Machine` 與內部讀寫用的不同 shape（因應舊格式遷移）分開宣告，跟 Go 一樣。 |
| `fyne.io/systray` | `windows-rs` 直接呼叫 `Shell_NotifyIcon`（**不**另外引入 `tray-icon` crate） | 理由：`tray-icon` 之類的第三方 crate 會自己開一個隱藏視窗＋訊息迴圈，容易跟 WinUI3 本身的 `DispatcherQueue` 打架（見 §6 風險）。直接用 Win32 API、把 tray 訊息泵進同一個迴圈，維持單一訊息迴圈的心智模型。 |
| `wailsapp/wails/v2` + `go-webview2` | 整個移除 | 改用 `windows::UI::Xaml::*` WinRT 投影 + Windows App SDK bootstrapper（`MddBootstrapInitialize`），皆透過 `windows-rs`。 |
| `golang.org/x/sys/windows` | `windows-rs` | 同一個 crate 同時提供 Win32（tray、mutex、process flags、語言偵測、登錄檔）**和** WinRT/XAML（WinUI3 本身）——這是純 Rust 路線相對「C# 殼＋Rust core」的具體優勢：今天 Go+Wails 疊了三個依賴（`x/sys/windows` 管 syscall、`go-webview2` 管 UI 嵌入、`systray` 管 tray），Rust 路線只要一個 crate。 |
| `os/exec` | `tokio::process::Command` | 與橋接 server 共用同一個 tokio runtime。 |
| `net.Pipe` | `tokio::io::duplex(bufsize)` | 語意等價（記憶體內成對連接）。 |
| Go `testing` + 手寫 fake | Rust `#[test]`／`#[tokio::test]` + 手寫 trait fake | 刻意不用 `mockall`——今天測試策略的核心就是「小介面＋手寫 fake」，這個模式直接搬，維持一樣的可讀性／可除錯性。只有當某個 trait 的 fake 大到純粫機械化時才考慮 `mockall`。 |
| Playwright | 見 §3.2 | 無法直接搬——WinUI3 沒有 DOM。 |

## 2. 橋接 API 延續方式

### 2.1 路由：1:1 沿用

今天 `internal/gui/server.go` 的每一條路由（見 [GUI.md](GUI.md) 的橋接 API 表）在路徑／方法／回傳形狀上原樣對應到 `wsld-bridge-server` 的 axum route：`GET /api/meta`、`/api/status`、`POST /api/engine/start`、containers／images／volumes／networks／machines 的 CRUD＋prune、`GET|PUT /api/settings`、`GET /api/doctor`、`POST /api/window/{action}`、`POST /api/distro/{action}`。僅供 `wsld-testhost` 使用的 `/__test/*` 控制端點也原樣搬過去，一樣限定在 testhost 二進位裡才註冊（跟今天 `internal/gui` 的 handler 只被 `cmd/wsld-gui-testhost` 掛上一樣）。

Auth（`Authorization: Bearer <token>`，退回 `?token=`）、CORS、錯誤 envelope 不變：

```rust
#[derive(Serialize)]
struct ApiError { error: String, code: ErrorCode }

#[derive(Serialize)]
#[serde(rename_all = "snake_case")]
enum ErrorCode { NotReady, Api, NotFound, Conflict, Usage, Error } // 對應 503/502/404/409/400/…
```

### 2.2 WebSocket：wire format 不變，client 端型別更明確

三個 WS 端點的行為原樣保留：logs stream 純文字 frame 無 envelope；exec／machine-exec 的 server→client 是二進位 PTY 輸出，client→server 則是「原始按鍵位元組（text frame）」或「JSON 控制訊息 `{"type":"resize",...}`（也是 text frame）」混合，今天 Go 端靠「試著當 JSON 解析、看 `Type=="resize"`」判斷。**並行期間 wire format 完全不變**（舊 TS 前端要能繼續對接新 Rust server），但在 `wsld-bridge` 里為新 Rust GUI 明確定義一個型別，不用再猜：

```rust
#[derive(Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
enum ExecClientMsg {
    Resize { cols: u16, rows: u16 },
    // 原始 stdin 位元組仍走獨立的 WS Text frame（wire format 不變）
}
```

### 2.3 具體簡化：server 與（新）GUI client 共用同一份型別

今天 Go 的 `Container`／`ContainerDetail`／machine／settings／doctor 等 struct 序列化成 JSON 後，TypeScript 前端（`frontend/src/api.ts`）**手動重新宣告**一份對應型別，兩邊沒有編譯期保證會一致——這是既有架構的一個已知風險（型別漂移）。改成純 Rust GUI 後，`wsld-bridge` 變成**唯一的型別來源**：`wsld-bridge-server`（handler）與 `wsld-gui`（native client）都依賴同一份 `serde` struct，`serde_json` 負責序列化，但編譯器保證兩邊對同一筆資料的理解完全一致。這是純 Rust 選擇相對「C# 殼＋Rust core」的另一個具體好處：C# 殼仍然需要自己宣告一份對應型別（或產生 binding），無法直接共用 Rust struct。

### 2.4 不變的部分

只綁 loopback、每次啟動隨機埠、per-run Bearer token、單一 HTTP+WS server 綁定單一執行中的 GUI 行程——皆不變。GUI 行程依然是**內嵌** server（跟今天 `wsld-gui.exe` = 視窗＋tray＋內嵌 server 單一 exe 一樣），不是獨立 daemon；這點是讓 `wsld-testhost` 能在完全不需要 Windows/WSL 的情況下服務相同 API 的前提，必須保留。

## 3. 測試策略

### 3.1 單元／整合測試：直接對應現有 177 個 Go 測試

Go 測試套件的策略是「小介面做依賴注入＋手寫 fake」（`wsl.Runner`、`lifecycle.Pinger`、`doctor.Pinger`），這個設計本身不需要因為換語言而改變：

| Go 套件（測試數） | Rust 對應 | Fake 策略 |
|---|---|---|
| `cli`（28） | `wsld-cli` 測試 | 注入 fake `wsld-core` backend，斷言退出碼／stdout 跟今天一樣。 |
| `config`（23） | `wsld-core::config` 測試 | 純邏輯，不需要 fake——TOML round-trip、port 分配、舊格式遷移可直接沿用同一批 fixture。 |
| `dockerapi`（28） | `wsld-core::dockerapi` 測試 | 起一個本機 `tokio` TCP listener，模擬 Docker Engine API 的最小 HTTP 行為。 |
| `lifecycle`（17） | `wsld-core::lifecycle` 測試 | fake `WslRunner`＋`Pinger`，情境不變（喚醒 distro、systemd 檢查、自動 bootstrap、等待 API、`Init` 流程）。 |
| `doctor`（14） | `wsld-core::doctor` 測試 | fake `Pinger`＋fake `tasklist.exe` runner。 |
| `wsl`（13） | `wsld-core::wsl` 測試 | fake `WslRunner`；UTF-16LE 解碼測試改用 `encoding_rs` fixture。 |
| `i18n`（8） | `wsld-core::i18n` 測試 | table-driven 測試照搬。 |
| `host`（3） | `wsld-core::host` 測試 | fake shell／open-folder backend。 |
| `procutil_windows`（3） | `wsld-core::procutil` 測試 | 斷言 creation flags bitmask。 |
| `gui`/`server`（30）＋`configgate`（1） | `wsld-bridge-server` 整合測試 | 用 fake backend（`wsld-core::fake`）起 axum `Router`，透過 `tower::ServiceExt::oneshot` 或真實綁定的 listener + `reqwest`/WS client 測試，對應今天 `httptest.NewServer` + WS client 的測試方式，覆蓋同樣的 30 個情境（路由形狀、錯誤碼、WS framing）。 |

每個套件的移植都應該對照一份「Go 測試函式 → Rust 測試函式」checklist（見 [ROADMAP.md](ROADMAP.md) Phase 6a／6b 的完成定義），而不是只看測試數量。

### 3.2 Playwright 替代方案：**不採 WinAppDriver／UIA**

這是 [SPEC.md](SPEC.md) §7 原本列為開放的第二項，現在定案：

- **為什麼不採 WinAppDriver／UIA**：WinAppDriver 目前是 Microsoft 維護強度較低的舊工具，且 WinUI3 的自訂 XAML 控制項在 UIA tree 上有已知的不完整案例；要跑 WinAppDriver／UIA 需要真正的 Windows 機器（VM 或實機），這違背了今天 `wsld-gui-testhost` 存在的目的——讓 CI 不需要 Windows/WSL 就能驗證業務邏輯。真實視窗的 focus／timing 也讓這類測試天生較 flaky。
- **改採的方案**：自動化測試只覆蓋橋接層（`wsld-bridge-server` 的 contract test，對應今天 30 個 `internal/gui` server 測試的行為：路由形狀、錯誤碼、WS framing、fake engine 狀態轉換）；原生 XAML UI 的驗證改成**每個 Phase 6 子階段退出前的手動／腳本化 smoke test checklist**（延伸 [GUI.md](GUI.md) 現有「實機（Windows）手測項目」的作法，而非新建一套自動化框架）。
- 今天 10 個 Playwright spec 檔涵蓋的內容裡，屬於業務邏輯行為的部分（confirm 對話框、pending 狀態、資料正確性）改由橋接 contract test 覆蓋；屬於視覺/DOM 專屬的部分（欄寬拖曳持久化、i18n 文字渲染、theme token、drawer/panel 版面）在沒有 DOM 的世界裡本來就不是同一種東西——這部分改由 XAML 的編譯期 binding 檢查＋手動 smoke test 把關，**這是測試涵蓋範圍的真實縮減，不是暫時的**（見 §6 最後一項風險）。若日後手測證明不敷使用，再重新評估 WinAppDriver／UIA，但不預先建置。

### 3.3 `wsld-testhost` 是否還有意義？

**有，且不受影響。** 它的價值從來不是「在 CI 跑真實 UI」，而是「用 fake engine 服務同一套橋接 API，讓業務邏輯／API 合約可以在沒有 Windows/WSL 的環境下驗證」。這個價值在 WinUI3 世界完全保留：`wsld-testhost` 變成驗證 `wsld-core` + `wsld-bridge-server` 對等性的 CI 關卡（§3.1 那 30 個情境），做不到（也從來沒打算做到）的是驗證原生 XAML 視窗畫面本身——那個缺口由手動 smoke test 填補，不是 testhost 的職責。

## 4. 分階段遷移（Phase 6a–6f）

編號延續 Roadmap 既有「Phase 0–5」的交付／完成定義／退出條件寫法，完整 checklist 見 [ROADMAP.md](ROADMAP.md)。此處列各階段目標與關鍵風險把關點：

- **6a — Rust core + CLI（並行、opt-in 二進位）**：`wsld-core`＋`wsld-cli` 對齊現有 177 個 Go 測試涵蓋的行為；與現有 Go CLI 共讀寫同一份 `%APPDATA%\wsld\config.toml`（並行期間兩者不能互相破壞對方）；不含任何 GUI 工作——這是風險最低、測試覆蓋最好的一塊，也是驗證 Rust／Windows 工具鏈（跨編譯、`tokio::process` 對真實 `wsl.exe` 的行為、TOML 相容性）最早的機會，在引入任何 UI 風險之前先過這一關。
- **6b — Rust 橋接 server（`wsld-bridge`＋`wsld-bridge-server`＋`wsld-testhost`）**：退出條件是**現有、未修改**的 TS 前端＋現有 10 個 Playwright spec 直接對新的 Rust testhost 跑過且全過，證明橋接層是 Go 版的位元組相容替代品，且完全不涉及原生 UI 風險。
- **6c — WinUI3 proof-of-concept（time-boxed spike）**：在投入更多工程量之前，先做一個最小可行的純 Rust WinUI3 視窗（`windows-rs` + Windows App SDK bootstrap、一個 XAML 頁面、一個按鈕），同時 spike tray icon（`Shell_NotifyIcon`）能否跟 XAML 的 `DispatcherQueue` 共存在同一行程。**這是 go/no-go 關卡**：具體驗證 UI thread 的 XAML 訊息迴圈、背景 tokio runtime、tray 訊息泵三者能否共存不死鎖；也決定封裝模式（MSIX vs unpackaged）。**中止準則**：若這裡證明 `windows-rs` 的 WinUI3 支援在合理工作量內不可行，退路是切到 C#/.NET 殼＋Rust core 透過 FFI／sidecar 整合——這個決定要在 6c 就做,不要拖到 6d／6e 才發現問題。
- **6d — WinUI3 殼＋核心視圖（Containers／Images／Volumes／Networks）**：建原生 XAML 版面骨架（header／side nav／main／info drawer／footer，對應 [GUI.md](GUI.md) 的版面圖），先做這 4 個最單純的視圖（列表＋列操作＋info drawer），透過共用的 `wsld-bridge` 型別呼叫內嵌 server。
- **6e — Machines／Settings／Logs-Exec terminal／tray／single-instance／autostart**：剩下較吃 Windows 整合的部分。Logs／Exec 面板需要一個 xterm.js 對應物——WinUI3 沒有現成的終端模擬器控制項，可能需要用 `vte`／`alacritty_terminal` 的 parser 驅動一個自訂 XAML 控制項，**這件事本身要另開一個子 spike**，因為目前任何語言都幾乎沒有現成範例。
- **6f — Parity 驗證與切換**：兩產品並行發布一段時間（比照本專案「先出基底、再疊打磨階段」的節奏）；切換前的 parity 關卡＝每個 Go 測試情境都有對應且綠燈的 Rust 版本、每個 Playwright spec 的行為斷言都有對應的橋接 contract test 或手動 smoke test 項目簽核、SPEC §6 驗收清單在真機上對 Rust/WinUI3 build 重跑一次全過。**cutover 的具體定義**：Rust/WinUI3 build 成為既有發行管道（`.github/workflows/release.yml`）下發布的二進位，檔名沿用 `dist/wsld.exe`／`dist/wsld-gui.exe`，版號跳到 **v2.0.0**；Go/Wails 版不立即刪除，先進入至少一個發行週期的 deprecation 通知，觀察無迴歸後才把 `cmd/`／`internal/`／`frontend/` 的舊碼移除。

## 5. Windows 專屬程式碼對應

| 今天（Go，Windows-only） | Rust／`windows-rs` |
|---|---|
| `internal/host/host_windows.go`（開 Explorer／Windows Terminal） | `windows::Win32::UI::Shell::ShellExecuteW` |
| `internal/host/sys_windows.go`（process attr） | `windows::Win32::System::Threading` 的 process-creation flags，或直接用 `std::os::windows::process::CommandExt`（不需要 `windows-rs`） |
| `internal/procutil/procutil_windows.go`（`CREATE_NO_WINDOW`） | 同上，`CommandExt::creation_flags` 即可 |
| `internal/i18n/detect_windows.go`（`GetUserDefaultUILanguage` via `syscall.NewLazyDLL`） | `windows::Win32::Globalization::GetUserDefaultUILanguage()`——型別化呼叫，比原始 DLL/proc lookup 更安全 |
| `internal/doctor/doctor.go`（`tasklist.exe`） | `tokio::process::Command`（本來就是 shell 外部工具，不是原生 API 呼叫，Go／Rust 做法一樣） |
| `internal/config/config.go`（`%APPDATA%` 路徑） | `std::env::var("APPDATA")`（不需要額外 crate） |
| （新增）Tray icon | `windows::Win32::UI::Shell::{Shell_NotifyIconW, NOTIFYICONDATAW}`——取代 `fyne.io/systray` |
| （新增）Single-instance mutex | `windows::Win32::System::Threading::{CreateMutexW, OpenMutexW}` |
| （新增）Autostart 登錄檔 | `windows::Win32::System::Registry::{RegOpenKeyExW, RegSetValueExW}` on `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` |
| （新增）WinUI3 殼本身 | `windows::UI::Xaml::*` WinRT 投影 + Windows App SDK bootstrapper（`MddBootstrapInitialize`） |

最後一列是純 Rust 路線值得點名的優點：同一個 `windows-rs` crate 同時提供上面所有 Win32 呼叫**和** WinRT/XAML UI 層，而今天 Go+Wails 疊了三個依賴才做到同樣的事（`x/sys/windows` 管 syscall、`go-webview2` 管 UI 嵌入、`systray` 管 tray）。

## 6. 風險（誠實列出，不預先淡化）

1. **工具鏈不成熟**：`windows-rs` 的 WinUI3/XAML 支援本質上是從 Windows metadata 產生的 WinRT 投影，功能上算完整，但沒有像 C#/.NET 那樣的視覺化 XAML 設計器，社群範例也少，Rust 特有的 threading／生命週期銜接方式較少被驗證過。**緩解**：6c 的 spike 就是刻意設計來早期發現這個問題，且已經定好中止準則（退回 C# 殼＋FFI），不是等到投入更多才發現。
2. **COM apartment／threading**：WinUI3 的 `DispatcherQueue` 期待 UI 工作在單一 STA 執行緒；橋接 server 需要 tokio 的非同步 runtime；tray icon 的 `Shell_NotifyIcon` 訊息處理也不能跟 XAML 的訊息迴圈搶同一個視窗類別。**預設方案**（待 6c 驗證）：UI 執行緒專職 `DispatcherQueue` 與 tray 訊息泵；一個背景的多執行緒 `tokio::runtime::Runtime` 專職跑 axum/hyper／`wsl.exe` process spawn；兩邊透過 channel（`tokio::sync::mpsc`）或 `DispatcherQueue::TryEnqueue` 溝通，不共享可變狀態。
3. **沒有視覺設計器**：版面（header／side nav／main／drawer／footer）要靠手寫 `.xaml` 檔案透過 WinRT XAML loader 載入，或退而用程式碼直接呼叫 `Windows.UI.Xaml.Controls` 建構子——6c 要驗證前者是否可行。
4. **社群範例少**：多數 `windows-rs` 的 UI 範例是 Win32/GDI，不是 WinUI3/XAML；terminal widget（§4 Phase 6e）尤其是任何語言都幾乎沒有現成範例的一塊，時程估算要單獨考慮，不能假設能抄到 Rust 範例。
5. **封裝模式未定**：MSIX 打包（簡化 Windows App SDK 執行期相依性解析，但改變今天「單一 .exe」的散佈模式）vs unpackaged（維持單一 .exe，但要處理 Windows App SDK 執行期是否存在，類似今天 WebView2 執行期的檢查）——留給 6c 具體決定，因為會影響 `.github/workflows/release.yml` 與安裝說明。
6. **測試涵蓋縮小是永久的，不是暫時的**：沒有 DOM 就沒有 Playwright 等級的原生 UI 自動化測試，§3.2 的橋接 contract test＋手動 smoke test 是長期方案，不是「之後補上 UIA 自動化」的過渡狀態——除非手測實際證明不敷使用才重新評估。
