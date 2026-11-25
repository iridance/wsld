# wsld Roadmap

版本：0.17.1（git tag `v0.17.1`）  
對應規格：[SPEC.md](SPEC.md)（**0.17.1 已交付**；Phase 0–4 基底已落地）

## 總覽

```text
Phase 0  規格／倉庫骨架 + 早期決策鎖定     ✅
Phase 1  Bootstrap 腳本可手動跑通         ✅
Phase 2  共用核心 + wsld CLI              ✅
Phase 3  GUI 基底（Wails + Playwright）   ✅
Phase 4  打磨（文件／Tray 自啟／LICENSE） ✅  → 基底就緒
Phase 5  0.5 主線：多 distro＋GUI 補完    ✅  → 語意化版號 **0.5.4**
Extra    延後能力（含首次 Setup；k3s 見「後續：本機 Kubernetes」） ← 有需求再排
Phase 6  Rust core + WinUI3（純 Rust）      ← 之後 v2.0，設計見 GUI-WINUI.md
  6a Rust core + CLI（並行 opt-in）
  6b Rust 橋接 server + testhost
  6c WinUI3 PoC spike（go/no-go 關卡）
  6d WinUI3 殼 + 核心視圖
  6e Machines/Settings/Terminal/Tray
  6f Parity 驗證 + cutover（→ v2.0.0）

0.5 = Phase 5 完成定義全過（見下方）；**0.6.0**（`v0.6.0`）= Info／Networks 等後續打磨；**0.7.0**（`v0.7.0`）= GUI 全面改版＋新圖示；**0.8.0**（`v0.8.0`）= 頂部導覽＋自繪輸入元件；**0.8.1**（`v0.8.1`）= 新增機器下載進度；**0.8.2**（`v0.8.2`）= openSUSE 支援＋新機器免手動建使用者；**0.8.3**（`v0.8.3`）= 新增機器設定 Linux 帳號與密碼；**0.9.0**（`v0.9.0`）= 區網存取（mirrored＋Hyper-V 防火牆）；**0.10.0**（`v0.10.0`）= 區網存取改為 wsld-gui 內建轉發器＋還原 NAT；**0.11.0**（`v0.11.0`）= 區網存取的防火牆授權（UAC 提權 wsld-gui 自身加規則）＋容器列地球按鈕＋自繪下拉選單；**0.11.1**（`v0.11.1`）= 修正 Kali 等 Debian 衍生版 bootstrap 404＋WSL 發行版分類表（`scripts/lib/distros.tsv`）＋「設為目前」改稱「設為主要」（未單獨發布，併入 v0.12.0）；**0.12.0**（`v0.12.0`）= Machines 卡片啟動／停止發行版＋wsld-gui 啟動時請求防火牆授權（區網存取開關只改設定）；**0.13.0**（`v0.13.0`）= 防火牆授權改為使用者開啟區網存取時才詢問：wsld 先說明規則再提權 netsh（有封鎖規則時改用 wsld-gui helper），未授權前不在內網 IP 監聽；**0.14.0**（`v0.14.0`）= 容器列瀏覽器按鈕常駐：區網存取關閉或無轉發時以 `http://localhost:<埠>` 開啟；**0.15.0**（`v0.15.0`）= 各頁磁碟用量卡：容器／compose、映像、Volume、Machines（ext4.vhdx）每項佔用的長條與總計（`/system/df`）＋容器／Volume 大小欄；**0.15.1**（`v0.15.1`）= 新增機器對話框：取消改為右上角圖示、安裝開始後隱藏安裝鈕，安裝中不再出現捲軸；**0.15.2**（`v0.15.2`）= 磁碟用量改為頁標題旁的用量鈕（迷你長條＋總量，點開看每項），不再佔列表上方的高度；全域窄版捲軸（跟隨主題配色）；**0.16.0**（`v0.16.0`）= 本機 Kubernetes（k3s；已自 main 移出，見下方「後續：本機 Kubernetes」）；**0.17.0**（`v0.17.0`）= 機器磁碟上限：新增機器可指定虛擬磁碟上限（`wsl --install --vhd-size`，預設 60 GB；`wsld init --vhd-size`），既有機器可在 Info 或以 `wsld resize-disk` 調整（`wsl --manage --resize`；VHD 被占用時需經同意 `wsl --shutdown`，完成後重啟原本執行中的機器）；機器卡片「釋放磁碟空間」與 `wsld compact-disk`（`wsl --manage --compact`），調整大小後也一併壓縮；wsld-gui 閃退寫入 `%APPDATA%\wsld\crash.log`，卡片與 Info 顯示上限（讀 VHDX 標頭，不喚醒）；**0.17.1**（`v0.17.1`）= 修正開啟已停止機器的 Info（及每 3 秒的用量輪詢）會執行 `wsl -d … cat /etc/wsl.conf` 而把機器啟動：停止中不進 distro 執行命令，Info 顯示「啟動機器後才能查看 /etc/wsl.conf」、停止中不輪詢，機器啟動／停止後 Info 自動更新；FakeWSL 改為任何 `-d <name>` 命令都會喚醒 distro（可用 `WakeExempt` 豁免），測試能抓到誤喚醒；**0.17.2**（`v0.17.2`）= 機器卡片圖示改為各 distro 的 logo（依名稱比對，認不出來維持通用圖示）；修正主要機器啟動後約 20 秒被 WSL 回收：實測 `wsl -d <distro> -- true` 之類一閃即逝的命令結束後，即使 `systemd=true`，distro 約 20 秒就回到 Stopped（systemd 服務不算 session）。wsld-gui 改為替主要機器掛一個常駐 `wsl -d <distro> -- sleep infinity`（`wsl.KeepAlive`）：設為主要／啟動主要／狀態輪詢發現主要在執行時掛上，換主要時放掉舊的；使用者明確停止、`wsl --shutdown`、結束 wsld-gui 時放掉；非主要機器不保活；**0.19.0**（`v0.19.0`；不含 k8s，k8s 在 `feat/k8s`）= 各頁首次載入前顯示對齊資料版面的 loading 佔位（Containers／Images／Volumes／Networks／Machines）；App 啟動時自動連線（帶起引擎，等同按 Start engine）；**0.19.1**（`v0.19.1`）= engine 卡片顯示 engine 版本、拿掉 icon；Machines 非 engine 群組標題改為「已安裝」；預設 engine distro 改名 `docker-engine`
之後 v2.0 = Phase 6（6a–6f）
Extra ≠ 0.5／v2.0 完成條件
```

---

## Phase 0 — 規格與倉庫

- [x] 倉庫結構、README、SPEC、ROADMAP、ARCHITECTURE、config.example
- [x] GUI 納入主線；WinUI 列 Phase 6
- [x] TDD + UI 測試 rule
- [x] grill-me 關閉早期開放問題並回寫 SPEC §2

**退出條件**：可開始 Phase 1。

---

## Phase 1 — Bootstrap

**目標**：腳本裝好 Ubuntu + rootful dockerd + `127.0.0.1:23750`（可換埠）。

### 交付

- `scripts/01-enable-wsl.ps1`
- `scripts/02-bootstrap-docker.sh`（CE + Compose plugin + linger + hosts）
- `docs/BOOTSTRAP.md`

### 完成定義

- [x] 手動跑通；Windows `curl http://127.0.0.1:<port>/_ping` → `OK`（2026-09-06 Ubuntu-26.04 實測）
- [x] WSL `docker info`／`docker compose version` 成功（CE 29.8.0／Compose v5.5.1）
- [x] 只綁 loopback；占用時能選下一埠（bats 覆蓋 + doctor 檢查）

