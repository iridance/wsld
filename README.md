# wsld — Windows Subsystem Linux with Docker

`wsld`（全名 **Windows Subsystem Linux with Docker**；倉庫名 `wsl-docker`）是 Windows 上的輕量 Docker 控制面：把 **WSL2 Ubuntu** 當成開發 machine（原始碼在 Linux FS），同機跑 **rootful dockerd**，再用 **`wsld` CLI + `wsld-gui`（Wails）** 做就緒與容器管理。類似 macOS 上 OrbStack 的體驗，但不需要 Docker Desktop。

- **v1.0**（本版）= Roadmap Phase 1–4：bootstrap、CLI、GUI、打磨；決策見 [docs/SPEC.md](docs/SPEC.md) §2
- **v2.0** = WinUI 3（Phase 6）

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

主視窗：標題列顯示連線中的 WSL distro 與狀態、容器列表（compose 專案分組；Start／Stop／Logs／Exec 終端；欄寬可拖曳）、啟動引擎、設定（刷新間隔、終端 shell、登入時啟動 Tray——預設關）。介面預設英文，右下角可切換繁體中文（寫入 `config.toml`，CLI 與 Tray 一併切換）。詳見 [docs/GUI.md](docs/GUI.md)。

### 4. 重開機之後

`wsld start` 或 GUI「啟動引擎」。細節與 Docker Desktop 並存、故障排除、已知限制：[docs/OPERATIONS.md](docs/OPERATIONS.md)。

## 文件

| 文件 | 內容 |
|------|------|
| [docs/SPEC.md](docs/SPEC.md) | v1.0 鎖定規格與驗收標準 |
| [docs/ROADMAP.md](docs/ROADMAP.md) | 分期；v1.0 = Phase 1–4 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架構 |
| [docs/BOOTSTRAP.md](docs/BOOTSTRAP.md) | 01／02 腳本細節、退出碼、安全邊界 |
| [docs/GUI.md](docs/GUI.md) | GUI 架構、功能對照、實機手測 |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | 重開機、Desktop 並存、故障排除、已知限制 |
| [docs/TESTING.md](docs/TESTING.md) | 一鍵測試（bats／Pester／Go／Playwright） |
| [config.example.toml](config.example.toml) | 設定範例 |
| `.cursor/rules/tdd-and-ui-testing.mdc` | TDD；UI → Playwright |

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
