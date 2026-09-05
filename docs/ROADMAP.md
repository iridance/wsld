# wsl-docker Roadmap

版本：0.4  
對應規格：[SPEC.md](SPEC.md)（**v1.0 決策已鎖定**）

## 總覽

```text
Phase 0  規格／倉庫骨架 + v1 決策鎖定     ✅
Phase 1  Bootstrap 腳本可手動跑通         ✅
Phase 2  共用核心 + wsld CLI              ✅
Phase 3  GUI v1（Wails + Playwright）     ✅
Phase 4  打磨（v1.0 必要）                ✅  → v1.0
Phase 5  （可選）npiperelay、自動更新、多 distro
Phase 6  WinUI 3 GUI                       ← v2.0

v1.0 = Phase 1 + 2 + 3 + 4 完成並通過 SPEC §6
v2.0 = Phase 6
```

---

## Phase 0 — 規格與倉庫

- [x] 倉庫結構、README、SPEC、ROADMAP、ARCHITECTURE、config.example
- [x] GUI 納入 v1.0；WinUI 列 v2.0
- [x] TDD + UI 測試 rule
- [x] grill-me 關閉 v1 開放問題並回寫 SPEC §2

**退出條件**：可開始 Phase 1。

---

## Phase 1 — Bootstrap

**目標**：腳本裝好 Ubuntu + rootful dockerd + `127.0.0.1:23750`（可換埠）。

### 交付

- `scripts/01-enable-wsl.ps1`
- `scripts/02-bootstrap-docker.sh`（CE + Compose plugin + linger + hosts）
- `docs/BOOTSTRAP.md`

### 完成定義

- [x] 手動跑通；Windows `curl http://127.0.0.1:<port>/_ping` → `OK`（2026-09-06 Ubuntu-26.04 實測）
- [x] WSL `docker info`／`docker compose version` 成功（CE 29.8.0／Compose v5.5.1）
- [x] 只綁 loopback；占用時能選下一埠（bats 覆蓋 + doctor 檢查）

---

## Phase 2 — Core + `wsld` CLI

### 交付

- Go module；`cmd/wsld`；`internal/{lifecycle,dockerapi,config,doctor}`
- `%APPDATA%\wsl-docker\config.toml`
- 命令：`init`／`start`／`status`／`ps`／`logs`／`start-ct`／`stop-ct`／`doctor`（退出碼 0–4）
- TDD；`docs/TESTING.md`（Go 部分）

### 完成定義

- [x] SPEC CLI 行為與退出碼（`internal/cli` 測試覆蓋 0–4）
- [x] 無 Desktop 可驗證生命週期＋API（實機 init → status → ps/logs/stop-ct/start-ct）
- [x] 自動換埠寫入 config（bootstrap 輸出 `WSLD_API_PORT` → `lifecycle.Init` → `config.Save`）

---

## Phase 3 — GUI v1（Wails + Playwright）

### 交付

- `wsld-gui.exe`（Wails v2）：Tray、列表、Start／Stop、Logs（100／Live）、**Exec 終端**、狀態列
- 刷新：前景 3s；Tray／失焦暫停
- 語系：跟 OS（zh→繁中）
- `docs/GUI.md`；Playwright 關鍵路徑；`docs/TESTING.md` UI 指令
- 與 `internal` 共用，禁止雙重邏輯

### 完成定義

- [x] SPEC §5.5／§6 GUI 相關項（Wails v2 + systray；`internal/gui` HTTP/WS 橋接；實機驗證見 docs/GUI.md）
- [x] UI 測試一鍵可跑（`make test-ui`：Playwright 18 案 + `go test ./internal/gui`）

---

## Phase 4 — 打磨（**v1.0 必要**）

### 交付

- 錯誤字串（WSL／systemd／埠／Desktop）
- README 快速開始（CLI + GUI）
- 登入起 Tray：**可選、預設關**
- 重開機文件、Desktop 並存建議、已知限制
- `LICENSE`（MIT）

### 完成定義

- [x] SPEC §6 全過 → **v1.0**（2026-09-06）
  - §6.1–3 CLI／GUI 同引擎、GUI 冷開→Start→列表／Logs／Exec：實機 + Playwright
  - §6.4 `docker run -v "$PWD:/app"` 於 WSL `$HOME`：實測 OK
  - §6.5 `systemctl stop docker` 後 `wsld start` 3 秒恢復（等同重開機路徑）
  - §6.6–7 doctor 誤綁／Desktop、埠衝突換埠：Go 單測 + bats
  - §6.8 `make test`；§6.9 docs/OPERATIONS.md；§6.10 LICENSE（MIT）

---

## Phase 5 — 可選（v1 後）

- npiperelay、多 distro、自動更新、Compose 專案視圖等

## Phase 6 — WinUI（v2.0）

- 見 SPEC §1.4；另訂 `docs/GUI-WINUI.md`

---

## 優先序

1. 碼在 WSL、引擎同 distro（rootful）  
2. 本機 TCP（23750+）  
3. v1.0 = Phase 1–4；TDD + Playwright  
4. v2.0 = WinUI  
5. 只綁 127.0.0.1  
6. GUI／CLI 共用 core  

## 里程碑

| Phase | 版本角色 |
|-------|----------|
| 0 | 前置（已鎖定決策） |
| 1–4 | **v1.0 必要** |
| 5 | 之後可選 |
| 6 | **v2.0** |
