# 日常操作與已知限制（OPERATIONS）

對應 [SPEC.md](SPEC.md) §6.9：重開機、Docker Desktop 並存、故障排除、已知限制。

## 重開機／WSL 關機後

WSL2 的 VM 在 Windows 重開機、`wsl --shutdown`、或閒置一段時間後會停止，dockerd 隨之停止。恢復方式任選其一：

| 方式 | 操作 |
|---|---|
| CLI | `wsld start`（喚醒 distro → 確認 systemd → `systemctl start docker` → 等 `_ping`；實測約 3 秒） |
| GUI | 開 `wsld-gui.exe`，按「啟動引擎」；或 Tray 選單「啟動引擎」 |
| 登入自啟 | 設定 →「開機自啟動」toggle（HKCU Run 值 `wsld`，預設關）。Tray 起來後仍需按一次「啟動引擎」，預設不會自動拉起 dockerd |

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

- 預設 `23750`；`wsld init` 選埠時會避開：
  1. 其他 machine 已寫入 `config.toml` 的埠（`--busy-ports`）
  2. WSL／Linux 內正在 LISTEN 的埠（`ss`／`netstat`）
  3. **Windows 主機**上被非 WSL 行程佔用的埠（`netstat.exe`＋行程名；`wslrelay`／`wsl` 視為本產品轉發，不算衝突——用來避開 Cursor 等佔埠導致 GUI 連錯引擎）
- 自動往上找後寫回 `config.toml` 的 `api_endpoint`。
- API **只綁 127.0.0.1**（這條規則只針對 Docker API；容器 published 埠要讓內網連入，請見「區網存取」）。`doctor` 的「API 只綁 loopback」會抓到 `0.0.0.0` 誤綁；「Docker API _ping」會抓到「埠有人聽但不是 Docker」。
- 手動改埠：改 `/etc/docker/daemon.json` 的 `hosts` 與 `config.toml` 的 `api_endpoint` 兩處，然後 `systemctl restart docker`。
- 若某 machine 已佔到衝突埠：對該 distro 再跑一次 `wsld init -d <name>`（或 GUI 重新設為目前）會改選空閒埠。

## 區網存取（讓其他設備連到容器）

預設 WSL 是 NAT 模式：`docker run -p 8080:80` 只會出現在 Windows 的 `127.0.0.1:8080`，手機或其他電腦連 `http://<這台電腦的內網IP>:8080` 會失敗。開啟「區網存取」後就能從內網連入。

- **開啟**：`wsld lan on`，或 GUI 的 Settings › Network › LAN access。
  1. 跳出 UAC 一次。wsld 會新增兩條 Hyper-V 防火牆放行規則：
     - 規則名稱：`wsld-lan-tcp`／`wsld-lan-udp`
     - 只套用在 WSL（VMCreatorId `{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}`）
     - **只對「私人」網路設定檔**生效
  2. 在 `%USERPROFILE%\.wslconfig` 的 `[wsl2]` 寫入 `networkingMode=mirrored`：
     - 其他設定與 CRLF 原樣保留
     - 第一次改寫前備份成 `.wslconfig.wsld.bak`
  3. 確認後執行 `wsl --shutdown`。這會重啟**所有** WSL 發行版，接著重新啟動 active machine。CLI 可用 `--yes` 略過詢問。
- **狀態**：`wsld lan status`（可加 `--json`）。會顯示：
  - `.wslconfig` 的設定與實際生效模式（`wslinfo --networking-mode`）
  - 防火牆規則是否存在、目前網路設定檔
  - 內網 IPv4，以及可連線的網址 `http://<IP>:<published 埠>`
