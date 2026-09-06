# wsld Roadmap

版本：0.5.3（git tag `v0.5.3`）  
對應規格：[SPEC.md](SPEC.md)（**0.5.3 已交付**；Phase 0–4 基底已落地）

## 總覽

```text
Phase 0  規格／倉庫骨架 + 早期決策鎖定     ✅
Phase 1  Bootstrap 腳本可手動跑通         ✅
Phase 2  共用核心 + wsld CLI              ✅
Phase 3  GUI 基底（Wails + Playwright）   ✅
Phase 4  打磨（文件／Tray 自啟／LICENSE） ✅  → 基底就緒
Phase 5  0.5 主線：多 distro＋GUI 補完    ✅  → 語意化版號 **0.5.3**
Extra    延後能力（含首次 Setup）           ← 有需求再排
Phase 6  WinUI 3 GUI                       ← 之後 v2.0

0.5 = Phase 5 完成定義全過（見下方）；發行版號 **0.5.3**（`v0.5.3`）
之後 v2.0 = Phase 6
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
- GUI：Machines「設為目前」；CLI：`wsld use <distro>`（同寫 config）
- 「設為目前」即寫**占位 profile**；未 init 可 active → 橫幅提示；**Start engine**（或 `wsld init`）自動 bootstrap／補齊 endpoint／user／port 後再拉起引擎
- 切換 active：不停舊引擎；硬切 Logs／Terminal／Info／快取／pending
- 取消管理：一律先停該 distro docker → 刪 profile（不刪 WSL）
- Unregister 發行版：GUI＋CLI；輸入 distro 名稱雙重確認

#### Machines／Containers UI

- Machines actions：設為目前、Open folder、Terminal（**底部 panel**、該 distro WSL shell、cwd＝user home）、取消管理、unregister、Info（右側 drawer）、Add distro
- Machines Info（右側，對齊 Containers）：基本欄、`.wslconfig` 的 memory／processors、`/etc/wsl.conf`、該 machine 的 **API endpoint（tcp）**、CPU／記憶體用量摘要（非 process 表）；取代 ⓘ dialog
- Containers：Remove（running 可刪，確認會停止）；Info summary：Name、ID、Image、State、Status、Created、Started、Command、Ports、Networks（+IP）、Mounts、Labels（可摺疊）、Restart policy

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
- [x] GUI「設為目前」與 `wsld use` 同步；切換硬切面板／快取；未 init active 顯示未就緒
- [x] 取消管理（停 docker＋刪 profile）；unregister（打名稱確認）
- [x] Machines：folder／terminal（home）／右側 Info（含 `.wslconfig` memory／processors、wsl.conf、tcp、用量摘要）
- [x] Containers：Remove（含 running）；Info summary 欄位齊全（必要時 inspect）
- [x] Settings 側欄＋theme；header 無 Settings／無 tcp；header icon 有 border
- [x] 列高／格線與 side nav 間距打磨
- [x] 相關 Go 測試＋Playwright 通過；實機抽樣驗收（切換 distro、unregister 確認、Remove running）

**退出條件**：上列完成定義全勾；產品敘事標 **0.5.3** 已交付（git tag `v0.5.3`；`make build-win` 注入 GUI／CLI 版號）。

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

### 其他候選

- [ ] npiperelay（Windows named pipe ↔ 引擎）
- [ ] 自動更新（`wsld`／`wsld-gui` 本體）
- [ ] arm64 正式支援
- [ ] 專用隔離 distro 旗標

**退出條件**：各項自訂；**不**阻擋 Phase 5／6 結案。

---

## Phase 6 — WinUI（預計 v2.0）

- 見 SPEC §1.4；另訂 `docs/GUI-WINUI.md`

---

## 優先序

1. 碼在 WSL、引擎同 distro（rootful）
2. 本機 TCP（23750+，每 managed machine 一埠）
3. 0.5 = Phase 5；TDD + Playwright
4. 之後 v2.0 = WinUI
5. Extra 有需求再排（不擋主線；含 Setup／npiperelay 等）
6. 只綁 127.0.0.1
7. GUI／CLI 共用 core

## 里程碑


| Phase | 版本角色 |
| ----- | -------- |
| 0     | 前置 |
| 1–4   | 基底就緒（曾稱 v1.0 驗收；現改 0.5 定義開放） |
| 5     | **0.5 主線** |
| Extra | Setup 一鍵、npiperelay、自動更新、arm64、隔離旗標 |
| 6     | 之後 **v2.0** |


