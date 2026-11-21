# wsld 產品規格（SPEC）

版本：0.17.1（git tag `v0.17.1`）  
狀態：**0.17.1 已交付**（Phase 0–4 基底 2026-09-06；Phase 5 多 distro＋GUI 補完見 Roadmap）
最後更新：2026-11-20

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
| `wsld`（CLI） | 建立／啟動 WSL、ensure dockerd、腳本化管理；可切換主要 machine |
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
- 完整 Kubernetes 發行版（kubeadm／minikube／kind）、多節點／多叢集、YAML 編輯器；Compose 視覺 IDE（單節點 k3s 自 0.16.0 起支援，見 §4.6）
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
| 退出碼 | 0／1／2／3／4（見 §5.4） |
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
- 禁止綁 `0.0.0.0`（只限 API。容器 published 埠可經「區網存取」開放給內網，見 §4.5）

### 4.3 Client

- Go `internal/*`：lifecycle、dockerapi、config、doctor
- CLI：`cmd/wsld` → `wsld.exe`
- GUI：Wails → `wsld-gui.exe`
- 僅正式支援 **windows/amd64**

### 4.4 Distro

- 偵測已安裝 Ubuntu 系列並沿用；否則 `wsl --install` 官方 Ubuntu（`--vhd-size`：`init --vhd-size <GB>`，預設 60，0＝WSL 預設 1 TB）
- 虛擬磁碟上限（0.17）：允許 8–16384 GB；新增機器以 `wsl --install --vhd-size <n>GB` 指定，既有機器以 `wsl --manage <d> --resize <n>GB` 調整（成功後接著 `--compact`），亦可單獨以 `--compact` 釋放空間；兩者共用 `wsl.WithFreeVHD`。VHD 須未掛在 WSL VM 上；實測 `--terminate` 不會卸下，回 `WSL_E_DISTRO_NOT_STOPPED`（訊息在 stdout），要 `wsl --shutdown`。故目標未執行時先直接試，仍被占用（或目標執行中）則需使用者同意（GUI 409 `needs_shutdown` 附 `running`；CLI `--shutdown`）：停各機 docker → `--shutdown`（被輪詢叫醒時重試，最多 3 次）→ 調整 → 原本執行中的機器 `wsl -d <d> -- true` 開回來（不論成敗）。調整期間 `/config` 不進 distro 執行命令。執行檔位於 `\\wsl.localhost\…`／`\\wsl$\…` 時拒絕關閉 WSL。目標執行中時先以 `findmnt`＋`stat -f -c %S`＋`resize2fs -P` 估計最小上限（區塊數×區塊大小，進位後加 1 GB），小於它就在關閉 WSL 前拒絕（GUI 400 `too_small`＋`min_gb`；`GET …/resize-info` 供對話框顯示）；WSL 回 `New size smaller than minimum (N)` 時換算成同樣的易讀訊息（422 `too_small`）。上限讀 vhdx（Lxss 登錄檔 `BasePath`＋`VhdFileName`）標頭的 Virtual Disk Size metadata，執行中也讀得到、不喚醒 distro
- `wsl.conf`：`systemd=true`
- `loginctl enable-linger`：啟用（doctor 會查）
- **0.5**：可管理多個 distro profile，同一時間一個 active（見 Roadmap Phase 5）

### 4.5 區網存取（LAN access）

- 目標：讓容器 published 埠可從其他設備透過 Windows 內網 IP 連入。
- 做法：**wsld-gui 內建轉發器**：
  - 在每個內網 IPv4 上監聽執行中容器的 published TCP 埠，轉到 `127.0.0.1:<埠>`（WSL localhost 轉發）。
  - 每 3 秒依容器清單同步。
  - 由 Windows 程式監聽，需通過 Windows 防火牆：規則 `wsld-gui (LAN access)`：TCP、私人＋公用、RemoteAddresses=LocalSubnet，綁定 wsld-gui.exe 路徑。
    - **只在使用者操作時請求**（開啟區網存取時尚未授權，或設定頁「授權防火牆」）；wsld-gui 啟動時不提權。提權前先顯示 wsld 說明對話框（規則內容、UAC 會顯示的程式），按「繼續」才 `ShellExecuteEx runas`。
    - 一般提權 `%SystemRoot%\System32\netsh.exe advfirewall firewall add rule …`（UAC 顯示 Microsoft 已驗證的「網路命令殼層」）。
    - 有該 exe 的對內封鎖規則、已有 wsld 規則，或 exe 位於 UNC 路徑時，改提權 `wsld-gui.exe --allow-firewall`：透過 `HNetCfg.FwPolicy2` 新增或更新規則並停用封鎖規則；結束碼為 HRESULT。
    - 讀取狀態不需提權（`GET /api/lan` 的 `firewall`、`firewall_method`）。missing／blocked 時不在內網 IP 監聽（避免 Windows 自行跳出安全性警訊、按取消產生封鎖規則）。