---

## Phase 2 — Core + `wsld` CLI

### 交付

- Go module；`cmd/wsld`；`internal/{lifecycle,dockerapi,config,doctor}`
- `%APPDATA%\wsld\config.toml`
- 命令：`init`／`start`／`status`／`ps`／`logs`／`start-ct`／`stop-ct`／`doctor`（退出碼 0–4）
- TDD；`docs/TESTING.md`（Go 部分）

### 完成定義

- [x] SPEC CLI 行為與退出碼（`internal/cli` 測試覆蓋 0–4）
- [x] 無 Desktop 可驗證生命週期＋API（實機 init → status → ps/logs/stop-ct/start-ct）
- [x] 自動換埠寫入 config（bootstrap 輸出 `WSLD_API_PORT` → `lifecycle.Init` → `config.Save`）

---

## Phase 3 — GUI 基底（Wails + Playwright）

### 交付

- `wsld-gui.exe`（Wails v2）：Tray、列表、Start／Stop、Logs（100／Live）、**Exec 終端**、狀態列
- 刷新：前景間隔可設定；Tray／失焦暫停
- 側欄多視圖（Containers／Images／Volumes／Networks／Machines）、header／footer、純圖示列操作、compose 分組
- `docs/GUI.md`；Playwright；與 `internal` 共用，禁止雙重邏輯

