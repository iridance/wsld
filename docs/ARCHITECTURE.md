# wsl-docker 架構說明

## 1. 邏輯視圖

```text
                    Windows Host (amd64)
┌──────────────────────────────────────────────────────────┐
│  wsld-gui.exe (Wails + Playwright 測試)                  │
│  wsld.exe (CLI)                                          │
│       └─ internal: lifecycle / dockerapi / config / doctor│
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
| `wsld-gui` | Tray、列表、Logs、Exec、ensure |
| `internal` | 唯一業務邏輯來源 |
| bootstrap 腳本 | 裝 WSL 功能與 dockerd |

## 3. v1 vs v2 UI

| 版本 | UI |
|------|-----|
| v1 | Wails／WebView2 |
| v2 | WinUI 3 |
