# 日常操作與已知限制（OPERATIONS）

對應 [SPEC.md](SPEC.md) §6.9：重開機、Docker Desktop 並存、故障排除、已知限制。

## 重開機／WSL 關機後

WSL2 的 VM 在 Windows 重開機、`wsl --shutdown`、或閒置一段時間後會停止，dockerd 隨之停止。**不用手動啟動**：啟動 wsld 就是啟動引擎。

| 方式 | 行為 |
|---|---|
| GUI | 開 `wsld-gui.exe` 就自動帶起 engine（首次使用先建立 `wsld-engine`），之後每 5 秒檢查一次，引擎掉了（WSL 被關、當機）會自動拉起；失敗後退避 30 秒再試，原因顯示在橫幅。沒有「啟動引擎」按鈕 |
| CLI | 需要 Docker 的指令（`ps`、`logs`、`start-ct`、`stop-ct`、`exec`、`k8s`）會自動喚醒 distro、確認 systemd、啟動 docker 並等到 API；已就緒時不碰 WSL。沒有 `wsld start`／`wsld use` |
| 登入自啟 | 設定 →「開機自啟動」toggle（HKCU Run 值 `wsld`，預設關）。Tray 起來後會自動帶起引擎 |

在 Machines 頁**停止** engine 是使用者的明確決定：wsld 不會再自動拉起，直到你按「啟動」。

`wsld status` 退出碼：0 就緒、3 引擎未就緒、4 API 無回應（見 [TESTING.md](TESTING.md)）。`wsld status` 與 GUI 的狀態輪詢只在 distro 已執行時才查 systemd／docker，不會把停止中的 distro 叫醒。

`loginctl enable-linger` 已由 bootstrap 開啟，避免最後一個 WSL 終端關閉時 systemd user session 被回收；但 **WSL VM 本身**閒置關機不在此控制範圍，這是 WSL 的行為。

**主要機器不會被閒置回收（0.17.2）**：WSL 在最後一個 `wsl.exe` session 結束後約 20 秒就回收 distro（systemd 服務、dockerd 都不算 session）。wsld-gui（含縮到系統匣）替 **engine** 掛一個常駐的 `wsl -d <distro> -- sleep infinity` session，所以 engine 會一直執行；client 不保活，閒置後仍會被回收。限制：保活 session 屬於 wsld-gui 程序，Tray「結束」、程序被結束或 Windows 重開機後就沒有了（`wsld start` 是一次性命令，也無法保活）；使用者在 Machines 明確停止主要機器後，不會被自動重新喚醒，要再按「啟動」。

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
- 若某 machine 已佔到衝突埠：對該 distro 再跑一次 `wsld init -d <name>`會改選空閒埠。

## 區網存取（讓其他設備連到容器）

預設 WSL 是 NAT 模式：`docker run -p 8080:80` 只會出現在 Windows 的 `127.0.0.1:8080`，手機或其他電腦連 `http://<這台電腦的內網IP>:8080` 會失敗。開啟「區網存取」後就能從內網連入。

- **開啟**：GUI 的 Settings › 網路 › 區網存取（立即生效），或 `wsld lan on`（寫入 `config.toml` 的 `lan_access = true`）。
  - **wsld-gui** 會在這台電腦的每個內網 IPv4 上，替執行中容器的 published TCP 埠監聽，並把連線轉到 `127.0.0.1:<埠>`（WSL localhost 轉發）。每 3 秒依容器清單同步：新埠自動加入，停掉的容器自動關閉。
  - **防火牆**：wsld-gui 要通過 Windows 防火牆，其他設備才連得進來。wsld **只在你操作時**請求授權：開啟「區網存取」時若尚未授權，或按設定頁的「授權防火牆」。啟動 wsld-gui 時不會跳 UAC。
    - 會先出現 wsld 的**說明對話框**，列出要加的對內允許規則 `wsld-gui (LAN access)`：程式路徑、TCP、**私人＋公用網路**、遠端位址限 **LocalSubnet**（只接受同網段設備），並說明接下來 UAC 會顯示哪個程式。按「繼續」才跳 UAC；按「稍後」則保留開關、只顯示警告。
    - 一般情況由 Windows 內建的 **netsh.exe** 加入規則：UAC 顯示「網路命令殼層」、已驗證的發行者 Microsoft Windows；在 UAC 視窗按「顯示更多詳細資料」可看到完整的 `netsh advfirewall firewall add rule …` 指令。
    - 已有針對 wsld-gui 的封鎖規則（例如之前在 Windows 安全性警訊按過取消）或舊的 wsld 規則時，netsh 一行指令處理不了，改由 **wsld-gui 自己**（`wsld-gui.exe --allow-firewall`，經防火牆 COM API）停用封鎖規則並更新規則；此時 UAC 顯示 wsld-gui，但不會再開一個 app。
    - **尚未授權時不在內網 IP 監聽**（轉發暫停、容器列的地球按鈕改開 localhost），避免 Windows 自己跳安全性警訊；在那裡按取消會產生封鎖規則。查不到授權狀態時（例如防火牆由組織原則管理）照常監聽。
    - 規則綁定 exe 的完整路徑：搬移或改名 wsld-gui.exe 後需要重新授權。
    - 授權狀態以防火牆 COM API（`HNetCfg.FwPolicy2`）讀取，不需提權；背景同步沿用最近 30 秒內的結果，開啟設定頁時重新讀取。判定「已允許」：該 exe 的啟用中對內允許規則（TCP 或任何協定）合起來涵蓋私人與公用；有啟用中的封鎖規則則判定「已封鎖」。
  - WSL 維持 NAT：不改 `.wslconfig`、不重啟 WSL、不影響其他發行版。
