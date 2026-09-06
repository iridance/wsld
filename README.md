# wsld — WSL with Docker

`wsld`（全名 **Windows Subsystem for Linux with Docker**；倉庫名 `wsl-docker`）是 Windows 上的輕量 Docker 控制面：把 **WSL2 Ubuntu** 當成開發 machine（原始碼在 Linux FS），同機跑 **rootful dockerd**，再用 `wsld` **CLI +** `wsld-gui`**（Wails）** 做就緒與容器管理。類似 macOS 上 OrbStack 的體驗，但不需要 Docker Desktop。

- **0.5**（本階段，定義中）= Roadmap Phase 5：多 distro、Machines／Containers 補完、Settings 側欄＋theme、Info summary、UI 打磨；決策見 [docs/SPEC.md](docs/SPEC.md)、[docs/ROADMAP.md](docs/ROADMAP.md)
- Phase 0–4 基底已落地；**之後 v2.0** = WinUI 3（Phase 6）

## 架構一覽

```text
Windows                                 WSL2 Ubuntu
┌─────────────────────────────┐         ┌──────────────────────────┐
│  wsld-gui.exe (Wails)       │         │  $HOME repos             │
│  wsld.exe (CLI)             │  TCP    │  dockerd (rootful)       │
│  shared internal/           │────────►│  127.0.0.1:23750+        │
└─────────────────────────────┘         │  + docker.sock           │
                                        └──────────────────────────┘
```



## 快速開始



### 0. 前置

- Windows 11 x64、可用系統管理員權限一次
- 沒裝過 WSL：以**系統管理員** PowerShell 執行
  ```powershell
  .\scripts\01-enable-wsl.ps1      # 啟用 WSL2 功能與 Ubuntu；可能需要重開機
  ```
- 取得執行檔：從 Release 下載 `wsld.exe`、`wsld-gui.exe`，或自行 `make build-win`（產出在 `dist/`）



### 1. 初始化（一次）

```powershell
wsld init
```

會挑選（或安裝）Ubuntu WSL2 distro，在裡面以 root 執行 bootstrap：安裝 Docker CE + Compose、開 systemd、把 dockerd 綁到 `tcp://127.0.0.1:23750`（占用時自動換埠）、`loginctl enable-linger`、把你的使用者加入 `docker` 群組，最後寫入 `%APPDATA%\wsl-docker\config.toml`。

### 2. CLI

```powershell
wsld start                 # 喚醒 distro、啟動 dockerd、等 API 就緒
wsld status                # 退出碼 0 就緒／3 未就緒／4 API 無回應
wsld ps [-a] [--json]
wsld logs <ct> [--tail 100] [--follow]
wsld stop-ct <ct> / wsld start-ct <ct>
wsld exec <ct> [cmd…]      # 互動 shell（bash→sh 自動退回）
wsld doctor                # 11 項健康檢查，每項附建議
```

一般 `docker` CLI 也能直接用：

```powershell
$env:DOCKER_HOST = "tcp://127.0.0.1:23750"   # Windows 側
```

```bash
export DOCKER_HOST=tcp://127.0.0.1:23750     # WSL 內（或重開終端讓 docker 群組生效，直接用 unix socket）
cd ~/Projects/myapp && docker run -v "$PWD:/app" …
```



### 3. GUI

```powershell
wsld-gui.exe           # 開主視窗；按 X 縮到系統匣
wsld-gui.exe --tray    # 只起系統匣
```

主視窗：標題列顯示 active distro（有 border 的開啟資料夾／終端）、連線狀態、啟動引擎；**tcp 路徑與設定不在 header**。左側側欄：**Containers**（compose 分組；Start／Stop／Logs／Exec／Info／Remove；右側 Info summary）、**Images**／**Volumes**／**Networks**、**Machines**（設為目前、folder／terminal、右側 Info 含 wsl 設定與用量、取消管理／unregister、新增發行版）、**Settings**（刷新間隔、shell、Tray 自啟、Theme）。左下版本、右下語言。詳見 [docs/GUI.md](docs/GUI.md)。0.5 多 distro 與上述補完見 [docs/ROADMAP.md](docs/ROADMAP.md) Phase 5。

### 4. 重開機之後

`wsld start` 或 GUI「啟動引擎」。細節與 Docker Desktop 並存、故障排除、已知限制：[docs/OPERATIONS.md](docs/OPERATIONS.md)。

## 文件


| 文件                                           | 內容                              |
| -------------------------------------------- | ------------------------------- |
| [docs/SPEC.md](docs/SPEC.md)                 | 規格；0.5 定義開放中；§6 為基底驗收        |
| [docs/ROADMAP.md](docs/ROADMAP.md)           | 分期；**0.5 = Phase 5**               |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架構                              |
| [docs/BOOTSTRAP.md](docs/BOOTSTRAP.md)       | 01／02 腳本細節、退出碼、安全邊界             |
| [docs/GUI.md](docs/GUI.md)                   | GUI 架構、功能對照、實機手測                |
| [docs/OPERATIONS.md](docs/OPERATIONS.md)     | 重開機、Desktop 並存、故障排除、已知限制        |
| [docs/TESTING.md](docs/TESTING.md)           | 一鍵測試（bats／Pester／Go／Playwright） |
| [config.example.toml](config.example.toml)   | 設定範例                            |
| `.cursor/rules/tdd-and-ui-testing.mdc`       | TDD；UI → Playwright             |




## 開發

```bash
make test        # shellcheck + bats + Pester + go test（含 windows 交叉 vet）+ Playwright
make build-win   # dist/wsld.exe + dist/wsld-gui.exe（GOOS=windows/amd64）
```

原則：GUI 與 CLI 共用 `internal/*`，不重複邏輯；所有功能先寫測試。

## 快速約定

1. 原始碼放 WSL `$HOME`，不要放 `C:\` 當主目錄。
2. 用 WSL Remote 開發。
3. 避免與 Docker Desktop 雙引擎混用。
4. 預設 API 埠 **23750**（占用會自動換）；API 只綁 `127.0.0.1`。
5. 重開機後 `wsld start` 或 GUI Start。



## 授權

[MIT](LICENSE)