- **關閉**：`wsld lan off`。會移除規則（UAC）、移除 `networkingMode`，再重啟 WSL，回到 NAT。
- **開放範圍**：mirrored 模式下，WSL 內**所有**監聽 `0.0.0.0` 的服務都會對「私人」網路開放，不只容器。Docker API 只綁 `127.0.0.1`，mirrored 不會把 loopback 鏡像到區網，所以 **API 不會對外**。
- **不需要另外設定 Windows Defender 的應用程式規則**：埠是開在 WSL 內，由 Hyper-V 防火牆把關。
- **連不到時**：
  - `wsld doctor` 只在已啟用時才會出現「區網存取」這一項，用來檢查：
    - 是否已生效（尚未生效時執行 `wsl --shutdown` 後 `wsld start`）
    - 防火牆規則是否存在
    - 目前網路是否為「公用」（需到 Windows 設定把網路改成「私人」）
  - 路由器的 AP／用戶端隔離（例如訪客 Wi-Fi）也會讓設備互相連不到。這不在 wsld 控制範圍內。
- **手動回復**：
  - 用 `.wslconfig.wsld.bak` 覆蓋 `.wslconfig`
  - 以系統管理員執行 `Remove-NetFirewallHyperVRule -Name wsld-lan-tcp,wsld-lan-udp`
  - 最後執行 `wsl --shutdown`
- **需求**：Windows 11 22H2（build 22621）以上。

## 容器「停止」要等 10 秒？

`docker stop` 先送 SIGTERM，等 **10 秒寬限期**（`t`）沒退出才 SIGKILL。容器內 PID 1 若不處理 SIGTERM（典型：`sh -c 'while true; do …; done'`、某些沒用 `exec` 啟動主程式的 entrypoint），就會撐滿 10 秒。nginx／redis／postgres 等正常處理訊號的映像約 0.1–0.5 秒。

- GUI 按「停止」後該列會立刻顯示「停止中…」並停用按鈕，完成後才變 `exited`；CLI `wsld stop-ct` 會等到 dockerd 回應（最多 `t` + `api_timeout_sec`）。
- 想快一點：映像用 `exec` 啟動主程式或加 `--init`（tini）；或建立容器時設 `--stop-timeout 2`。

## 故障排除

先跑 `wsld doctor`；每個 FAIL／WARN 都附「↳ 建議」。常見情境：

| 症狀 | 原因 | 處理 |
|---|---|---|
| `wsl.exe 不可用` | WSL 功能未啟用 | 以系統管理員執行 `scripts\01-enable-wsl.ps1`，重開機（之後 Extra Setup 會一鍵＋重開接續） |
| `systemd 未啟用` | `/etc/wsl.conf` 缺 `[boot] systemd=true` | bootstrap 會寫入；之後需 `wsl --shutdown` 再進 distro |
| `bootstrap prerequisites not met`（exit 3） | systemd 剛啟用尚未生效 | `wsl --shutdown` → 重開 distro → `wsld init` |
| `不是支援的發行版系列`（exit 5） | distro 不是 Ubuntu/Debian、Arch、openSUSE 系 | 改用支援的發行版 |
| `API 無回應` 但 `docker.service 運作中` | `daemon.json` hosts 沒有 `tcp://127.0.0.1:<port>`，或埠與 config 不一致 | 對照 `daemon.json` 與 `config.toml`；`wsld init` 可重寫 |
| `有服務監聽但不是 Docker` | 埠被其他程式占用 | 重跑 `wsld init` 自動換埠 |
| GUI 顯示「請先執行 wsld init」 | `%APPDATA%\wsld\config.toml` 不存在 | `wsld init` |
| Desktop WARN | Docker Desktop 同時運行 | 見上節 |
| `docker` 指令在 WSL 內連不到 | 未設 `DOCKER_HOST`，也不在 `docker` 群組 | bootstrap 已把使用者加入 `docker` 群組，重開終端；或 `export DOCKER_HOST=tcp://127.0.0.1:23750` |

日誌：`wsl -d <distro> -u root -- journalctl -u docker -n 50`。

## 清理磁碟：GUI 的 Images／Volumes