- **只在 wsld-gui 執行時有效**（縮到系統匣也算）。只用 CLI 不會轉發。
- **開放範圍**：
  - 只轉發「綁所有介面」的 published 埠（`-p 8080:80`）。明確綁本機的（`-p 127.0.0.1:5432:5432`）不轉發。
  - **Docker API 埠不轉發**，仍只綁 `127.0.0.1`。
  - 只支援 TCP（UDP 不轉發）。
  - 容器看到的來源 IP 是本機，不是對方設備的 IP。
- **狀態**：
  - **容器頁**每個服務列的動作欄常駐地球按鈕：區網存取開啟時以瀏覽器開啟它的區網位址，關閉（或沒有轉發）時開 `http://localhost:<埠>`（多個埠時跳選單），容器 Info 也以文字列出區網位址。無法監聽時（例如該埠已被其他 Windows 程式佔用）按鈕標紅，滑過可看原因，點擊改開 localhost。設定頁只顯示開關與狀態。
  - `wsld lan status`（可加 `--json`）會顯示設定與內網 IPv4。
- **關閉**：GUI 開關，或 `wsld lan off`。轉發會立即（GUI）或在下一次同步時停止。
- **連不到時**：
  - 設定頁 › 網路若出現防火牆警告（轉發已暫停），按「授權防火牆」。從 `\\wsl.localhost\...` 執行開發版時，路徑不同於下載版，需另外授權（UNC 路徑一律由 wsld-gui 自己加規則）。
  - 路由器的 AP／用戶端隔離（例如訪客 Wi-Fi）也會讓設備互相連不到。這不在 wsld 控制範圍內。
- **0.9.0 遺留的 mirrored 模式**：
  - 0.9.0 以 WSL mirrored 網路＋Hyper-V 防火牆規則實作區網存取。mirrored 模式下容器埠直接佔用 Windows 的埠，轉發器無法監聽。
  - 偵測到 `.wslconfig` 有 `networkingMode=mirrored` 時，GUI 會顯示「還原 NAT」，`wsld doctor`／`wsld lan status` 也會提示。
  - `wsld lan restore-nat`（或 GUI 按鈕）會依序：
    1. 以 UAC 移除 `wsld-lan-tcp`／`wsld-lan-udp` 規則。
    2. 從 `.wslconfig` 移除 `networkingMode`（備份：`.wslconfig.wsld.bak`）。
    3. 確認後執行 `wsl --shutdown`。
    4. 等 Windows 釋放 API 埠（最多約 2 分鐘），引擎會由 wsld 自動重新帶起。
  - 手動回復：以系統管理員執行 `Remove-NetFirewallHyperVRule -Name wsld-lan-tcp,wsld-lan-udp`，刪掉 `.wslconfig` 的 `networkingMode` 行，再 `wsl --shutdown`。

## 本機 Kubernetes（k3s）

單節點 k3s 與該 distro 的 dockerd 共用映像檔：`docker build -t app:dev .` 之後，Deployment 以 `imagePullPolicy: Never`（或 `IfNotPresent` 且不用 `:latest`）直接使用，不必推到 registry。