### 完成定義

- [x] Wails v2 + systray；`internal/gui` HTTP/WS 橋接；實機驗證見 docs/GUI.md
- [x] UI 測試一鍵可跑（`make test-ui` + `go test ./internal/gui`）

---

## Phase 4 — 打磨（基底）

### 交付

- 錯誤字串（WSL／systemd／埠／Desktop）
- README 快速開始（CLI + GUI）
- 登入起 Tray：**可選、預設關**
- 重開機文件、Desktop 並存建議、已知限制
- `LICENSE`（MIT）

### 完成定義

- [x] SPEC §6 基底驗收（2026-09-06）：CLI／GUI 同引擎、冷開→Start→列表／Logs／Exec、`$HOME` bind mount、`wsld start` 恢復、doctor／換埠、`make test`、OPERATIONS、MIT

> 註：當時曾標為「v1.0」；**0.5 起改為功能定義開放中**，以 Phase 5 完成定義為下一里程碑，不再宣稱產品已鎖死 v1.0。

---

## Phase 5 — 0.5 主線（已交付）

**目標**：多 distro（Machines 切換作用於全部功能）＋ GUI／設定／Info 補完與打磨；TCP 引擎模型不變。

### 交付

#### 多 distro

- Config：多 machine profile＋單一 `active`；舊扁平 `config.toml` **自動遷移**
- 全局設定：`lang`、`tray_autostart`、`refresh_interval_sec`、`exec_shell`、`theme`
- 每 machine：`distro`、`user`、`api_endpoint`（各自埠，避免並行 dockerd 搶埠）
- GUI：Machines「設為主要」；CLI：`wsld use <distro>`（同寫 config）
- 「設為主要」即寫**占位 profile**；未 init 可 active → 橫幅提示；**Start engine**（或 `wsld init`）自動 bootstrap／補齊 endpoint／user／port 後再拉起引擎
- 切換 active：不停舊引擎；硬切 Logs／Terminal／Info／快取／pending
- 取消管理：一律先停該 distro docker → 刪 profile（不刪 WSL）
- Unregister 發行版：GUI＋CLI；輸入 distro 名稱雙重確認

#### Machines／Containers UI

- Machines actions：設為主要、Open folder、Terminal（**底部 panel**、該 distro WSL shell、cwd＝user home）、取消管理、unregister、Info（右側 drawer）、Add distro
- Machines Info（右側，對齊 Containers）：基本欄、`.wslconfig` 的 memory／processors、`/etc/wsl.conf`、該 machine 的 **API endpoint（tcp）**、CPU／記憶體用量摘要（非 process 表）；取代 ⓘ dialog
- Containers：Remove（running 可刪，確認會停止）；Info summary：Name、ID、Image、State、Created、Started、Command、Ports、Networks（+IP）、Mounts、Labels（可摺疊）、Restart policy

#### Settings／header

- Side nav 新增 **Settings**（承載 refresh、exec_shell、tray_autostart、**Theme：Light／Dark／System**）
- 移除 header Settings 鈕與 header 上的 tcp 路徑
- Header 保留：distro 名、Open folder／Open terminal（**有 border**）、連線徽章、Start engine

#### UI 打磨

- 各表 actions 列與其他欄列高／格線對齊
- Side nav 按鈕上下間距＝左右間距

#### 文件／測試

- 回寫 SPEC／OPERATIONS／GUI／README；Go 單測＋Playwright＋實機驗收條列

### 完成定義

