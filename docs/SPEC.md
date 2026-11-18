# wsld 產品規格（SPEC）

版本：0.8.1（git tag `v0.8.1`）  
狀態：**0.8.1 已交付**（Phase 0–4 基底 2026-09-06；Phase 5 多 distro＋GUI 補完見 Roadmap）
最後更新：2026-11-19

## 1. 問題與目標

### 1.1 問題

在 Windows 上想要接近 macOS OrbStack 的日常體驗：

- 在 **Ubuntu Linux 環境**裡放原始碼、跑 git 與 Linux script
- 使用 **Docker** 跑容器，並能從 Windows 側查看／管理容器
- 避免早期 Docker Desktop 過重的體感，也不想靠 SSH 遠端硬接

### 1.2 目標

| 角色 | 責任 |
|------|------|
| WSL Ubuntu | 「machine」：原始碼、git、shell；同機執行 **rootful dockerd** |
| `wsld`（CLI） | 建立／啟動 WSL、ensure dockerd、腳本化管理；可切換 active machine |
| `wsld-gui`（GUI） | Tray + 主視窗；與 CLI **共用** Go `internal` core |

### 1.3 基底（Phase 1–4，已落地）

- Windows **GUI**（Wails v2）：Tray + 主視窗——**非 WinUI**
- Windows **CLI** `wsld` + 獨立 **`wsld-gui.exe`**
- WSL bootstrap：Docker CE（rootful）+ Compose plugin + API `127.0.0.1:23750+`（可自動換埠）
- TDD；UI 變更以 **Playwright** 驗證
- 授權：**MIT**；正式散佈目標：**windows/amd64**

### 1.3a 產品 0.5（Phase 5，已交付）

見 [ROADMAP.md](ROADMAP.md) Phase 5：多 distro（單一 active）、Machines／Containers 補完、Settings 側欄＋theme、Info summary、UI 打磨。

### 1.4 之後（v2.0）目標

- 核心（取代 Go `internal/*`）與 GUI 皆改寫為**純 Rust**；**WinUI 3** UI 透過 `windows-rs` 的 WinRT 投影直接寫（非 C#/.NET 殼）（Roadmap Phase 6a–6f）
- 核心與 GUI／CLI 之間沿用現有的本機 HTTP+WebSocket 橋接 API 模式（不改為 in-process FFI）
- 遷移採漸進式並行：現有 Go/Wails 產品持續發布，新架構在同一 repo 旁邊長出來，功能與測試對等後才切換
- 功能至少對齊當時 GUI；與同一引擎／CLI 行為一致
- 完整設計見 [GUI-WINUI.md](GUI-WINUI.md)

### 1.5 非目標（0.5）

- WinUI（Phase 6）；Setup 一鍵、arm64／npiperelay／自動更新／專用隔離 distro 旗標（Roadmap Extra）
- Kubernetes、Compose 視覺 IDE
- 以 `C:\` 為一等公民專案路徑
- SSH／暴露 `0.0.0.0` 上的 Docker API
- 依賴 Docker Desktop

## 2. 鎖定決策一覽（grill-me）

| 主題 | 決定 |
|------|------|
| 產品／repo | `wsld` |
| CLI | `wsld` |
| GUI 二進位 | `wsld-gui.exe`（與 CLI 分開散佈；兩者都要） |
| Config | `%APPDATA%\wsld\config.toml` |
| Distro | 優先沿用既有 Ubuntu；否則安裝官方 Ubuntu |
| Docker | Rootful CE + 預設安裝 Compose plugin |
| API | 預設 `tcp://127.0.0.1:23750`；占用則自動遞增並寫入 config；只綁 loopback |
| GUI 棧 | Wails v2 |
| UI 測試 | Playwright |
| Logs | 預設尾部 100 行；不 follow；GUI「Live」預設關 |
| Terminal | 選中容器 `docker exec`；預設 bash→sh；config 可覆寫 |
| 列表刷新 | 前景每 3s；縮 Tray／失焦暫停；間隔可設定 |
| 登入起 Tray | 可選，**預設關閉** |
| doctor | 標準組（見 §4.7） |
| 退出碼 | 0／1／2／3／4（見 §4.5） |
| 語系 | 跟隨 Windows；中文→繁中，其餘英；缺譯用英 |
| 架構 | windows/amd64 only |
| 授權 | MIT |
| 基底範圍 | Phase 1+2+3+4（已落地） |
| 0.5 範圍 | Phase 5（多 distro＋GUI 補完；見 Roadmap） |

## 3. 使用者工作流

### 3.1 日常

1. Cursor／VS Code **WSL Remote** 進 Ubuntu；repo 在 `$HOME/...`
2. WSL 內用 `docker`／`docker compose`（unix socket）
3. Windows 用 **wsld-gui** 看列表／Logs／Exec；或以 `wsld` 腳本化