**啟用**（預設關；需要約 0.6 GB 以上記憶體，WSL 只有 3 GB 時請先調高 `%UserProfile%\.wslconfig` 的 `memory=`）：

```powershell
wsld k8s on          # 首次會下載並安裝 k3s（約 70 MB），啟動（機器內可直接 `kubectl`；不會動 Windows 的 `~\.kube\config`，要在 Windows 用請另外 `wsld k8s kubeconfig`）
kubectl --context wsld-Ubuntu-26.04 get nodes
wsld k8s status      # 或 wsld k8s status --json
```

GUI：頂部 **Kubernetes** 頁的「啟動」鈕做同樣的事；頁面可看叢集資訊、Pods／Deployments／Services，並對 Pod 看 logs／開終端／刪除，對 Deployment 調整副本數或 rollout restart。

- **只有一台 machine 能開**：所有 WSL2 distro 共用同一個網路（API 埠、pod／service 網段、node 名稱＝Windows 主機名）。第二台 `wsld k8s on` 會被拒絕並指出目前開啟者；GUI 的「移到 …」會先停掉原本那台再啟動新的（兩邊是各自獨立的叢集，工作負載不會搬移）。
- **kubeconfig 以機器內為準，匯出到 Windows 是手動的**：`/etc/rancher/k3s/k3s.yaml` 是正本，啟動 k8s 不會寫 Windows 的檔案（與 docker 一樣，只轉出 loopback 埠）。要在 Windows 使用時按 GUI 的「匯出 kubeconfig」或執行 `wsld k8s kubeconfig`：以 context `wsld-<distro>` 合併進現有檔案，保留其他 context、註解與 current-context（原本沒有 current-context 才採用 wsld 的）。第一次修改前會備份為 `~\.kube\config.wsld-bak`；現有檔案無法解析時一律不動。
- **重開機／`wsl --shutdown` 之後**：自動帶起 engine 時會一併啟動 k3s（CLI 的 `wsld k8s`／GUI）。k3s 必須在 docker 之後啟動（wsld 已在 `k3s.service` 加 drop-in）；若手動改過 unit，開機後 node 一直 NotReady 通常就是這個原因，`systemctl restart k3s` 可恢復。
- **node IP**：實測 WSL 重啟後 eth0 位址未變；若日後變了，`wsld doctor` 與 `wsld k8s status` 會警告，執行 `wsld k8s off && wsld k8s on` 即可。Windows 側一律連 `127.0.0.1:<API 埠>`，不受影響。
- **停止**：`wsld k8s off` 會跑 `k3s-killall.sh`，連同 pod 容器一起停掉並釋放記憶體與 API 埠（`--docker` 時 pod 是 dockerd 的容器，單停 service 不會停它們）。Containers 頁把這些 `k8s_*` 容器摺成一個預設收合的群組；請交給 k3s 管理，不要手動刪。
- **重置**：`wsld k8s reset`（需確認或 `--yes`）清除所有 pod／volume／secret，回到乾淨叢集；若先前匯出過 kubeconfig，會一併更新 Windows 那份（憑證已更新），從未匯出過則不動。**移除**：`wsld k8s uninstall`（執行 `k3s-uninstall.sh`）。
- **與 Docker Desktop 的 Kubernetes 並存**：Docker Desktop 的 k8s 預設用 6443；wsld 新安裝的 k3s 改用 26443（`config.toml` 的 `k8s_api_port` 可改，裝時若被占用會自動往後找），因此通常不衝突。舊版安裝的 k3s 仍在 6443，需 `wsld k8s uninstall` 後重裝才會換埠。
- **埠**：Traefik 已停用，不會搶 80／443。NodePort 服務可在 Windows 以 `http://localhost:<nodePort>` 開啟（Services 分頁的地球鈕）；不轉發到區網。
- 疑難：`wsl -d <distro> -u root -- journalctl -u k3s -n 50`；`wsld doctor` 會檢查 k3s 就緒、記憶體、node IP 與埠。

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