- [x] 多 profile＋active；遷移舊 config；每 machine 自有埠
- [x] GUI「設為主要」與 `wsld use` 同步；切換硬切面板／快取；未 init active 顯示未就緒
- [x] 取消管理（停 docker＋刪 profile）；unregister（打名稱確認）
- [x] Machines：folder／terminal（home）／右側 Info（含 `.wslconfig` memory／processors、wsl.conf、tcp、用量摘要）
- [x] Containers：Remove（含 running）；Info summary 欄位齊全（必要時 inspect）
- [x] Settings 側欄＋theme；header 無 Settings／無 tcp；header icon 有 border
- [x] 列高／格線與 side nav 間距打磨
- [x] 相關 Go 測試＋Playwright 通過；實機抽樣驗收（切換 distro、unregister 確認、Remove running）

**退出條件**：上列完成定義全勾；產品敘事標 **0.5.4** 已交付（git tag `v0.5.4`）。後續 **0.6.0**（`v0.6.0`）見發行說明。

> 曾列在 P5「延後」的項目改見下方 **Extra**；Compose 專案視圖已於基底完成，不進 Extra。

---

## Extra — 延後能力（不綁版本）

不進 0.5／v2.0 完成定義；有明確需求再排期。下列可分開交付。

### Setup 應用（首次一鍵）

**目標**：還沒開過 WSL 的使用者，用一鍵走完「啟用 WSL →（必要時）重開機 → 自動接續 `init`」，不必自己找 `01-enable-wsl.ps1`。

#### 交付

- 獨立 setup 入口（例如 `wsld-setup.exe`，或 `wsld-gui`／`wsld` 首次偵測到未就緒時進入的 wizard）
- **一鍵啟用 WSL**：等同 `01-enable-wsl.ps1`（Optional Feature、`wsl --update`、預設 WSL2；需 **UAC 提權**）
- 若需重開機：寫「待續」狀態（例如 RunOnce／登入自啟／本地 pending flag），**重開機後自動跳出** setup，接著跑下一步
- 重開後步驟：確認 `wsl.exe` 可用 →（必要時）`wsl --install` Ubuntu → 引導首次進 distro 建使用者（若尚未建立）→ 呼叫既有 **`wsld init`**（bootstrap + 寫 config）→ 可選直接 `start`／開主視窗
- 冪等：WSL／distro／config 已就緒則跳過對應步驟；失敗給明確下一步（對齊 doctor 文案）
- 文件：README／OPERATIONS 標「建議用 Setup」；保留腳本給進階／CI

#### 完成定義

- [ ] 無 WSL 功能：一鍵 → UAC → 啟用 → 提示重開；重開後 **自動再開 setup** 且狀態接得上
- [ ] 重開後可一路到 `init` 成功（有 config、API 可 ping）；不需使用者再手動跑 `01-enable-wsl.ps1`
- [ ] 已裝好 WSL／已 init：再開 setup 不重複破壞性動作
- [ ] 首次 distro 建使用者若必須互動：UI 明確引導，不假裝全自動略過
- [ ] 相關測試（至少核心狀態機／pending flag；UI 能測的用 Playwright 或同等）

**退出條件**：上列完成定義全勾；**不**阻擋 Phase 5／6。

### 後續：本機 Kubernetes（k3s）

> **狀態：已自 main 移出，在 `feat/k8s` 分支維護，不隨下次 release 發布**（0.16.0–0.19.0 曾發布）。main 不含 k8s 頁、`wsld k8s`、k8s 設定與 doctor 檢查。

**方向**：單節點 k3s 跑在共用 engine 上（`--docker` 與 dockerd 共用映像檔，不必架 registry），Windows 側 `kubectl` 透過 `127.0.0.1:6443` 連線；定位對標 OrbStack，多節點列為日後可選的 Cluster 進階模式。

後續項目：

- [ ] 在 `feat/k8s` 對齊單一 engine 後重新合併（k3s 只裝在 engine；一律由 engine 啟停）
- [ ] 單節點流程重新設計（0.20.0 候選，分支 `feat/k8s-single-node-flow`）：Off 時中央 hero、Enabling 步驟清單、Deploy／Apply YAML、Import（Compose → k8s stack）
- [ ] 實機驗收：GUI Kubernetes 頁手測、Windows 端 `kubectl.exe`、`wsl --shutdown` 後恢復
- [ ] 區網存取轉發 NodePort／LoadBalancer（現有轉發器只看 docker published 埠）
- [ ] Enabling log 尾端、⋯ 選單、異常 namespace 的「CrashLoop 原因」

