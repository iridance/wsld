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
│  dockerd + containerd + k3s            docker CLI + compose／buildx plugin（無 daemon）             │
│  data-root 在自己的 ext4 VHD           docker context "wsld" ─┐                                    │
│  unix:///mnt/wsl/wsld/docker.sock ◄────────────────────────────┘（chmod 0666，GID 各 distro 不同）  │
│  tcp://127.0.0.1:<port>  ◄── Windows（wsld／wsld-gui）                                              │
└────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

- 角色存在 `config.toml` 的 `machines[].role`（`engine`｜`client`｜省略＝獨立）；`Config` 保證最多一台 engine，且有 engine 時 k8s 只能開在 engine 上（`k3s --docker` 必須與 dockerd 同 distro）。client 的 `api_endpoint` 沿用 engine 的，init client 不會改 active。
- engine 由 wsld-gui 常駐保活（`internal/wsl/keepalive.go`，與主要機器一起）；WSL 在沒有 session 時會回收 distro。
- `/mnt/wsl` 是記憶體 tmpfs，只放 socket；data-root 留在 engine 的 ext4。
- **限制**：dockerd 在 engine distro 解析 bind mount 路徑，client 內的 `/home/...` engine 看不到；只有 `/mnt/c/...` 與 named volume 可用。路徑改寫 proxy 見 [ROADMAP](ROADMAP.md)。

## 4. v1 vs v2 UI

| 版本 | UI |
|------|-----|
| v1 | Wails／WebView2 |
| v2 | WinUI 3 |
