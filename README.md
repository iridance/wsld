# wsl-docker（`wsld` / `wsld-gui`）

Windows 上的輕量 Docker 控制面：把 **WSL2 Ubuntu** 當成開發 machine（原始碼在 Linux FS），同機跑 **rootful dockerd**，再用 **`wsld` CLI + `wsld-gui`（Wails）** 做就緒與容器管理。

- **v1.0** = Roadmap Phase 1–4（含 GUI + 打磨）；決策見 [docs/SPEC.md](docs/SPEC.md) §2  
- **v2.0** = WinUI 3（Phase 6）

## 文件

| 文件 | 內容 |
|------|------|
| [docs/SPEC.md](docs/SPEC.md) | v1.0 鎖定規格 |
| [docs/ROADMAP.md](docs/ROADMAP.md) | 分期；v1.0 = Phase 1–4 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架構 |
| [config.example.toml](config.example.toml) | 設定範例 |
| `.cursor/rules/tdd-and-ui-testing.mdc` | TDD；UI → Playwright |

## 架構一覽

```text
Windows                                 WSL2 Ubuntu
┌─────────────────────────────┐         ┌──────────────────────────┐
│  wsld-gui.exe (Wails)       │         │  $HOME repos             │
│  wsld.exe (CLI)             │  TCP    │  dockerd (rootful)       │
│  shared internal/           │────────►│  127.0.0.1:23750+        │
└─────────────────────────────┘         │  + docker.sock           │
                                        └──────────────────────────┘
```

## 狀態

Phase 0：**v1 決策已鎖定**，實作尚未開始。下一步 = Phase 1 bootstrap。

## 快速約定

1. 原始碼放 WSL `$HOME`，不要放 `C:\` 當主目錄。  
2. 用 WSL Remote 開發。  
3. 避免與 Docker Desktop 雙引擎混用。  
4. 預設 API 埠 **23750**（占用會自動換）。  
5. 重開機後 `wsld start` 或 GUI Start。

## 授權

**MIT**（`LICENSE` 於 Phase 4／發布前落地）。
