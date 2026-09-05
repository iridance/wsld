# Bootstrap（Phase 1）

把一台 Windows 11 從零帶到「WSL2 Ubuntu 內有 rootful Docker CE，且 API 只綁 `127.0.0.1:23750`」。
兩支腳本都冪等，可重複執行。

## 步驟

### 1. Windows 端：啟用 WSL2 + Ubuntu

以 **系統管理員** 開 PowerShell：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\01-enable-wsl.ps1
```

- 啟用 `Microsoft-Windows-Subsystem-Linux`、`VirtualMachinePlatform`（`-NoRestart`）
- `wsl --update`、`wsl --set-default-version 2`
- 沒有任何 Ubuntu（WSL2）發行版時執行 `wsl --install -d Ubuntu --no-launch`
- 需要重開機時輸出提示並以 **exit 10** 結束；重開後不需再跑
- 參數：`-Distro <名稱>`（預設 `Ubuntu`）、`-DryRun`

### 2. WSL 端：安裝並設定 Docker

在 Ubuntu 終端（repo 位於 WSL 檔案系統內）：

```bash
bash scripts/02-bootstrap-docker.sh            # 預設埠 23750
bash scripts/02-bootstrap-docker.sh --port 24000 --no-compose
bash scripts/02-bootstrap-docker.sh --dry-run
```

流程：

| 步驟 | 動作 | 冪等行為 |
|---|---|---|
| 0 | 檢查 WSL2 核心、Ubuntu/Debian、使用者存在 | 不符則 exit 3 |
| 1 | `/etc/wsl.conf` 寫入 `[boot] systemd=true` | 已存在則略過；新寫入時要求 `wsl --shutdown` 後重跑（exit 3） |
| 2 | 官方 apt 來源安裝 `docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin` | 已安裝則略過 |
| 3 | `usermod -aG docker <user>` | 已在群組則略過 |
| 4 | 選埠：從 `--port` 起遞增找空埠；寫 `/etc/docker/daemon.json`（`hosts` = unix socket + `tcp://127.0.0.1:<port>`）與 systemd drop-in 清掉 `-H fd://` | 既有 daemon.json 已是同埠或 dockerd 正在監聽則沿用；含其他設定時先備份 `.wsld.bak` |
| 5 | `systemctl enable --now docker`、`loginctl enable-linger <user>` | — |
| 6 | 驗證 `docker info`、`curl http://127.0.0.1:<port>/_ping` == `OK`、確認未綁非 loopback | 失敗 exit 4 |

結尾輸出機器可讀行，供 Windows 端 `wsld init` 解析：

```
WSLD_API_PORT=23750
WSLD_API_ENDPOINT=tcp://127.0.0.1:23750
```

### 3. Windows 端驗證

```powershell
curl.exe http://127.0.0.1:23750/_ping     # 應回 OK
```

WSL2 會把 WSL 內 `127.0.0.1` 監聽的埠轉發到 Windows 的 `localhost`；因此 dockerd **不需要**、也**不應**綁 `0.0.0.0`。

## 退出碼

| 碼 | 意義 |
|---|---|
| 0 | 成功 |
| 2 | 參數／用法錯誤 |
| 3 | 前置條件不符（非 WSL2、非 Ubuntu、需重開機／`wsl --shutdown`、缺 sudo） |
| 4 | Docker 安裝後驗證失敗（`docker info` / `_ping`） |
| 10 | （僅 01）Windows 功能已啟用，需要重新開機 |

## 安全邊界

- API 僅綁 `127.0.0.1`，無 TLS；`wsld_validate_loopback_hosts` 在寫入前拒絕任何非 loopback host。
- rootful Docker：`docker` 群組成員等同 root。這是 v1 的既定選擇（見 `docs/SPEC.md`）。

## 測試

```bash
make test-scripts          # bats（bootstrap-lib）+ shellcheck + Pester（WslHelpers，透過 powershell.exe）
```

- `scripts/tests/bootstrap-lib.bats`：埠選擇、daemon.json 渲染／loopback 驗證、systemd drop-in、wsl.conf 冪等、參數解析、WSL／distro 偵測。
- `scripts/tests/WslHelpers.Tests.ps1`：`wsl -l -v` 解析（含 UTF-16 NUL）、Ubuntu 選擇、Win11 判定、重開機判定。
- 主腳本的副作用（apt、systemctl）不在單元測試內，用 `--dry-run` / `-DryRun` 人工檢查步驟列表。

## 已知限制

- `.ps1` 需 UTF-8 **含 BOM**（Windows PowerShell 5.1 才會正確讀取中文）；`.gitattributes` 已設定。
- 首次把使用者加入 `docker` 群組後，需重開 WSL shell 才能不加 `sudo` 使用 `docker`。
- 只支援 Ubuntu／Debian 系 apt 發行版。