- 先看用量：各頁標題旁的磁碟用量鈕顯示該類總計與比例長條，點開列出每一項的佔用與占比（容器頁以 compose 專案為單位）；容器與 Volume 列表也有 Size 欄；跨類別的總覽（含建置快取）用 `docker system df`。數字每 15 秒最多重算一次，刪除或 prune 後立即更新。
- **Machines** 卡片的「磁碟」是發行版虛擬磁碟（ext4.vhdx）在 Windows 上實際佔用的大小；刪掉 Docker 資料後這個檔案不會自動縮小，需另外壓縮 VHDX（例如 `wsl --manage <distro> --set-sparse true`）。
- **Images → 清除未使用**等同 `docker image prune -a`：刪掉所有沒有容器（含已停止容器）引用的映像，含有標籤者。
- **Volumes → 清除未使用**等同 `docker volume prune -a`：連具名 volume 一起刪，只要沒有容器掛載；資料不可復原，確認框會提醒。
- 單筆移除使用中的映像／volume／network 會收到 Docker 的 409（顯示在紅色 banner）。GUI 不會自動 `--force`：先停掉並移除相關容器再刪。
- **Networks → 清除未使用**等同 `docker network prune`：刪掉未被容器使用的自訂網路；`bridge`／`host`／`none` 不會動。個別 Remove 對內建網路為 disabled。

## Machines：管理多個 WSL 發行版

- GUI 的 Machines 視圖列出 `wsl -l -v` 的所有發行版；標籤標示 **active**（目前管理）與 **default**（WSL 預設）。
- **單一 engine**：沒有「設為主要」或切換機器；Docker 一律走 engine。Machines 頁把 Engine **單獨分一組放在最上面**，它不能停止、取消管理或刪除（GUI 沒有這些按鈕，API／CLI 回 409／錯誤）；只有它沒在跑時才出現「啟動」，其餘由 wsld 自動維持。右側 Info 顯示 `.wslconfig`（memory／processors）、`/etc/wsl.conf`、API endpoint、CPU／Mem 用量摘要；可 Open folder／Terminal（user home）、取消管理、unregister（打名稱確認）。
- 「新增機器」執行 `wsl --install -d <name> --no-launch`（會跳過發行版第一次啟動時建立帳號與密碼的步驟）。因此對話框會一併詢問 **Linux 帳號**（預設依 Windows 使用者名稱）與**密碼**。安裝完成後 wsld 會 `useradd` 建立帳號、加入 sudo／wheel、以 `chpasswd` 設定密碼，並把 `/etc/wsl.conf` 的 `[user] default` 設為該帳號。之後在卡片上按「共用 engine」會 bootstrap（只裝 docker CLI，連到共用 engine）。支援 Ubuntu/Debian、Arch、openSUSE 系。
  - 密碼只經由 stdin 交給發行版內的 `chpasswd`，不會出現在指令參數中，也不會寫入設定檔或 log。
  - 密碼留空則帳號沒有密碼，`sudo` 無法使用；事後可補設：`wsl -d <machine> -u root passwd <帳號>`。
  - CLI `wsld init`（以及 `start`／`use` 觸發的初始化）遇到只有 root 的發行版、需要新建帳號時，會以隱藏輸入詢問密碼（輸入兩次確認，留空略過）；非互動環境可用 `wsld init --distro <name> --password-stdin`，從 stdin 第一行讀取。既有帳號的密碼一律不更動。
- 自動帶起 engine 失敗時，banner 保留失敗原因（`/api/status` 的 `start_error`）直到引擎就緒，不會被輪詢的「無法連線 Docker API」蓋掉。

## 共用 engine（專用 dockerd distro）

預設每個 distro 各自有一個 dockerd，image／volume／container 各存一份。要共用就指定**一個 engine**，其他當 **client**：

- **GUI**：Machines 頁，沒有 engine 時其他機器顯示「設為 engine」；有 engine 後顯示「共用 engine」（設為 client）。卡片上有 Engine／Client 標籤。
- **CLI**：`wsld init --distro wsld-engine --role engine`，再 `wsld init --distro Debian1 --role client`。建議 engine 用新裝的乾淨 distro，例如 `wsl --install -d Ubuntu --name wsld-engine`（GUI「新增機器」亦可）；也可以把已裝好 docker 的機器直接設為 engine，bootstrap 會沿用既有安裝並補上共用 socket（此路徑尚未實測）。
- **k8s**：有 engine 後，`wsld k8s on` 一律裝在 engine（不帶 `--machine` 時）；其他機器不能開 k8s。
- **保活**：engine 由 wsld-gui 掛著 `wsl -d … -- sleep infinity`，避免 WSL 閒置回收。**只在 wsld-gui（含系統匣）執行時有效**；只用 CLI 時 engine 閒置後會被回收，下一個需要 Docker 的指令會自動再帶起。
- **doctor**：多檢查 `engine_socket`（`/mnt/wsl/wsld/docker.sock` 存在）與各 `client_<distro>`（context 為 `wsld`、沒有本機 dockerd 並存）；停止中的 client 不會被喚醒檢查。