- 開關存在 `config.toml` 的 `lan_access`。只在 wsld-gui 執行時（含系統匣）有效。
- WSL 維持 NAT：不改 `.wslconfig`、不重啟 WSL；只有加入防火牆規則時需要一次 UAC（使用者確認說明後請求，規則綁定 exe 路徑）；開關區網存取只改設定。
- 不轉發：
  - 明確綁 `127.0.0.1` 的埠
  - Docker API 埠
  - UDP
- 容器看到的來源 IP 是本機。
- 0.9.0 曾用 mirrored＋Hyper-V 防火牆。偵測到 `.wslconfig` 的 `networkingMode=mirrored` 時提示「還原 NAT」（`wsld lan restore-nat`）：
  - UAC 移除規則、移除 `networkingMode`
  - `wsl --shutdown`
  - 等 API 埠釋放後只啟動 docker.service（不重跑 bootstrap；啟動前 `systemctl reset-failed docker`）

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

### 4.6 本機 Kubernetes（k3s）

- 目標：每台 machine 可**選擇開啟**單節點 k3s（預設關），與該 distro 的 dockerd 共用映像檔——`docker build` 完 `imagePullPolicy: Never` 即可部署，不必架 registry；Windows 側 `kubectl` 經 `127.0.0.1:6443` 連線。
- 做法：
  - 安裝：`03-k3s.sh install`（內嵌 `scripts.K3sBundle()`）以 `--docker --disable traefik --write-kubeconfig-mode 644` 裝 k3s（k3s 內建 cri-dockerd，node 的 runtime 為 `docker://…`）。k3s.service 加 drop-in `After=docker.service`／`Requires=docker.service`：開機時 k3s 若早於 dockerd，agent 不會啟動 kubelet（node 永遠 NotReady）。unit 預設 disable，啟停由 wsld 管（`systemctl start k3s`）。
  - 單一叢集：所有 WSL2 distro 共用同一個 VM 網路命名空間（6443、flannel CIDR、kube-proxy 規則、node 名稱＝Windows 主機名），所以 config 中**同一時間只能有一台 machine `k8s = true`**；第二台被拒絕並指出目前開啟者（GUI 可一鍵移動）。
  - 停止：`k3s-killall.sh`（`--docker` 時 pod 容器不會隨 service 停止）。重置：停止後清 `/var/lib/rancher/k3s`、`/var/lib/kubelet`、`/etc/rancher/k3s` 再啟動。移除：`k3s-uninstall.sh`。
  - kubeconfig：把 k3s.yaml 以 `wsld-<distro>` 為 context／cluster／user 名稱**合併**進 `%USERPROFILE%\.kube\config`（保留既有項目、註解與 current-context；首次修改前備份為 `config.wsld-bak`；無法解析時不動原檔）；server 固定 `https://127.0.0.1:6443`（憑證 SAN 含 127.0.0.1）。
  - 資源管理經 `wsl.exe … k3s kubectl …`（root）；不引入 client-go。使用者輸入進引數前驗證為 DNS 標籤，避免被當成旗標。
  - 重開機：`wsld start`／GUI 啟動引擎時，若 primary 為 `k8s = true` 則一併啟動 k3s。實測 `wsl --shutdown` 後 node IP 未變、k3s 自動恢復；k3s 每次啟動重新偵測 node IP，Windows 側一律走 127.0.0.1，故不預先綁固定位址，改由 doctor 偵測 node IP 與目前 eth0 不一致。
- 資源：單節點待機約 0.6 GB（k3s）＋dockerd；doctor 於可用記憶體不足時警告。
- 不做：Ingress（Traefik 停用）、NodePort／LoadBalancer 的區網轉發、多節點。

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

0.5 形狀：全局 `lang`／`tray_autostart`／`refresh_interval_sec`／`exec_shell`／`theme`；`active`＋多筆 `[[machines]]`（各含 `distro`／`user`／`api_endpoint`，以及選填 `k8s = true`＝該 machine 開啟 k3s，全部 machine 最多一台，見 §4.6）。舊扁平格式載入時自動遷移。範例見 `config.example.toml`。

### 5.3 Bootstrap

`02-bootstrap-docker.sh`（冪等）：

