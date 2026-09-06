# 日常操作與已知限制（OPERATIONS）

對應 [SPEC.md](SPEC.md) §6.9：重開機、Docker Desktop 並存、故障排除、已知限制。

## 重開機／WSL 關機後

WSL2 的 VM 在 Windows 重開機、`wsl --shutdown`、或閒置一段時間後會停止，dockerd 隨之停止。恢復方式任選其一：

| 方式 | 操作 |
|---|---|
| CLI | `wsld start`（喚醒 distro → 確認 systemd → `systemctl start docker` → 等 `_ping`；實測約 3 秒） |
| GUI | 開 `wsld-gui.exe`，按「啟動引擎」；或 Tray 選單「啟動引擎」 |
| 登入自啟 | 設定 → 勾「登入時啟動 Tray」（HKCU Run 值 `wsld`，預設關）。Tray 起來後仍需按一次「啟動引擎」，預設不會自動拉起 dockerd |

`wsld status` 退出碼：0 就緒、3 引擎未就緒、4 API 無回應（見 [TESTING.md](TESTING.md)）。

`loginctl enable-linger` 已由 bootstrap 開啟，避免最後一個 WSL 終端關閉時 systemd user session 被回收；但 **WSL VM 本身**閒置關機不在此控制範圍，這是 WSL 的行為。

## 與 Docker Desktop 並存

可以同時安裝，但**不建議同時運行兩個引擎**：

- Desktop 的 WSL integration 會往 distro 注入自己的 `docker` CLI 與 socket，`docker ps` 可能看到的是 Desktop 的引擎而非 wsld 管理的 dockerd。
- `wsld doctor` 會偵測 `Docker Desktop.exe`／`com.docker.backend.exe` 並回 WARN。

建議做法：

1. 只用 wsld：解除安裝 Desktop，或至少在 Desktop 設定關掉「WSL integration」對該 distro 的整合。
2. 需要 Desktop 時：關掉 wsld 的 dockerd（`wsl -d <distro> -u root -- systemctl stop docker`），再啟動 Desktop；反之亦然。
3. 用 `DOCKER_HOST` 明確指定要用哪個引擎（wsld：`tcp://127.0.0.1:23750`，實際埠見 `config.toml`）。

## 埠

- 預設 `23750`；`wsld init` 若發現被占用會自動往上找（bats 覆蓋），並寫回 `config.toml` 的 `api_endpoint`。
- API **只綁 127.0.0.1**。`doctor` 的「API 只綁 loopback」會抓到 `0.0.0.0` 誤綁；「Docker API _ping」會抓到「埠有人聽但不是 Docker」。
- 手動改埠：改 `/etc/docker/daemon.json` 的 `hosts` 與 `config.toml` 的 `api_endpoint` 兩處，然後 `systemctl restart docker`。

## 容器「停止」要等 10 秒？

`docker stop` 先送 SIGTERM，等 **10 秒寬限期**（`t`）沒退出才 SIGKILL。容器內 PID 1 若不處理 SIGTERM（典型：`sh -c 'while true; do …; done'`、某些沒用 `exec` 啟動主程式的 entrypoint），就會撐滿 10 秒。nginx／redis／postgres 等正常處理訊號的映像約 0.1–0.5 秒。

- GUI 按「停止」後該列會立刻顯示「停止中…」並停用按鈕，完成後才變 `exited`；CLI `wsld stop-ct` 會等到 dockerd 回應（最多 `t` + `api_timeout_sec`）。
- 想快一點：映像用 `exec` 啟動主程式或加 `--init`（tini）；或建立容器時設 `--stop-timeout 2`。

## 故障排除

先跑 `wsld doctor`；每個 FAIL／WARN 都附「↳ 建議」。常見情境：