限制：

- **bind mount**：`docker run -v /home/u/proj:/app`（或 compose 的 `./`）在 engine 解析，client 的 distro 內路徑 engine 看不到。可用 `/mnt/c/...` 與 named volume；`docker build .` 正常（context 經 API 傳送）。
- **安全**：共用 socket 為 0666，同一 VM 內任何 distro 的任何使用者都等同 root；loopback TCP 本來就是如此。
- **從舊版升級**：舊設定（各 distro 自帶 dockerd）載入時，原有已 init 的 distro 全部變成 client、資料原封不動，並新增專用的 `wsld-engine`，啟動 wsld 時自動 `wsl --install` 建立。舊機器上的 k3s 不再由 wsld 管理（旗標被清掉，叢集仍留在該 distro）；要用 k8s 請在 engine 上 `wsld k8s on`。接著用下面的 `wsld migrate` 把舊資料搬進 engine。
- **遷移**：`wsld migrate [--distro <client>] [--images=false] [--volumes=false] [--dry-run]` 把 client 內舊的本機 dockerd 資料複製到 engine：有 tag 的 image 以 `docker save | docker load`、具名 volume 以 alpine 的 `tar` 串流複製，全部在 client distro 內經 docker context（`default` → `wsld`）完成，不經 Windows。不刪來源、不搬容器；沒 tag 的 image、匿名 volume 會列為略過。本機 dockerd 沒在跑會先嘗試啟動。確認資料無誤後再 `wsl -d <client> -u root -- systemctl disable --now docker docker.socket`（指令結尾會提示）。先用 `--dry-run` 看清單。
- **磁碟**：所有 image 都在 engine 的 VHD，調整大小／釋放空間針對 engine（Machines 頁）。
- 尚未在真實 WSL 長期驗證（`/mnt/wsl` 共用 socket 行為、保活）；遇到問題請先 `wsld doctor`。

## 機器的磁碟上限

WSL 的虛擬磁碟（ext4.vhdx）是動態成長的：檔案大小跟著實際用量走，上限只是「最多能長到多大」。WSL 預設上限 1 TB；wsld 0.17 起新增機器預設 **60 GB**。

- **新增機器**：對話框「磁碟上限（GB）」，留空＝WSL 預設。CLI：`wsld init --vhd-size 60`（只在沒有 Ubuntu、需要安裝時有作用；`0`＝WSL 預設）。
- **既有機器**：GUI 的 Info →「磁碟上限」旁的筆形鈕；CLI：

```powershell
wsld resize-disk Ubuntu-26.04 60              # VHD 沒被占用時直接調整
wsld resize-disk --shutdown Ubuntu-26.04 60   # 需要關閉 WSL 時，確認後加 --shutdown
```

  WSL 只有在該機器的虛擬磁碟沒掛在 WSL VM 上時才能調整。實測 `wsl --terminate` **不會**卸下（停止後仍回 `WSL_E_DISTRO_NOT_STOPPED`），要 `wsl --shutdown`，也就是**所有機器都會停止**。所以：從未啟動過（或 WSL 已關）的機器直接調整；其他情況 GUI 會列出會被停止的機器、按「關閉 WSL 並套用」才進行，CLI 則要 `--shutdown`。流程是停各機 docker → `wsl --shutdown` → 調整 → 把原本執行中的機器開回來（不論調整成敗）。WSL 內的終端、編輯器連線等也會中斷。
