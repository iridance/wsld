# wsld 架構說明

## 1. 邏輯視圖

```text
                    Windows Host (amd64)
┌──────────────────────────────────────────────────────────┐
│  wsld-gui.exe (Wails + Playwright 測試)                  │
│  wsld.exe (CLI)                                          │
│       └─ internal: lifecycle / dockerapi / wsl / config /  │
│                    doctor / gui（HTTP+WS 橋接）            │
│              TCP → 127.0.0.1:23750+（config）             │
└─────────────────────────────▲────────────────────────────┘
                │ wsl.exe     │ localhost forward
                ▼             │
┌─────────────────────────────┴────────────────────────────┐
│  Ubuntu WSL2（沿用既有或新裝）                            │
│  $HOME 專案 · rootful dockerd · compose plugin           │
│  unix:///var/run/docker.sock + tcp://127.0.0.1:<port>    │
└──────────────────────────────────────────────────────────┘
```

細節與鎖定決策見 [SPEC.md](SPEC.md) §2–§5。

## 2. 元件邊界

| 元件 | 職責 |
|------|------|
| `wsld` | init／start／ps／logs／doctor／… |
| `wsld-gui` | Tray、側欄五視圖（Containers／Images／Volumes／Networks／Machines）、Logs、Exec、ensure |
| `internal` | 唯一業務邏輯來源 |
| bootstrap 腳本 | 裝 WSL 功能與 dockerd |

## 3. 共用 engine（專用 docker-engine）

一個**專用 engine distro**（`docker-engine`，由 wsld 自動建立，走 `lifecycle.InitEngine` 專門流程、以 root 執行 dockerd、不建 Linux 帳號）跑唯一的 dockerd，其餘 distro 當 client：

```text
┌─────────────── WSL2 VM（所有 distro 共用 kernel、network namespace、/mnt/wsl tmpfs）───────────────┐
│  docker-engine (role=engine)             Ubuntu / Debian1 … (role=client)                            │
│  dockerd + containerd            docker CLI + compose／buildx plugin（無 daemon）             │
│  data-root 在自己的 ext4 VHD           docker context "wsld" ─┐                                    │
│  unix:///mnt/wsl/wsld/docker.sock ◄────────────────────────────┘（chmod 0666，GID 各 distro 不同）  │
│  tcp://127.0.0.1:<port>  ◄── Windows（wsld／wsld-gui）                                              │
└────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

- 角色存在 `config.toml` 的 `machines[].role`（`engine`｜`client`｜省略＝獨立）；`Config` 保證最多一台 engine。client 的 `api_endpoint` 沿用 engine 的，init client 不會改 active。
- engine 由 wsld-gui 常駐保活（`internal/wsl/keepalive.go`，與主要機器一起）；WSL 在沒有 session 時會回收 distro。
- `/mnt/wsl` 是記憶體 tmpfs，只放 socket；data-root 留在 engine 的 ext4。
- **bind mount（曝露）**：dockerd 在 engine distro 解析 bind 路徑，所以 client 要把自己的根目錄（單一檔案系統、非遞迴 `mount --bind /`）掛到共用的 `/mnt/wsl/distro/<名稱>`，掛載會傳播給所有 distro，engine 就能在**同一個絕對路徑**看到同一份檔案；在那條路徑底下執行 `docker compose`，`./backend:/app` 就會展開成 engine 也解析得到的路徑，不需要改寫任何請求。client 內的 `docker` shim 會自動處理（見 OPERATIONS）。
- **曝露要有生命週期**：傳播出去的掛載會讓那份 VHD 一直被接著，而且來源 distro 被 `wsl --terminate` 後仍殘留在其他 distro（實測：壓縮／調整大小不會報 busy，卻會把同一份 VHD 再接一次）。所以 wsld 只在執行 docker 時才曝露，用不到就 GC 放掉，停止／刪除／壓縮前先釋放，殘留的由 Supervise 與 `wsld doctor` 偵測並清掉。卸載一律是一般的 `umount`（由 engine 執行，乾淨傳播給所有 peer），絕不用遞迴或 lazy 卸載。

## 3a. 本機 Kubernetes（k8s-engine，可選）

k3s 跑在另一個專用 distro，但 pod 跑在 engine 的 dockerd 上（詳見 SPEC §4.6）：

```text
docker-engine (role=engine)                         k8s-engine (role=k8s, Debian)
  dockerd ── /mnt/wsl/wsld/docker.sock ◄──────────── cri-dockerd（--network-plugin= 空：docker 原生網路）
  /var/lib/docker/containers                             ▲ CRI（/run/wsld-cri-dockerd.sock）
     └─ro,private bind─► /mnt/wsl/wsld/k8s/containers ─bind─► /var/lib/docker/containers（kubelet 讀 log）
                                                      k3s server＋agent（kubelet root-dir、local-path 儲存在曝露路徑下）
  解析 bind 來源 ◄── /mnt/wsl/distro/k8s-engine/var/lib/wsld-k8s/…（k8s-engine 的根目錄，兩邊同一路徑、同一份磁碟）
  pod = docker 容器（docker0 網段，與一般容器互通）   API https://127.0.0.1:26443 ◄── Windows kubectl／其他 distro
```

- 程式：`internal/k8s`（Manager：Status／Enable／Start／Restart／Reset／Stop／Disable／Kubeconfig；`pods.go` ListPods／DeletePod、`services.go`、`stats.go`（engine 的 docker stats）、`forward.go`（engine 上的 socat 轉發容器）、`kubeconfig.go`（併入 ~/.kube/config））、`deploy.go`（Deploy／Apply）、`stack.go`（StackSpec → manifest、依相依順序 rollout）、`compose.go`（Compose → StackSpec、ResolveBind）、`internal/gui/k8sres.go`（Pods／Services／Stats／Forwards 端點、區網 NodePort）、`internal/gui/compose.go`（檔案瀏覽 `/api/fs/*`、Compose 預覽、Deploy／Apply／Stack 端點）、`scripts/03-k8s.sh`＋`scripts/lib/k8s-lib.sh`（`scripts.K8sBundle()`）、`internal/gui/k8s.go`（`/api/k8s*`、背景作業、Supervise）、`internal/cli/k8s.go`。
- 為什麼不是「kubelet 在 engine、控制面在 k8s-engine」或 kind／k3d：使用者要的是獨立的 k8s distro＋pod 直接是 engine 上的容器；跨 distro 的路徑用 `/mnt/wsl` 對應即可，沿用 client 曝露的同一套機制。

## 4. v1 vs v2 UI

| 版本 | UI |
|------|-----|
| v1 | Wails／WebView2 |
| v2 | WinUI 3 |