不做：YAML 編輯器、多叢集、k8s 版本切換、預設啟用 Traefik／Ingress。

### 其他候選

- [x] 區網存取：0.9.0 以 mirrored＋Hyper-V 防火牆交付；0.10.0 改為 wsld-gui 內建轉發器（Windows 原生防火牆確認、不重啟 WSL），並提供 `wsld lan restore-nat` 還原 mirrored；0.11.0 開啟時以 UAC 替 wsld-gui 加入防火牆規則（私人＋公用、LocalSubnet），設定頁顯示授權狀態；0.12.0 改為 wsld-gui 啟動時請求授權，開關只改設定；0.13.0 改回使用者操作時才請求：先顯示 wsld 說明對話框，再提權 Windows 內建 netsh（UAC 顯示 Microsoft 已驗證的網路命令殼層），未授權前不監聽（見 SPEC §4.5）
- [ ] npiperelay（Windows named pipe ↔ 引擎）
- [ ] 自動更新（`wsld`／`wsld-gui` 本體）
- [ ] `wsld import`：從 Docker Desktop 把映像檔、volume、容器搬進 wsld 引擎（參考 Kiri 的 `kiri import`）
- [ ] Local domains：每個容器 `http://<name>.wsld.local`、Compose 為 `<service>.<project>.wsld.local`，可選 HTTPS（自家 CA）（參考 Kiri 的 `.kiri.local`）
- [ ] arm64 正式支援
- [ ] 專用隔離 distro 旗標（由下方「共用 engine」取代）

### 共用 engine（專用 dockerd distro）

動機：每個 distro 各裝 dockerd 造成 image／volume 重複。設計：一個 engine distro 跑 dockerd，其餘 client 只裝 CLI，經 `/mnt/wsl/wsld/docker.sock`（WSL2 所有 distro 共用的 tmpfs）連線；WSL2 共用 network namespace，published ports 在所有 distro 與 Windows 都可達。

階段 1（分支 `feat/shared-engine-distro`，尚未發版）：

- [x] `Machine.Role`（engine／client）與設定驗證：最多一台 engine
- [x] bootstrap `--role engine|client`、`wsld init --role`
- [x] engine 保活（wsld-gui）
- [x] doctor：`engine_socket`、`client_<distro>`
- [x] GUI Machines：Engine／Client 標籤與設定按鈕
- [x] 只剩單一 engine：舊設定載入時遷移（一律新增專用 `docker-engine` 並於啟動時自動建立；原有已 init 的 distro 全部變 client、資料保留，再以 `wsld migrate` 搬進 engine）；移除 `wsld start`／`wsld use`、GUI「設為主要」「啟動引擎」
- [x] 啟動即啟動：GUI 開啟自動帶起（首次建立 `docker-engine`）、Supervise 持續拉起並退避；CLI 需要 Docker 的指令自動帶起
- [ ] 真實 WSL 實測（`/mnt/wsl` socket、保活、client context、首次自動建立、Supervise）
- [x] `wsld migrate`：把 client 內舊的 image／具名 volume 複製到 engine（不刪來源、不搬容器；停用本機 dockerd 由使用者確認後自行執行）；待實機驗證
- [ ] 實測：把既有已裝 docker 的 machine 直接設為 engine（bootstrap 沿用既有安裝並補共用 socket）
- [ ] 角色改回獨立／client 改回（GUI 目前沒有「取消角色」）
- [ ] 獨立 data VHD（`wsl --mount --vhd`），engine 重建時 image 不丟
- [ ] CLI 的 engine 保活（目前只有 wsld-gui）