### 3.2 首次安裝

現行（手動）：

1. Admin：`01-enable-wsl.ps1`（若需要；可能重開機）
2. `wsld init` → bootstrap + 寫 config
3. 開 `wsld-gui` → Start → 列表／操作
4. `wsld doctor` 輔助診斷

**Extra — Setup 應用**（見 Roadmap Extra）：一鍵啟用 WSL（UAC）→ 需重開則寫待續 → **重開後自動跳出**接續 `init`（可選 start／開主視窗）。腳本路徑保留給進階使用者。

### 3.3 重開機後

`wsld start` 或 GUI 一鍵 Start。登入自動起 Tray **預設關**（可在設定開啟）。

## 4. 架構決策

### 4.1 引擎在內

dockerd 在**開發用 Ubuntu**（rootful），利於 `$HOME` bind mount。

### 4.2 本機 TCP

- hosts：`unix:///var/run/docker.sock` + `tcp://127.0.0.1:<port>`
- 預設 port **23750**；衝突則 23751… 並更新 config
- ensure／systemctl 可用 `wsl -d <distro> -- …`
- 禁止綁 `0.0.0.0`

### 4.3 Client

- Go `internal/*`：lifecycle、dockerapi、config、doctor
- CLI：`cmd/wsld` → `wsld.exe`
- GUI：Wails → `wsld-gui.exe`
- 僅正式支援 **windows/amd64**

### 4.4 Distro

- 偵測已安裝 Ubuntu 系列並沿用；否則 `wsl --install` 官方 Ubuntu
- `wsl.conf`：`systemd=true`
- `loginctl enable-linger`：啟用（doctor 會查）
- **0.5**：可管理多個 distro profile，同一時間一個 active（見 Roadmap Phase 5）

## 5. 元件規格

### 5.1 倉庫（目標）

```text
wsld/
  LICENSE                 # MIT
  README.md
  docs/ SPEC ROADMAP ARCHITECTURE BOOTSTRAP GUI TESTING
  config.example.toml
  scripts/01-enable-wsl.ps1  02-bootstrap-docker.sh
  cmd/wsld/               # CLI
  cmd/wsld-gui/           # Wails GUI（或同等目錄）
  internal/
  frontend/               # Wails UI（若適用）
```

### 5.2 Config（`%APPDATA%\wsld\config.toml`）

基底（扁平，仍可讀；0.5 載入時自動遷移）：

```toml
distro = "Ubuntu-26.04"
user = "irice"
api_endpoint = "tcp://127.0.0.1:23750"
# exec_shell = ""
# refresh_interval_sec = 3
# tray_autostart = false
# api_timeout_sec = 10
# lang = "en"
```

0.5 形狀：全局 `lang`／`tray_autostart`／`refresh_interval_sec`／`exec_shell`／`theme`；`active`＋多筆 `[[machines]]`（各含 `distro`／`user`／`api_endpoint`）。舊扁平格式載入時自動遷移。範例見 `config.example.toml`。

### 5.3 Bootstrap

`02-bootstrap-docker.sh`（冪等）：

1. 安裝 Docker CE + **Compose plugin**
2. 使用者加入 `docker` 群組（rootful）
3. daemon hosts：unix socket + `tcp://127.0.0.1:<chosen-port>`
4. `systemctl enable --now docker`
5. enable linger
6. `docker info` 成功

`01-enable-wsl.ps1`（Admin）：啟用 WSL2 功能、update、必要時裝 Ubuntu、重開機提示。

### 5.4 CLI（`wsld`）

| 命令 | 行為 |
|------|------|
| `init` | distro 策略、bootstrap、寫／更新該 machine profile（含 API port）並可設 active |
| `start` | 喚醒 active distro、ensure docker、探測 API |
| `use` | 切換 active machine（與 GUI「設為目前」同寫 config） |
| `unmanage` | 停該 distro docker 後刪 profile（保留 WSL） |
| `unregister` | `wsl --unregister`（需 `--confirm <name>`）＋刪 profile |
| `status` | WSL／docker／API 狀態（對準 active） |
| `ps` | 容器列表 |
| `logs <id>` | 預設尾部 100 行；`--follow` 可選 |
| `start-ct`／`stop-ct` | 啟停容器 |
| `doctor` | 見 §5.6 |
| `exec`（可選對齊 GUI） | 進入容器 shell（策略同 GUI） |

**退出碼**：

| 碼 | 意義 |
|----|------|
| 0 | 成功 |
| 1 | 一般錯誤 |
| 2 | 用法／參數錯誤 |
| 3 | WSL／引擎未就緒 |
| 4 | API／連線失敗 |