1. 安裝 Docker CE + **Compose plugin**
2. 使用者加入 `docker` 群組（rootful）
3. daemon hosts：unix socket + `tcp://127.0.0.1:<chosen-port>`
4. `systemctl enable --now docker`
5. enable linger
6. `docker info` 成功

`03-k3s.sh install|uninstall`（選用，§4.6）：安裝／移除單節點 k3s，由 `wsld k8s` 觸發，不在 `init` 預設執行。

`01-enable-wsl.ps1`（Admin）：啟用 WSL2 功能、update、必要時裝 Ubuntu、重開機提示。

### 5.4 CLI（`wsld`）

| 命令 | 行為 |
|------|------|
| `init` | distro 策略、bootstrap、寫／更新該 machine profile（含 API port）並可設 active |
| `start` | 喚醒 active distro、ensure docker、探測 API |
| `use` | 切換主要 machine（與 GUI「設為主要」同寫 config） |
| `unmanage` | 停該 distro docker 後刪 profile（保留 WSL） |
| `unregister` | `wsl --unregister`（需 `--confirm <name>`）＋刪 profile |
| `compact-disk [--shutdown] <distro>` | 壓縮 VHDX（`wsl --manage --compact`），把不再使用的空間還給 Windows；被占用時同 `resize-disk` 需 `--shutdown`；報告釋放量 |
| `resize-disk [--shutdown] <distro> <GB>` | 調整虛擬磁碟上限（`wsl --manage --resize`，成功後在同一個空檔 `--compact`）；VHD 被 WSL 占用時需 `--shutdown`（所有機器停止，完成後再啟動），未帶則退出碼 3 並列出會被停止的機器；縮小時資料須放得下 |
| `status` | WSL／docker／API 狀態（對準 active） |
| `ps` | 容器列表 |
| `logs <id>` | 預設尾部 100 行；`--follow` 可選 |
| `start-ct`／`stop-ct` | 啟停容器 |
| `doctor` | 見 §5.6 |
| `exec`（可選對齊 GUI） | 進入容器 shell（策略同 GUI） |
| `k8s on\|off\|status\|reset\|kubeconfig\|uninstall` | 單節點 k3s（§4.6）：`on` 未安裝則先安裝、啟動、合併 kubeconfig，成功後才寫 `k8s = true`；`off` 停止（`k3s-killall.sh`）並清旗標；`status [--json]`；`reset`／`uninstall` 需 `--yes` 或互動確認；`--machine` 指定 machine |
| `lan on\|off\|status\|restore-nat` | 區網存取（§4.5）：on／off 寫入 `lan_access`（由 wsld-gui 轉發）；`status --json`；`restore-nat [--yes]` 還原 0.9.0 的 mirrored 設定並重啟 WSL |

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
| Machines | 列表＋設為主要／folder／terminal（底部 panel、user home）／取消管理／unregister／右側 Info（`.wslconfig` memory／processors、wsl.conf（停止中不讀：顯示「啟動機器後才能查看」，不喚醒）、tcp endpoint、CPU／Mem 摘要、磁碟上限＋調整）；新增機器（磁碟上限，預設 60 GB；下載進度：進度條、已下載／總量、速度、經過時間） |
| Settings | 側欄視圖：refresh、exec_shell、tray_autostart、Theme（Light／Dark／System）；**網路 › 區網存取**（§4.5，即時套用；顯示狀態；未授權防火牆時先顯示說明對話框；偵測到 mirrored 時「還原 NAT」）。Containers 各服務列常駐地球按鈕，以系統瀏覽器開啟區網位址（區網存取關閉或無轉發時開 `http://localhost:<埠>`；多埠跳選單），Info 以文字列出 |
| Kubernetes | 頂部導覽（Machines 旁）：一鍵啟動／停止（背景作業＋輪詢，首次含安裝）、重置、匯出 kubeconfig、叢集資訊卡（狀態、版本、node、API、context、記憶體）、另一台持有時一鍵移動；namespace 下拉＋ Pods（logs 串流、exec 終端、刪除）／Deployments（scale、rollout restart、刪除）／Services（NodePort 開瀏覽器、刪除）；Containers 頁把 `k8s_*` 容器摺成預設收合的群組（§4.6） |
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
- k3s（僅有 machine `k8s = true` 時）：就緒／未就緒／失敗、node IP 漂移、記憶體、6443 被占用、80／443 提示；一律只警告，不影響退出碼
- 區網存取：開啟時列出（Info）；偵測到 0.9.0 遺留的 mirrored 模式時 Warn（提示 `wsld lan restore-nat`）；不影響退出碼

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