階段 2（已改為「專用路徑曝露」，取代 proxy）：client 以非遞迴 `mount --bind /` 把根目錄曝露在共用的 `/mnt/wsl/distro/<名稱>`（只在執行 docker 時才曝露），client 內 docker shim 在 wsld context 下對 `run`／`create`／`compose up|run|create|start|restart` 先曝露並 `cd` 到對應的曝露路徑，engine 以相同絕對路徑解析 bind mount；不需要 proxy、同步或共用 VHD。實測依據：`/mnt/c`（virtiofs）小檔案慢 50–270 倍且 inotify 不跨掛載、`\\wsl.localhost` 掛載再慢約 7 倍，故不採用。

- [x] `wsld-expose`（root helper）＋ sudoers（只限 expose／release／status）＋ docker shim（`WSLD_NO_SHIM=1` 可關）
- [x] `internal/expose`：Report／Release／GC／StopUsers／EnsureFor；Supervise 節流 GC（30 秒）、停止／刪除前釋放、GUI 啟動舊容器前補曝露
- [x] `wsld expose status|release|gc`、doctor（`expose_stale`、`expose_tools_<distro>`）、Machines 卡片「已曝露」標籤與釋放按鈕
- [ ] 真實 WSL 長期驗證（moto-wander compose、壓縮／調整大小、`wsl --terminate` 後殘留清除）
- [ ] 絕對路徑 `-v /home/...` 的改寫（目前不處理，只在文件說明）

**退出條件**：各項自訂；**不**阻擋 Phase 5／6 結案。

---

## Phase 6 — Rust core + WinUI3（預計 v2.0）

對應規格：[SPEC.md](SPEC.md) §1.4／§7；完整架構設計：[GUI-WINUI.md](GUI-WINUI.md)。

**已鎖定決策**：核心（取代 Go `internal/*`）與 CLI 改寫為 Rust；GUI 改寫為純 Rust（`windows-rs` 直寫 WinUI3/XAML，非 C#/.NET 殼）；核心與 GUI／CLI 間沿用現有本機 HTTP+WebSocket 橋接 API 模式；漸進式並行遷移（現有 Go/Wails 產品持續發布，新架構在旁邊長出來，功能與測試對等後才切換）。

### Phase 6a — Rust core + CLI（並行、opt-in binary）

**目標**：`wsld-core`＋`wsld-cli` 對齊現有 Go `internal/{lifecycle,dockerapi,wsl,config,doctor,i18n,procutil,host}`＋`internal/cli` 的行為，作為額外的 opt-in 二進位發布，不影響現行 Go 產品。

#### 交付

- Cargo workspace（`crates/wsld-core`、`crates/wsld-cli`）
- `wsld-core`：lifecycle／dockerapi／wsl／config／doctor／i18n／procutil／host，逐一對應現有 Go package（見 GUI-WINUI.md §1.2）
- `wsld-cli`：`clap` derive，子命令與退出碼（0–4）對齊現有 `wsld` CLI

#### 完成定義

- [ ] `cli`／`config`／`wsl`／`lifecycle`／`doctor`／`i18n` 現有 Go 測試逐一有對應且綠燈的 Rust 測試（checklist 逐套件核對，非只看數量）
- [ ] Rust CLI 能讀寫現有 Go CLI 產生的 `%APPDATA%\wsld\config.toml`，兩者可在同一機器並行、不互相破壞
- [ ] 實機（Ubuntu-26.04）跑通 `init`／`start`／`status`／`ps`／`logs`／`doctor`／`use`／`unmanage`／`unregister`，與現有 Go CLI 側寫同一份 config 不衝突

**退出條件**：上列完成定義全過；可進入 6b。

---

### Phase 6b — Rust 橋接 server + testhost

**目標**：`internal/gui/server.go`＋`resources.go`＋`fake.go`／`fakewsl.go` 對應的橋接 API，改寫成 `wsld-bridge`（共用型別）＋`wsld-bridge-server`（axum）＋`wsld-testhost`。

#### 交付

- `wsld-bridge`：REST／WS API 的 serde 型別（server 與未來 GUI client 共用）
- `wsld-bridge-server`：axum router，對 `Backend` trait 泛型，路由與 `/__test/*` 對齊現有 `internal/gui`（見 GUI-WINUI.md §2）
- `wsld-testhost`：Linux/CI 可跑，服務現有（未修改）`frontend/dist` + fake engine

#### 完成定義

