# 原生 GUI 評估：記憶體用量與 WinUI 3 路線

**狀態**：評估（2026-11-19），尚未定案。本文整理三件事：wsld-gui 實際吃多少記憶體、改成 WinUI 3 能省多少、有哪些做法可選。結論與建議見 [§7](#7-建議與路線)。

相關文件：[GUI.md](GUI.md)（現行 Wails 介面）、[GUI-WINUI.md](GUI-WINUI.md)（Phase 6 純 Rust + WinUI 3 設計，部分內容已過時，見 [§4.2](#42-gui-winuimd-需要修正的地方)）、[ROADMAP.md](ROADMAP.md) Phase 6。

標記說明：**實測** = 在開發機上量到的數字；**估** = 依架構推算，尚未驗證。

## 1. 起點：「工作管理員顯示不到 50 MB」是真的嗎

只算一半。不到 50 MB 的只有 `wsld-gui.exe` 主程序本身。wsld-gui 是 Wails v2，畫面由 WebView2 顯示，WebView2 會另外開好幾個 `msedgewebview2.exe` 子程序，工作管理員常把它們算在別的群組，所以看起來很省。

### 1.1 實測：wsld-gui 完整程序樹（實測）

量測對象：開發機上執行中的發行版 `wsld-gui.exe`，視窗開啟狀態。欄位「私有工作集」= 工作管理員預設的「記憶體」欄。

| 程序 | 類型 | 私有工作集 | 工作集 | Commit |
|---|---|---|---|---|
| wsld-gui.exe | 主程序（Go + Wails） | **46.9 MB** | 88.6 MB | 61.2 MB |
| msedgewebview2.exe | browser | 27.4 MB | 131.6 MB | 39.8 MB |
| msedgewebview2.exe | gpu-process | 72.0 MB | 123.8 MB | 187.9 MB |
| msedgewebview2.exe | renderer（前端頁面） | 34.5 MB | 100.8 MB | 50.2 MB |
| msedgewebview2.exe | utility（network） | 6.3 MB | 42.4 MB | 11.2 MB |
| msedgewebview2.exe | utility（storage） | 2.8 MB | 20.3 MB | 7.3 MB |
| msedgewebview2.exe | crashpad-handler | 1.7 MB | 13.7 MB | 3.0 MB |
| **合計** | | **約 190 MB** | （有重複計算） | 約 360 MB |

- 比較有意義的數字是私有工作集合計 **約 190 MB**。工作集直接加總會超過 500 MB，但共用的 DLL 頁面會被重複計算，不能拿來比。
- 用 `--tray` 隱藏啟動也省不了多少：Wails v2 在視窗隱藏時一樣會建立 WebView2，目前程式碼也沒有縮到系統匣時釋放或暫停 WebView2 的處理。
- 佔最多的是 GPU 程序（72 MB）。

### 1.2 實測：原生 WinUI 3 app 對照組（實測）

兩者都是原生 C++、載入 `Microsoft.UI.Xaml.dll`（WinUI 3），沒有 .NET，也沒有 WebView2，是 Rust 或 C++ 寫 WinUI 3 時很好的參考。

| app | 狀態 | 私有工作集 | 工作集 | Commit |
|---|---|---|---|---|
| 記事本 | 視窗開著，有一份未存檔的 .md | **82.7 MB** | 163.3 MB | 113.1 MB |
| 時鐘 | **沒有任何可見視窗**（程序仍在背景） | **48.6 MB** | 142.6 MB | 65.9 MB |

重點：**WinUI 3 程序就算沒有顯示視窗，XAML 框架載入後仍佔約 45–50 MB，而且不會釋放。** 所以「同一個程序縮到系統匣」在 WinUI 3 下仍是 40–50 MB 等級；要把常駐記憶體壓低，不論用哪種 UI 技術，都要把系統匣和視窗拆成兩個程序。

## 2. 可選的方案

| 方案 | 內容 | 後端 | 改寫範圍 |
|---|---|---|---|
| **A** | 現有架構優化：系統匣與視窗拆成兩個程序、關閉 GPU 加速等 | Go（不變） | 小：只改 `cmd/wsld-gui` |
| **B** | 全部改寫：Rust 核心 + Rust WinUI 3 介面（[GUI-WINUI.md](GUI-WINUI.md) 的 Phase 6） | Rust（改寫） | 全部 |
| **C** | 保留 Go 後端，另寫 WinUI 3 介面（C#、Rust 或 C++） | Go（不變） | 介面全部改寫，Go 端小改 |

### 2.1 記憶體比較（私有工作集）

| 狀態 | 現況 Go + Wails | A：拆成兩個程序 | B：Rust + WinUI 3（單一程序） | C：Go 常駐 + WinUI 3 介面程序 |
|---|---|---|---|---|
| 縮到系統匣（大部分時間） | 約 190 MB（實測） | **約 10–20 MB**（估） | 約 40–50 MB（估，依時鐘實測推算） | **約 10–20 MB**（估） |
| 視窗開著 | 約 190 MB（實測） | 約 190 MB 以上（估） | **約 50–90 MB**（估） | 約 70–130 MB（估，見 [§5.3](#53-介面用哪種語言寫)） |

- 縮在系統匣時，A 和 C 最省。B 若也拆成兩個程序（[§4.4](#44-架構建議)），常駐同樣可以降到約 5–10 MB（估）。
- WinUI 3 真正的優勢只在**視窗開著時**，大約省 100–140 MB。
- 這個 app 開機後通常以 `--tray` 常駐，所以常駐記憶體才是主要矛盾。

## 3. 方案 A：在現有架構內優化

| 做法 | 效果 | 成本與風險 |
|---|---|---|
| **系統匣與視窗拆成兩個程序** | 常駐只剩 Go + systray + lifecycle，估約 10–20 MB；關閉視窗時 UI 程序直接結束，WebView2 全部釋放 | 中：要處理 base／token 交接與單一實例喚醒；打開視窗多一次 WebView2 冷啟動（約 0.5–1 秒） |
| `WebviewGpuIsDisabled: true` | 可能降低 GPU 程序的 72 MB；實際能省多少要實測，Chromium 仍會留一個軟體合成程序 | 改一行；動畫可能變卡 |
| WebView2 `TrySuspend`／`MemoryUsageTargetLevel` | 隱藏時降低用量 | Wails v2 沒有提供這些 API，要碰 go-webview2 的底層物件；效益不如拆程序 |

**拆成兩個程序和要不要改 WinUI 3 無關**，而且 B 和 C 最後也需要同樣的拆法，所以是優先做的一步。

## 4. 方案 B：Rust 核心 + Rust WinUI 3 介面

### 4.1 關鍵變化：微軟推出 Windows Reactor

| | GUI-WINUI.md 原本的設想 | 實際情況（2026-11） |
|---|---|---|
| UI 技術 | `windows-rs` 直接呼叫 `windows::UI::Xaml::*` | 這個 namespace 是舊的 UWP 系統 XAML，不是 WinUI 3（WinUI 3 是 `Microsoft.UI.Xaml`）；而且 `windows` crate 早已移除 XAML 綁定 |
| 直接手寫 WinUI 3 binding | — | 做得到（社群的 `winio-winui3` 用 windows-bindgen 產生），但要自己實作 `IXamlMetadataProvider`、沒有 XAML 編譯器、沒有 `x:Bind` |
| **Windows Reactor** | 文件中沒有（當時不存在） | 2026-05 併入 windows-rs，是微軟官方、以 WinUI 3 為底層的 Rust **宣告式 UI** 函式庫；目前 0.10x **預覽版** |

Reactor 的特性：

- **程式模型**：類似 React，UI 在 Rust 程式碼裡組（不用 XAML 檔），`Component` 的 `update`／`view` 加上 hooks（`use_state`、`use_effect`、`use_resource` 等）管理狀態。
- **元件**：55 個以上，含 NavigationView、虛擬化的 ListView／GridView、ItemsRepeater、TabView、TreeView、ContentDialog、CommandBar、MenuBar、Flyout、TextBox、**WebView2**、**SwapChainPanel**、Canvas。
- **系統匣**：內建 `notify_icon`。
- **背景工作**：`update`／`view` 在 UI 執行緒執行，阻塞工作用 `spawn_background`，完成後回傳 message；**和 tokio 怎麼接沒有說明**。
- **散佈**：單一執行檔約 3 MB；需要 **Windows App SDK 2.0.1 以上**的 runtime，可用 `windows-reactor-setup` 做 self-contained（把 runtime 檔案放在 exe 旁邊）。
- **PR 內附的數字**：Rust 範例工作集 109.5 MB（工作集，不是私有工作集），第一個視窗出現約 160 ms。和記事本實測（工作集 163 MB、私有 83 MB）一致，支持「視窗開著私有約 50–90 MB」的估算。

風險：

- **預覽版**，官方明講 public API 會變，沒有長期支援承諾。WinUI 團隊過去對 Rust 興趣不高；winrt-rs 曾因 WinUI 3 頻繁、未記載的改動而放棄支援。
- **開發機只裝到 Windows App SDK 1.8**：要嘛使用者自行安裝 2.x runtime，要嘛 self-contained。兩者都會失去現在「單一 exe」的散佈方式。
- 社群回報：動畫功能有限、啟動時先黑一下畫面、沒有 DataGrid 類元件。

### 4.2 GUI-WINUI.md 需要修正的地方

1. §1.3、§5 寫的 `windows::UI::Xaml::*` 是錯的 namespace，應改為以 Windows Reactor（或 windows-bindgen 產生的 `Microsoft.UI.Xaml`）為基礎。
2. §6 的「XAML loader 是否可行」「沒有視覺設計器」在 Reactor 下已不是問題；改為「預覽版 API 變動」「WinAppSDK 2.x runtime 散佈」。
3. 文件寫的 Go 測試數 177 已過時，目前是 **242 個**。
4. 階段順序（見 [§4.4](#44-架構建議)）。

### 4.3 改寫規模

| 部分 | 規模 | 難度 |
|---|---|---|
| Go 核心（不含 `internal/gui`、`cmd/`） | 約 5.6k 行，含新的 `internal/lan` | 中：純邏輯為主，有測試可逐一對照 |
| 橋接 server（`internal/gui`） | 約 3.0k 行 | 中 |
| Go 測試 | 242 個，約 5.2k 行 | 工作量大但機械化 |
| 前端 | 3.5k 行 TS（`app.ts` 佔 2.5k）、13 個 Playwright spec | **高**：全部改寫成 Reactor 元件 |
| 終端（目前 xterm.js） | — | **最難**，見 [§6](#6-終端面板) |
| 欄寬拖曳與記憶（`columns.spec`） | — | 中偏高：沒有 DataGrid，要用 Grid + ListView 自己組 |

### 4.4 架構建議

1. **階段順序：GUI spike 移到最前面。** 原本 6a 核心 → 6b 橋接 → 6c GUI spike，等於先做風險最低、工作量最大的部分，最後才知道會不會整個不可行。建議改成 6c' Reactor spike（1–2 週，直接接現有 Go 橋接 API）→ 6a → 6b（可縮減）→ 6d／6e → 6f。
2. **GUI 程序內不必跑 HTTP server。** GUI 和核心都是 Rust、又在同一程序時，GUI 可直接呼叫 `trait Backend`，測試時換 fake；發布的版本不需要 axum、token 驗證與 JSON 序列化。橋接 server 只保留給 testhost 與過渡期的 TS 前端。
3. **仍要拆成兩個程序**（依 §1.2 時鐘的實測）：`wsld-tray.exe`（Rust + Win32 `Shell_NotifyIcon`，不載入 XAML，負責 lifecycle，估約 5–10 MB）與 `wsld-gui.exe`（Reactor 視窗，開啟才啟動、關閉即結束），兩者都直接連結 `wsld-core`。
4. **先決定散佈方式**：self-contained（exe + runtime 資料夾打包成 zip）或要求安裝 WinAppSDK 2.x；影響 `release.yml`、README，doctor 也要多檢查 runtime。

### 4.5 Spike 的 go/no-go 條件

1. Containers 列表 500 筆捲動流暢，能透過現有 Go 橋接 API 啟動、停止容器。
2. 背景執行緒（tokio）能把訊息送進 Reactor 的 `update`。**一定要驗證。**
3. `notify_icon` 系統匣、隱藏與顯示視窗、單一實例鎖。
4. WebView2 元件放 xterm.js 終端，接上現有 exec WebSocket。
5. self-contained 打包後的實際大小，以及在沒裝 runtime 的乾淨 Windows 上能否執行。
6. 量私有工作集：視窗開著與關閉後各一次。
7. 升級一次 Reactor 預覽版，觀察 API 變動造成的修改量，評估長期維護成本。

## 5. 方案 C：保留 Go 後端，另寫 WinUI 3 介面

即 [GUI-WINUI.md](GUI-WINUI.md) 裡的退路（「殼 + core sidecar」）改為主要方案。

### 5.1 為什麼適合

- **`internal/*` 與 Wails 完全沒有耦合**。用到 Wails runtime 的只有 `cmd/wsld-gui/main_windows.go`（顯示／隱藏視窗、Quit、`BrowserOpenURL`）。
- **橋接 API 已經完成**：43 個 handler，加上 logs、exec、machine-exec 的 WebSocket；綁定 loopback、每次啟動隨機埠、Bearer token（`internal/gui/server.go` 的 `ListenLoopback`）。TS 前端本來就只透過這套 API 運作，WinUI 3 介面只是換掉呼叫端。
- **242 個 Go 測試、doctor、lifecycle、區網存取都不用動。** 區網存取本來就要求「只在 wsld-gui 執行時有效（含系統匣）」，由常駐的 Go 程序負責正好符合。

### 5.2 架構

```text
wsld-gui.exe（Go，常駐，不開視窗）          wsld-ui.exe（WinUI 3，需要時才啟動）
├─ fyne systray（系統匣）                  ├─ 主視窗（Containers／Images／Machines…）
├─ lifecycle／dockerapi／lan               ├─ 終端面板：需要時才載入 WebView2 + xterm.js
└─ gui.Server（loopback + token）  ◀──HTTP/WS──┘
   點系統匣 → 啟動 wsld-ui.exe 並交付 base 與 token
   關閉視窗 → wsld-ui.exe 直接結束，記憶體全部釋放
```

Go 端要改的地方：

1. `cmd/wsld-gui` 拿掉 Wails，只留系統匣、server、lifecycle。
2. **base 與 token 交接**：目前以注入 `window.__WSLD__` 的 JS 傳給前端；改成啟動參數、只有使用者本人可讀的檔案，或 named pipe。
3. 單一實例與喚醒：點系統匣時，UI 程序未啟動就啟動，已啟動就帶到前景。
4. `/api/window/{action}` 改由 UI 程序自己處理，或改成通知 UI 程序。
5. 順便解決型別漂移：從 Go struct 自動產生 C#／Rust 型別，取代 `frontend/src/api.ts` 手動維護。

### 5.3 介面用哪種語言寫

| | C# + NativeAOT | Rust + Windows Reactor | C++/WinRT |
|---|---|---|---|
| 成熟度 | **最高**，微軟主要路線 | 預覽版，API 會變 | 成熟，但官方已停止新增功能 |
| 開發體驗 | 最好：XAML、Hot Reload、CommunityToolkit（有 DataGrid） | 中：宣告式、在程式碼裡組 UI | 最差：冗長、編譯慢 |
| 視窗開著（私有，估） | 約 60–110 MB（NativeAOT 比 JIT 省） | 約 50–90 MB | 約 50–80 MB（參考記事本實測 83 MB） |
| 範例與資源 | 最多 | 很少 | 中 |
| 散佈 | WinAppSDK runtime 或 self-contained | 同左（需 2.0.1 以上） | 同左 |

只有 C++/WinRT 有記事本實測可對照，C# 與 Rust 都是推估。§2.1 方案 C「視窗開著約 70–130 MB」= Go 常駐程序約 10–20 MB + 介面程序。

**建議 C# + NativeAOT**：主要目標是常駐記憶體，而常駐時不會載入 UI 程序，C# 多出的幾十 MB 只在視窗開著時才有；換來最成熟的工具、最多的範例，也不用承擔預覽版 API 變動。若更在意全 Rust 或視窗開著時的記憶體，再選 Rust + Reactor。

### 5.4 要注意的地方

- **散佈方式改變**：從單一 exe 變成兩個 exe，加上 WinAppSDK runtime（要求安裝或 self-contained），`release.yml` 與安裝說明都要改。
- **測試**：Playwright 測的是 TS 前端，換成 WinUI 3 後用不上。Go 的 server 測試繼續保護 API 合約；介面改為手動 smoke test。過渡期保留 Wails 版當備用，功能對齊後再移除。
- **版號**：依版本規則屬架構改版，升到 **v1.0.0**（v2.0.0 原本保留給全部改寫成 Rust）。

## 6. 終端面板

WinUI 3 沒有現成的終端控制項，三種做法：

| 做法 | 優點 | 缺點 |
|---|---|---|
| **終端面板內放 WebView2，沿用 xterm.js** | 最務實；現有 exec WebSocket 與前端終端程式可沿用；只在打開終端時載入，關閉即釋放 | 打開終端時記憶體會暫時增加 |
| `alacritty_terminal`（或 `vte`）解析 + SwapChainPanel 自繪 | 效果最好、完全原生 | 工作量很大，幾乎沒有範例 |
| 改成只呼叫外部 Windows Terminal | 最省事，現在已有「Open terminal」 | 失去內嵌的容器 exec 與 logs 體驗 |

建議先用 WebView2 + xterm.js，有需要再考慮自繪。

## 7. 建議與路線

1. **先做方案 A 的「拆成兩個程序」**：Go 常駐程序（系統匣 + server + lifecycle）＋ 暫時沿用 Wails 的 UI 程序。常駐記憶體馬上從約 190 MB 降到約 10–20 MB（估），風險低，而且 B、C 都會沿用這個架構。
2. **WinUI 3 spike（約 1 週）**：以方案 C 為前提，做出 Containers 列表、啟動與停止容器、接上 logs WebSocket；量私有工作集與打包大小（條件可參考 [§4.5](#45-spike-的-gono-go-條件)）。
3. **通過就逐頁改寫**，最後以 WinUI 3 版取代 Wails 版的 UI 程序，發布 v1.0.0。
4. **方案 B（全部改寫成 Rust）不建議為了記憶體而做**。若日後仍有其他理由要走，先依 [§4.2](#42-gui-winuimd-需要修正的地方) 修正 GUI-WINUI.md，並把 GUI spike 移到最前面。

## 8. 待決事項

- 介面語言：C# + NativeAOT 或 Rust + Reactor（[§5.3](#53-介面用哪種語言寫)）。
- 散佈方式：self-contained 或要求安裝 WinAppSDK runtime。
- base／token 交接方式：啟動參數、使用者私有檔案或 named pipe。
- 過渡期是否保留 Wails 版、保留多久。
- `WebviewGpuIsDisabled` 的實測效果（方案 A 的額外選項）。

## 9. 來源

- [windows-rs PR #4479 — Windows Reactor](https://github.com/microsoft/windows-rs/pull/4479)
- [microsoft/windows-rs README](https://github.com/microsoft/windows-rs)
- [docs.rs — windows-reactor](https://docs.rs/windows-reactor)
- [windows-rs PR #1836 — Remove Xaml from `windows` crate](https://github.com/microsoft/windows-rs/pull/1836)
- [microsoft-ui-xaml #2488 — WinUI 3 rust support](https://github.com/microsoft/microsoft-ui-xaml/issues/2488)
- [winio-winui3（docs.rs）](https://docs.rs/crate/winio-winui3/0.4.0/source/src/Microsoft/mod.rs)
- [Windows GUI in Rust — 2026 Guide](https://rust-pc.github.io/rust-windows-gui.html)