| 症狀 | 原因 | 處理 |
|---|---|---|
| `wsl.exe 不可用` | WSL 功能未啟用 | 以系統管理員執行 `scripts\01-enable-wsl.ps1`，重開機 |
| `systemd 未啟用` | `/etc/wsl.conf` 缺 `[boot] systemd=true` | bootstrap 會寫入；之後需 `wsl --shutdown` 再進 distro |
| `bootstrap prerequisites not met`（exit 3） | systemd 剛啟用尚未生效 | `wsl --shutdown` → 重開 distro → `wsld init` |
| `API 無回應` 但 `docker.service 運作中` | `daemon.json` hosts 沒有 `tcp://127.0.0.1:<port>`，或埠與 config 不一致 | 對照 `daemon.json` 與 `config.toml`；`wsld init` 可重寫 |
| `有服務監聽但不是 Docker` | 埠被其他程式占用 | 重跑 `wsld init` 自動換埠 |
| GUI 顯示「請先執行 wsld init」 | `%APPDATA%\wsl-docker\config.toml` 不存在 | `wsld init` |
| Desktop WARN | Docker Desktop 同時運行 | 見上節 |
| `docker` 指令在 WSL 內連不到 | 未設 `DOCKER_HOST`，也不在 `docker` 群組 | bootstrap 已把使用者加入 `docker` 群組，重開終端；或 `export DOCKER_HOST=tcp://127.0.0.1:23750` |

日誌：`wsl -d <distro> -u root -- journalctl -u docker -n 50`。

## 清理磁碟：GUI 的 Images／Volumes

- **Images → 清除未使用**等同 `docker image prune -a`：刪掉所有沒有容器（含已停止容器）引用的映像，含有標籤者。
- **Volumes → 清除未使用**等同 `docker volume prune -a`：連具名 volume 一起刪，只要沒有容器掛載；資料不可復原，確認框會提醒。
- 單筆移除使用中的映像／volume 會收到 Docker 的 409（顯示在紅色 banner）。GUI 不會自動 `--force`：先停掉並移除相關容器再刪。
- Networks 在 GUI 為唯讀；`docker network prune` 請在 WSL 內執行。

## Machines：管理多個 WSL 發行版

- GUI 的 Machines 視圖列出 `wsl -l -v` 的所有發行版；標籤標示 **active**（目前管理）與 **default**（WSL 預設）。
- **0.5**：可「設為目前」切換 active（作用於全部功能）；右側 Info 顯示 `.wslconfig`（memory／processors）、`/etc/wsl.conf`、API endpoint、CPU／Mem 用量摘要；可 Open folder／Terminal（user home）、取消管理、unregister（打名稱確認）。
- 「新增發行版」執行 `wsl --install -d <name> --no-launch`。安裝完成後：
  1. `wsl -d <name>` 進入一次，依提示建立預設使用者；
  2. 設為目前後執行 `wsld init`（或等效）以安裝 Docker 並寫入該 machine 的 endpoint。

## 已知限制（現行／0.5 目標）

- **平台**：Windows 11 x64 + WSL2；未測 Windows 10、arm64（arm64 延後）。
- **多 distro（0.5 目標）**：多 profile、單一 active；每 managed machine 自有 API 埠。基底階段仍為單一 distro config，實作前請見 Roadmap Phase 5。
- **rootful dockerd**：容器以 root 執行 dockerd（bootstrap 有把使用者加入 `docker` 群組）。rootless 不在範圍。
- **無 npiperelay（延後）**：Windows 側 `docker.exe` 需以 `DOCKER_HOST=tcp://127.0.0.1:<port>` 連線，不提供 `//./pipe/docker_engine`。
- **GUI**：
  - 0.5 目標：Theme Light／Dark／System；Settings 在側欄；header 無 tcp 路徑。
  - 「登入時啟動 Tray」只起 Tray，不自動啟動引擎。
- **自動更新（延後）**：無；以發布頁下載新版覆蓋。
- **安全**：API 只綁 loopback、無 TLS；同機任何程序可存取 Docker API（等同本機 root）。不要把 `hosts` 改成 `0.0.0.0`。