- wsld-gui／wsld.exe 若是從 WSL 檔案系統（`\\wsl.localhost\…`）執行，關閉 WSL 會讓它們中途結束，因此會拒絕並提示改從 Windows 資料夾執行。
- wsld-gui 閃退時，錯誤與 stack 會寫到 `%APPDATA%\wsld\crash.log`。
- **縮小有下限，而且不只是已用空間**：檔案系統的固定開銷（例如 inode 表）是依「當初格式化的大小」配置的。1 TB 格式化的發行版即使只用了 30 GB，`resize2fs` 的最小值也約 70 GiB（實測：inode 表就占 16 GiB）。執行中的機器在對話框會顯示「最小可設約 N GB」並擋下更小的值，在關閉 WSL 之前就拒絕；停止中的機器無法預估，WSL 回報太小時會顯示同樣的易讀訊息。CLI 同樣會先檢查。縮小前請先備份：`wsl --export <distro> backup.tar`。
- **從 1 TB 縮到很小可能要分兩次**：最小值是依「目前」的配置估計的，縮小後配置變緊湊，最小值也會下降。實測 Ubuntu-26.04（約 30 GB 資料）：1 TB 時最小約 71 GB → 先縮到 72 GB → 重新估計只剩約 56 GB → 再縮到 60 GB 成功。每次都需要關閉 WSL。
- **縮小後 Windows 上的 vhdx 檔可能反而變大**：resize2fs 搬動資料，舊位置不會自動還給 Windows（實測 32 GB → 52.7 GB）。因此 wsld 調整成功後會在同一次關機中接著壓縮（`wsl --manage <d> --compact`），不必再停一次機器。

## 釋放磁碟空間（還給 Windows）

vhdx 是動態成長的：刪掉 Linux 裡的檔案後，Windows 上的檔案不會自動變小。機器卡片的「釋放磁碟空間」鈕（縮小箭頭圖示）或 CLI：

```powershell
wsld compact-disk Ubuntu-k3s-poc              # 磁碟沒被 WSL 占用時直接壓縮
wsld compact-disk --shutdown Ubuntu-26.04     # 需要關閉 WSL 時，確認後加 --shutdown
```

與調整大小相同：壓縮需要 VHD 沒掛在 WSL VM 上。執行中（或停止但仍被占用）的機器要 `wsl --shutdown`，GUI 會列出會被停止的機器並確認，完成後把原本執行中的機器開回來；從 `\\wsl.localhost\…` 執行 wsld 時會拒絕。完成後顯示釋放了多少、Windows 上現在佔用多少。實測 k3s-poc：4.71 GB → 3.71 GB（2 秒）。
- **要比下限更小**：只能匯出再匯入成新的、較小的磁碟，例如 `wsl --export <d> d.tar` → `wsl --unregister <d>` → 在 `%UserProfile%\.wslconfig` 的 `[wsl2]` 設 `defaultVhdSize=60GB` 後 `wsl --import <d> <目錄> d.tar`（此流程未在本機實測；unregister 會刪除原磁碟，務必先確認 tar 完整）。
- 等同的 WSL 指令：新增 `wsl --install -d Ubuntu --vhd-size 60GB`；調整 `wsl --manage <distro> --resize 60GB`；全域預設可在 `%UserProfile%\.wslconfig` 的 `[wsl2]` 加 `defaultVhdSize=60GB`（影響之後所有新建的發行版）。

## 已知限制

- **平台**：Windows 11 x64 + WSL2；未測 Windows 10、arm64（arm64 見 Roadmap Extra）。
- **多 distro（0.5）**：多 profile、單一 active；每 managed machine 自有 API 埠；舊扁平 config 載入時自動遷移。
- **rootful dockerd**：容器以 root 執行 dockerd（bootstrap 有把使用者加入 `docker` 群組）。rootless 不在範圍。
- **無 npiperelay（Extra）**：Windows 側 `docker.exe` 需以 `DOCKER_HOST=tcp://127.0.0.1:<port>` 連線，不提供 `//./pipe/docker_engine`。
- **GUI**：Theme（radio：Light／Dark／System）；Settings 在側欄；header 無 tcp 路徑。「開機自啟動」起 Tray，並自動帶起引擎。
- **自動更新（Extra）**：無；以發布頁下載新版覆蓋。
- **安全**：API 只綁 loopback、無 TLS；同機任何程序可存取 Docker API（等同本機 root）。不要把 `hosts` 改成 `0.0.0.0`。要讓內網連到**容器**，請開啟區網存取（wsld-gui 轉發），不要對外開放 API。
- **Kubernetes（k3s）**：單節點、同一時間一台 machine（有共用 engine 時固定在 engine）、不做 Ingress／NodePort 區網轉發／多叢集；Windows 端需自行安裝 `kubectl`（wsld 只在你手動匯出時合併 kubeconfig）。
- **區網存取只在 wsld-gui 執行時有效**，且只轉發 TCP；容器看到的來源 IP 是本機。