- [ ] `internal/gui` 現有 30 個 server 測試對應的情境（路由形狀、錯誤碼、WS framing、fake engine 狀態轉換）在 `wsld-bridge-server` 有對應且綠燈的整合測試
- [ ] 現有**未修改**的 TS 前端＋現有 10 個 Playwright spec 直接對新 Rust testhost 跑過並全過（證明橋接層行為對等，零 UI 風險）

**退出條件**：上列完成定義全過；可進入 6c。

---

### Phase 6c — WinUI3 proof-of-concept（time-boxed spike，go/no-go 關卡）

**目標**：在投入 6d／6e 工程量之前，驗證純 Rust（`windows-rs`）寫 WinUI3 的可行性。

#### 交付

- 最小 WinUI3 視窗（`windows-rs` + Windows App SDK bootstrap、一個 XAML 頁面、一個按鈕）
- Tray icon（`Shell_NotifyIcon`）與 XAML `DispatcherQueue` 共存於同一行程的 spike
- 背景 tokio runtime（跑 axum 橋接 server）與 UI thread 的 threading 方案 spike（見 GUI-WINUI.md §6.2 預設方案）
- 封裝模式決定：MSIX vs unpackaged
  - 併入 **MSIX 防火牆 spike**：以 manifest 的 `desktop2:Extension Category="windows.firewallRules"` 在安裝時建立規則，執行時免 UAC（取代 0.13.0 的 netsh 提權）。需驗證：
    - 一般安裝（雙擊 .msix，非 App Attach）是否確實建立規則
    - 規則只能指定方向／協定／設定檔／埠範圍，**不支援 RemoteAddresses=LocalSubnet**，範圍比現行規則寬
    - AppData 寫入虛擬化是否把 `%APPDATA%\wsld\config.toml` 與 CLI `wsld.exe` 分開
    - 登入自啟改 `windows.startupTask`、CLI 以 `appExecutionAlias` 暴露
    - 簽章管道（Microsoft Store 或付費簽章服務；自簽憑證需管理員匯入，等於又回到 UAC）與 CI 打包（`makeappx`／`signtool`）

#### 完成定義

- [ ] 證明（或否證）XAML dispatcher、tray 訊息泵、tokio runtime 三者可在同一行程穩定共存，不死鎖
- [ ] 手寫 `.xaml` 檔透過 WinRT XAML loader 載入可行（若不可行，記錄改用程式碼建構 UI 的替代方案）
- [ ] 封裝模式（MSIX／unpackaged）定案

**退出條件（go/no-go）**：可行 → 進入 6d。**不可行**（在合理工作量內無法解決 threading/工具鏈問題）→ 中止純 Rust GUI 路線，改為 C#/.NET 殼＋Rust core FFI／sidecar，回頭修訂本文件與 GUI-WINUI.md 後再重新排 6d 起的階段。

---

### Phase 6d — WinUI3 殼 + 核心視圖

**目標**：原生 XAML 版面骨架（header／side nav／main／info drawer／footer）＋ Containers／Images／Volumes／Networks 四個視圖。

#### 交付

- WinUI3 殼：header／side nav／main／info drawer／footer，對應 [GUI.md](GUI.md) 版面
- Containers／Images／Volumes／Networks：列表＋列操作＋info drawer，透過 `wsld-bridge` 型別呼叫內嵌 server

#### 完成定義

- [ ] 上述四視圖功能對齊 [GUI.md](GUI.md) 功能對照表對應項目
- [ ] 手動 smoke test checklist（比照 GUI.md「實機（Windows）手測項目」）通過
- [ ] 6b 的橋接 contract test 仍全過（API 未變，只是新增了一個 client）

**退出條件**：上列完成定義全過；可進入 6e。

---

### Phase 6e — Machines／Settings／Terminal／Tray／Autostart

**目標**：剩餘 Windows 整合較重的部分。

#### 交付

- Machines 視圖（設為主要／folder／terminal／Info／unmanage／unregister／Add distro）
- Settings 視圖（refresh／exec_shell／tray_autostart／Theme）
- Logs／Exec 面板的終端 widget（子 spike：`vte`／`alacritty_terminal` parser 驅動自訂 XAML 控制項，無現成範例）
- Tray（`Shell_NotifyIcon`）、single-instance mutex、autostart 登錄檔

