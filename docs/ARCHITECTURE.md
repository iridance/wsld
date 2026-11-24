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

## 3. 共用 engine（可選）

預設每個被管理的 distro 自帶 dockerd。也可以指定**一個專用 engine distro**，其餘 distro 當 client：

```text
┌─────────────── WSL2 VM（所有 distro 共用 kernel、network namespace、/mnt/wsl tmpfs）───────────────┐
│  wsld-engine (role=engine)             Ubuntu / Debian1 … (role=client)                            │
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

## 4. v1 vs v2 UI

| 版本 | UI |
|------|-----|
| v1 | Wails／WebView2 |
| v2 | WinUI 3 |