- **Images → 清除未使用**等同 `docker image prune -a`：刪掉所有沒有容器（含已停止容器）引用的映像，含有標籤者。
- **Volumes → 清除未使用**等同 `docker volume prune -a`：連具名 volume 一起刪，只要沒有容器掛載；資料不可復原，確認框會提醒。
- 單筆移除使用中的映像／volume／network 會收到 Docker 的 409（顯示在紅色 banner）。GUI 不會自動 `--force`：先停掉並移除相關容器再刪。
- **Networks → 清除未使用**等同 `docker network prune`：刪掉未被容器使用的自訂網路；`bridge`／`host`／`none` 不會動。個別 Remove 對內建網路為 disabled。

## Machines：管理多個 WSL 發行版

- GUI 的 Machines 視圖列出 `wsl -l -v` 的所有發行版；標籤標示 **active**（目前管理）與 **default**（WSL 預設）。
- **0.5**：可「設為目前」切換 active（作用於全部功能）；右側 Info 顯示 `.wslconfig`（memory／processors）、`/etc/wsl.conf`、API endpoint、CPU／Mem 用量摘要；可 Open folder／Terminal（user home）、取消管理、unregister（打名稱確認）。
- 「新增機器」執行 `wsl --install -d <name> --no-launch`（會跳過發行版第一次啟動時建立帳號與密碼的步驟）。因此對話框會一併詢問 **Linux 帳號**（預設依 Windows 使用者名稱）與**密碼**。安裝完成後 wsld 會 `useradd` 建立帳號、加入 sudo／wheel、以 `chpasswd` 設定密碼，並把 `/etc/wsl.conf` 的 `[user] default` 設為該帳號。之後「設為目前」會 bootstrap（安裝 Docker）、寫入該 machine 的 user＋endpoint 並啟動引擎。支援 Ubuntu/Debian、Arch、openSUSE 系。
  - 密碼只經由 stdin 交給發行版內的 `chpasswd`，不會出現在指令參數中，也不會寫入設定檔或 log。
  - 密碼留空則帳號沒有密碼，`sudo` 無法使用；事後可補設：`wsl -d <machine> -u root passwd <帳號>`。
  - CLI `wsld init`（以及 `start`／`use` 觸發的初始化）遇到只有 root 的發行版、需要新建帳號時，會以隱藏輸入詢問密碼（輸入兩次確認，留空略過）；非互動環境可用 `wsld init --distro <name> --password-stdin`，從 stdin 第一行讀取。既有帳號的密碼一律不更動。
- 「啟動引擎」或「設為目前」失敗時，banner 保留失敗原因直到引擎就緒，不會被輪詢的「無法連線 Docker API」蓋掉。

## 已知限制

- **平台**：Windows 11 x64 + WSL2；未測 Windows 10、arm64（arm64 見 Roadmap Extra）。
- **多 distro（0.5）**：多 profile、單一 active；每 managed machine 自有 API 埠；舊扁平 config 載入時自動遷移。
- **rootful dockerd**：容器以 root 執行 dockerd（bootstrap 有把使用者加入 `docker` 群組）。rootless 不在範圍。
- **無 npiperelay（Extra）**：Windows 側 `docker.exe` 需以 `DOCKER_HOST=tcp://127.0.0.1:<port>` 連線，不提供 `//./pipe/docker_engine`。
- **GUI**：Theme（radio：Light／Dark／System）；Settings 在側欄；header 無 tcp 路徑。「開機自啟動」只起 Tray，不自動啟動引擎。
- **自動更新（Extra）**：無；以發布頁下載新版覆蓋。
- **安全**：API 只綁 loopback、無 TLS；同機任何程序可存取 Docker API（等同本機 root）。不要把 `hosts` 改成 `0.0.0.0`。要讓內網連到**容器**，請用 `wsld lan on`，不要對外開放 API。
- **區網存取（mirrored）是全域設定**：會影響本機所有 WSL 發行版，切換時必須 `wsl --shutdown`。