#### 完成定義

- [ ] 全部視圖功能對齊 [GUI.md](GUI.md) 完整功能表
- [ ] 終端 widget 可用（Logs tail／live、Exec 互動）
- [ ] i18n（EN／zh-TW）對齊 `wsld-core::i18n`
- [ ] 手動 smoke test checklist 通過

**退出條件**：上列完成定義全過；可進入 6f。

---

### Phase 6f — Parity 驗證 + cutover

**目標**：確認 Rust/WinUI3 build 與現行 Go/Wails 產品功能與測試對等，決定並執行切換。

#### 交付

- 逐套件 Go 測試 → Rust 測試對應 checklist（100% 覆蓋確認）
- 逐 Playwright spec 行為斷言 → 橋接 contract test 或手動 smoke test 對應 checklist
- SPEC §6 驗收清單在 Rust/WinUI3 build 上重跑一次（實機）

#### 完成定義

- [ ] 上述三項 checklist 全過
- [ ] 版號跳至 **v2.0.0**；沿用既有發行管道（`.github/workflows/release.yml`）與檔名（`dist/wsld.exe`／`dist/wsld-gui.exe`）
- [ ] Go/Wails 版進入至少一個發行週期的 deprecation 通知（README／Roadmap 標註），觀察無迴歸後才移除 `cmd/`／`internal/`／`frontend/` 舊碼

**退出條件**：上列完成定義全過；v2.0.0 發布，Phase 6 結案。

---

## 優先序

1. 碼在 WSL、引擎同 distro（rootful）
2. 本機 TCP（23750+，每 managed machine 一埠）
3. 0.5 = Phase 5；TDD + Playwright
4. 之後 v2.0 = Rust core + WinUI3（Phase 6a–6f）
5. Extra 有需求再排（不擋主線；含 Setup／npiperelay 等）
6. 只綁 127.0.0.1
7. GUI／CLI 共用 core

## 版本編號原則

格式 `v{a}.{b}.{c}`（git tag 帶 `v`，`Makefile`／`package.json`／`winres.json` 不帶）：

| 位 | 何時進位 | 例 |
| -- | -------- | -- |
| **a** | 大型改版或架構變化 | Rust core＋WinUI3 cutover → `v2.0.0` |
| **b** | 功能變更（新增、移除或改變功能行為） | 新增區網存取、新增發行版支援 |
| **c** | 修正錯誤或微調畫面（不改功能） | bug fix、文案、間距／顏色、樣式調整 |

- 進位時右邊歸零（`0.8.3` → 加功能 → `0.9.0`；`0.9.0` → 修 bug → `0.9.1`）。
- 一次發行同時含功能與修正時，取最高的那一位進位。
- 此原則自 0.8.3 之後的發行起適用；`v0.8.1`～`v0.8.3` 依舊制以 c 位發行功能，tag 不回改。

## 分支開發原則

- 新功能或任何改動一律從最新的 `main` 開分支進行，不直接在 `main` 上提交。
- 分支命名：`feat/<主題>`（功能）、`fix/<主題>`（修錯）、`docs/<主題>`、`chore/<主題>`。
- 分支上可自由多次提交；完成（測試通過、文件同步）後以 **squash merge** 併回 `main`，`main` 上每個功能／修正只留一個提交。
- squash 提交訊息沿用既有格式（`<範圍>: <摘要>`，如 `gui: start/stop WSL distros from Machines cards`）；發行提交為 `release x.y.z: ...`。
- 併回後刪除該分支。

## 里程碑


| Phase | 版本角色 |
| ----- | -------- |
| 0     | 前置 |
| 1–4   | 基底就緒（曾稱 v1.0 驗收；現改 0.5 定義開放） |
| 5     | **0.5 主線** |
| Extra | Setup 一鍵、npiperelay、自動更新、arm64、隔離旗標 |
| 6a–6b | Rust core／CLI／橋接 server 並行開發（不影響現行發布） |
| 6c    | go/no-go 關卡：純 Rust WinUI3（`windows-rs`）可行性 |
| 6d–6e | WinUI3 殼與功能對等 |
| 6f    | Parity 驗證＋cutover → 之後 **v2.0.0** |


