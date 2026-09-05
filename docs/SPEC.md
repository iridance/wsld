# wsl-docker 產品規格（SPEC）

版本：0.4  
狀態：**v1.0 已實作並通過 §6 驗收**（2026-09-06，Ubuntu-26.04 實機）  
最後更新：2026-09-06

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
| `wsld`（CLI） | 建立／啟動 WSL、ensure dockerd、腳本化管理 |
| `wsld-gui`（GUI） | Tray + 主視窗；與 CLI **共用** Go `internal` core |

### 1.3 第一版（v1.0）必須有

- **Phase 1–4 全部完成**（含打磨；見 Roadmap）
- Windows **GUI**（Wails v2）：Tray + 主視窗（列表、Start／Stop、Logs、**Exec 終端**、一鍵 ensure）——**非 WinUI**
- Windows **CLI** `wsld` + 獨立 **`wsld-gui.exe`**
- WSL bootstrap：Docker CE（rootful）+ Compose plugin + API `127.0.0.1:23750`（可自動換埠）
- TDD；UI 變更以 **Playwright** 驗證
- 授權：**MIT**；正式散佈目標：**windows/amd64**

### 1.4 第二版（v2.0）目標

- **WinUI 3** UI（Roadmap Phase 6）
- 功能至少對齊 v1 GUI；與同一引擎／CLI 行為一致

### 1.5 非目標（v1.0）

- WinUI、arm64 正式支援、開 WSL 通用終端按鈕（僅容器 Exec）
- Kubernetes、Compose 視覺 IDE
- 以 `C:\` 為一等公民專案路徑
- SSH／暴露 `0.0.0.0` 上的 Docker API
- 依賴 Docker Desktop

## 2. 鎖定決策一覽（grill-me）

| 主題 | 決定 |
|------|------|
| 產品／repo | `wsl-docker` |
| CLI | `wsld` |
| GUI 二進位 | `wsld-gui.exe`（與 CLI 分開散佈；兩者都要） |
| Config | `%APPDATA%\wsl-docker\config.toml` |
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
| v1.0 範圍 | Phase 1+2+3+**4** |

## 3. 使用者工作流

### 3.1 日常

1. Cursor／VS Code **WSL Remote** 進 Ubuntu；repo 在 `$HOME/...`
2. WSL 內用 `docker`／`docker compose`（unix socket）
3. Windows 用 **wsld-gui** 看列表／Logs／Exec；或以 `wsld` 腳本化

### 3.2 首次安裝

1. Admin：`01-enable-wsl.ps1`（若需要）
2. `wsld init` → bootstrap + 寫 config
3. 開 `wsld-gui` → Start → 列表／操作
4. `wsld doctor` 輔助診斷

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

## 5. 元件規格

### 5.1 倉庫（目標）

```text
wsl-docker/
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

### 5.2 Config（`%APPDATA%\wsl-docker\config.toml`）

```toml
distro = "Ubuntu-26.04"
user = "irice"
api_endpoint = "tcp://127.0.0.1:23750"
# exec_shell = ""          # 空則 bash→sh
# refresh_interval_sec = 3
# tray_autostart = false
# api_timeout_sec = 10
```

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
| `init` | distro 策略、bootstrap、寫 config（含選到的 API port） |
| `start` | 喚醒 distro、ensure docker、探測 API |
| `status` | WSL／docker／API 狀態 |
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
| 版面 | 上：header（distro、連線、endpoint、刷新間隔、啟動引擎、設定）；左：side nav（大型 icon + 文字）；下：footer（版本、語言） |
| Containers | 固定顯示全部容器（含已停止）；刷新（前景 3s；失焦／Tray 暫停）；Start／Stop／Logs／Terminal 純圖示操作；compose 分組；欄寬可拖曳 |
| Images | 列表（repo:tag、ID、大小、建立、使用中）、移除、清除未使用（= `image prune -a`）；使用中不強制刪 |
| Volumes | 列表、移除、清除未使用（= `volume prune -a`）；使用中不強制刪 |
| Networks | 唯讀列表 |
| Machines | 本機 WSL 發行版（狀態、版本、wsld／default 角色）、查看 wsl.conf 與 systemd、以 `wsl --install --no-launch` 新增 |
| Logs | 預設 100 行；Live 預設關 |
| Terminal | 選中容器 Exec；bash→sh；config 可覆寫 |
| 破壞性操作 | 一律確認對話框；Docker 409 原樣呈現，不自動 force |
| 語系 | 預設英；右下角可切繁中並寫入 config |

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

## 6. 驗收標準（v1.0）

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

## 7. 仍開放（非 v1 阻擋）

- v2 WinUI 與 Go core 整合方式、WinAppDriver／UIA（Phase 6 前）
- arm64、專用隔離 distro 旗標、npiperelay（Phase 5+）