### 5.5 GUI（Wails + Playwright）

| 區域 | 功能 |
|------|------|
| Tray | 狀態、Ensure／Start、開主視窗、退出；登入自啟預設關 |
| 版面 | 上：header（active distro＋有 border 的 folder／terminal、連線徽章、啟動引擎；**無** Settings、**無** tcp 路徑）；左：side nav（Containers…Machines＋**Settings**）；右：Info drawer；下：footer（版本、語言） |
| Containers | 固定顯示全部；刷新可設定；Start／Stop／Logs／Terminal／Info／**Remove**；右側 Info＝summary（Name／ID／Image／State／Created／Started／Command／Ports／Networks／Mounts／Labels／Restart）；compose 分組；欄寬可拖曳 |
| Images | 列表、移除、清除未使用；使用中不強制刪 |
| Volumes | 列表、移除、清除未使用；使用中不強制刪 |
| Networks | 列表；個別 Remove（內建不可刪）；清除未使用（prune） |
| Machines | 列表＋設為目前／folder／terminal（底部 panel、user home）／取消管理／unregister／右側 Info（`.wslconfig` memory／processors、wsl.conf、tcp endpoint、CPU／Mem 摘要）；新增機器（下載進度：進度條、已下載／總量、速度、經過時間） |
| Settings | 側欄視圖：refresh、exec_shell、tray_autostart、Theme（Light／Dark／System） |
| Logs | 預設 100 行；Live 預設關 |
| Terminal | 容器 Exec；Machines 亦可開 distro shell（底部 panel） |
| 破壞性操作 | 一律確認；unregister 需輸入名稱；Remove 容器可含 running（會停止） |
| 語系 | 預設英；右下角可切繁中並寫入 config |
| 多 distro | 多 profile、單一 active；`wsld use`／Machines 切換作用於全部功能 |
詳細互動與 test id 寫入 `docs/GUI.md`（Phase 3）。

### 5.6 doctor（標準組）

必須檢查並報告：

- WSL 存在且目標 distro 為 v2、可喚醒
- systemd、`docker.service` active
- API `_ping` 成功
- 監聽只在 127.0.0.1（警告 `0.0.0.0`）
- linger
- Docker Desktop 是否在跑（建議勿雙引擎混用）
- API 埠占用／config 中的 endpoint 說明
- 提醒：repo 應放在 WSL `$HOME`，非 `C:\`

### 5.7 測試

- Core：Go `testing`（TDD）
- UI：Playwright（每次 UI 改動）
- 指令見 `docs/TESTING.md`

## 6. 驗收標準（基底／曾稱 v1.0）

Phase 0–4 已於 2026-09-06 通過下列項目（Ubuntu-26.04 實機）。**0.5 另以 Roadmap Phase 5 完成定義為準**。

1. 無 Docker Desktop 亦可：`init` → ensure → CLI／GUI 列表。  
2. GUI：冷開 → Start → 列表；Start／Stop；Logs；**Exec 終端可用**。  
3. CLI 與 GUI 所見同一引擎。  
4. WSL 內 `$HOME` 專案 `docker run -v "$PWD:/app"` 正常。  
5. 重開機後 `wsld start` 或 GUI Start 可恢復。  
6. doctor 能抓誤綁 `0.0.0.0`、Desktop 衝突等。  
7. 埠衝突時自動換埠並寫入 config。  
8. Playwright＋Go 測試有文件化一鍵指令且關鍵路徑覆蓋。  
9. Phase 4 打磨項完成（錯誤文案、重開機文件、已知限制、Desktop 建議）。  
10. MIT `LICENSE`；散佈 amd64。

## 7. 仍開放／延後

- ~~v2 WinUI 與 Go core 整合方式~~：已定案——v2 為 Rust core（取代 Go internal）＋ WinUI3（純 Rust／`windows-rs`）；整合方式沿用 HTTP+WS 橋接。見 [GUI-WINUI.md](GUI-WINUI.md)。
- ~~WinAppDriver／UIA（Phase 6 前）~~：已定案——不採 WinAppDriver/UIA；改採橋接 API contract test＋手動/腳本化 smoke test。見 [GUI-WINUI.md](GUI-WINUI.md) §3。如日後手測不敷使用可重新評估。
- 新開放項（見 [GUI-WINUI.md](GUI-WINUI.md) §6）：WinUI3 封裝模式（MSIX vs unpackaged）、Phase 6c spike 的 go/no-go 結果（純 Rust `windows-rs` 可行性，否則退回 C#/.NET 殼＋Rust core FFI）
- Extra（見 Roadmap）：**Setup 應用**（一鍵啟用 WSL → 重開接續 init）、arm64、專用隔離 distro 旗標、npiperelay、自動更新
