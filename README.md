# wsld — WSL with Docker

在 Windows 上跑 Docker，但不靠 Docker Desktop。

把 WSL2 當開發機（程式放 Linux 檔系統），裡面跑 dockerd；外面用 `wsld` CLI 或 `wsld-gui` 管引擎與容器。感覺接近 mac 上的 OrbStack。

目前 **0.6.0**（tag `v0.6.0`）已交付（Networks prune／remove、Info State、Playwright 本機依賴）：見 [ROADMAP](docs/ROADMAP.md)、[SPEC](docs/SPEC.md)。Phase 0–4 基底可用；之後 v2.0 會換成 WinUI。

```text
Windows                         WSL2
┌─────────────────────┐         ┌──────────────────┐
│  wsld-gui / wsld    │  TCP    │  你的專案        │
│                     │────────►│  dockerd         │
└─────────────────────┘         │  :23750+         │
                                └──────────────────┘
```

## 快速開始

### 前置

- Windows 11 x64；第一次裝 WSL 需要管理員權限
- 還沒裝 WSL 的話，開**管理員** PowerShell：

```powershell
.\scripts\01-enable-wsl.ps1   # 開 WSL2 + Ubuntu；可能要重開機
```

- 執行檔：Release 下載，或自己 `make build-win`（產出在 `dist/`）

### 初始化（做一次）

```powershell
wsld init
```

會選／裝 Ubuntu、在裡面裝 Docker、開 systemd、把 API 綁在 `127.0.0.1:23750`（被占用就換埠），並寫入 `%APPDATA%\wsld\config.toml`。

### CLI

```powershell
wsld start                 # 喚醒 distro、起 dockerd
wsld use <distro>          # 設為目前；首次會自動 init + start
wsld unmanage <distro>     # 停 docker 並刪 profile（保留 WSL）
wsld unregister <d> --confirm <d>
wsld status                # 0 就緒／3 未就緒／4 API 掛了
wsld ps [-a] [--json]
wsld logs <ct> [--tail 100] [--follow]
wsld stop-ct <ct> / wsld start-ct <ct>
wsld exec <ct> [cmd…]
wsld doctor                # 健康檢查 + 建議
```

也可以直接用一般 `docker`：

```powershell
$env:DOCKER_HOST = "tcp://127.0.0.1:23750"   # Windows
```

```bash
export DOCKER_HOST=tcp://127.0.0.1:23750     # WSL（或重開終端後走 unix socket）
cd ~/Projects/myapp && docker run -v "$PWD:/app" …
```

### GUI

```powershell
wsld-gui.exe           # 開主視窗；按 X 縮到系統匣
wsld-gui.exe --tray    # 只留系統匣
```

側欄切 Containers／Images／Volumes／Networks／Machines／Settings；標題列可開資料夾、終端、啟動引擎。細節：[GUI](docs/GUI.md)。

### 重開機之後

再跑一次 `wsld start`，或在 GUI 按「啟動引擎」。並存 Desktop、踩坑：[OPERATIONS](docs/OPERATIONS.md)。

## 小提醒

1. 程式碼放 WSL `$HOME`，別把 `C:\` 當主戰場。
2. 用 WSL Remote 開發比較順。
3. 盡量別跟 Docker Desktop 雙引擎混著玩。
4. API 預設 **23750**（占用會換），只聽 `127.0.0.1`。

## 開發

```bash
make test        # shellcheck + bats + Pester + go test + Playwright
make build-win   # dist/wsld.exe + dist/wsld-gui.exe（VERSION←git exact tag，預設 0.6.0；GUI 左下角 v0.6.0）
```

GUI／CLI 共用 `internal/*`，功能先寫測試再實作。

## 文件

| 文件 | 內容 |
| --- | --- |
| [SPEC](docs/SPEC.md) | 規格（**0.6.0** 已交付） |
| [ROADMAP](docs/ROADMAP.md) | 分期 |
| [ARCHITECTURE](docs/ARCHITECTURE.md) | 架構 |
| [BOOTSTRAP](docs/BOOTSTRAP.md) | 安裝腳本細節 |
| [GUI](docs/GUI.md) | 介面說明 |
| [OPERATIONS](docs/OPERATIONS.md) | 日常使用／故障排除 |
| [TESTING](docs/TESTING.md) | 怎麼跑測試 |
| [config.example.toml](config.example.toml) | 設定範例 |

## 授權

[MIT](LICENSE)
