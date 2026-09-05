# GUI（wsld-gui）

對應規格：[SPEC.md](SPEC.md) §5.5。Wails v2（WebView2）+ Vite/TypeScript 前端 + `fyne.io/systray`。

## 架構

```
┌─ wsld-gui.exe ───────────────────────────────────────────────┐
│ Wails 視窗（WebView2） ──HTTP/WS──▶ internal/gui.Server         │
│ systray（專屬 OS 執行緒）             │  Bearer token、僅 127.0.0.1  │
│                                      ▼                        │
│                       internal/{lifecycle,dockerapi,config,…} │
└──────────────────────────────────────────────────────────────┘
                 ▲ 同一套前端、同一個 API
┌─ wsld-gui-testhost（Linux/CI）────────┐
│ 服務 frontend/dist + FakeEngine       │◀── Playwright
│ /__test/* 控制情境                    │
└──────────────────────────────────────┘
```

- 前端**不**用 Wails binding，只透過 `internal/gui` 的 HTTP + WebSocket 橋接（`/api/*`）。這讓 Wails 應用與 Playwright 測試主機走**完全相同**的路徑，UI 邏輯零分歧。
- 橋接只監聽 loopback 隨機埠，每次啟動產生新 token；前端由 Wails asset middleware 注入 `/wsld-config.js` 取得 base URL 與 token。
- Go core 與 CLI 共用（`internal/*`），GUI 不重複實作任何 Docker／WSL 邏輯。

## 功能對照（SPEC §5.5）

| 區塊 | 行為 | Playwright 覆蓋 |
|---|---|---|
| Tray | 狀態列（5s ping）、啟動引擎、開啟主視窗、結束；按 X 只縮到 Tray | 實機手測（見下） |
| 標題列 | 連線徽章、顯示已停止、刷新、啟動引擎、設定 | `list.spec.ts` |
| 列表 | 名稱／映像／狀態／埠（IPv4/IPv6 去重）／操作；前景 3s 刷新，失焦或隱藏時暫停 | `list.spec.ts` |
| 欄寬 | `table-layout: fixed`：內容變動不改變欄寬（省略號截斷）；標頭右緣可拖曳調整，寬度存 localStorage | `columns.spec.ts` |
| Compose 分組 | 依 `com.docker.compose.project` 分組：專案標頭（running/total、折疊、整組 Start／Stop）、組內顯示 service 名；無 compose 容器時維持平面列表 | `groups.spec.ts` |
| Start／Stop | 呼叫 `/api/containers/{id}/start|stop`，列狀態即時更新 | `list.spec.ts` |
| Logs | 預設 tail 100；「即時」勾選走 WebSocket 串流 | `panels.spec.ts` |
| Exec 終端 | xterm.js ↔ `/api/containers/{id}/exec`（WS、hijack）；shell 依 `exec_shell` 或 bash→sh | `panels.spec.ts` |
| 狀態列 | 發行版、endpoint、API 狀態、刷新間隔、版本；永遠貼齊底部 | `list.spec.ts` |
| 設定 | 刷新間隔、終端 shell、登入時啟動 Tray（HKCU Run，預設關）；錯誤顯示於 banner | `settings.spec.ts` |
| i18n | 跟 OS 語系（zh→繁中）；`WSLD_LANG` 可覆寫 | `list.spec.ts` |
| 尚無 config | 主視窗可開，狀態列顯示「請先執行 wsld init」，啟動引擎回傳 not_ready | Go 單測（`configGate`） |

## 啟動參數

| 參數 | 說明 |
|---|---|
| `--tray` | 隱藏啟動，只顯示系統匣圖示（登入自啟採用此參數） |

## 建置

```bash
make build-frontend   # frontend/dist（tsc + vite）
make build-gui        # dist/wsld-gui.exe（GOOS=windows，-tags desktop,production，-H windowsgui）
make build-win        # 上述 + dist/wsld.exe
```

WebView2 執行期：Windows 11 內建；缺少時 Wails 會提示下載。

## 測試

```bash
make test-ui          # 建前端 → Playwright（自動啟動 wsld-gui-testhost）
cd frontend && npx playwright test --ui   # 互動除錯
```

Playwright 在 WSL 需要 Chromium 相依套件；本機若無 sudo，可將 `libnss3` `libnspr4` `libasound2t64` 解到 `~/.local/lib/pw` 並以 `LD_LIBRARY_PATH` 帶入（詳見 [TESTING.md](TESTING.md)）。

### 測試主機控制端點（僅 testhost）

| 端點 | 用途 |
|---|---|
| `POST /__test/reset` | 還原 fake 狀態（web running、db exited） |
| `POST /__test/engine {"down":bool}` | 模擬 API 不可達 |
| `POST /__test/lifecycle {...}` | 設定 `Status()`／`Start()` 回應 |
| `POST /__test/logs {"name","lines":[]}` | 推送即時日誌 |
| `POST /__test/lang {"lang":"zh-TW"}` | 切換語系 |
| `POST /__test/container?name=&image=&state=` | 新增容器；帶 `project=&service=` 則為 compose 容器 |
| `POST /__test/action-delay?ms=` | 讓 Start／Stop 延遲回應（測 pending 回饋） |

### 實機（Windows）手測項目

Playwright 無法涵蓋 Wails／systray 原生行為，v1 以下列步驟在 Windows 驗證（已於 Ubuntu-26.04 + WebView2 實測）：

1. `dist\wsld-gui.exe` 開啟後標題列「已連線」，列表顯示 `docker run` 的容器。
2. 按 X → 視窗隱藏、程序仍在（Tray 圖示保留）；Tray「開啟主視窗」可還原。
3. `dist\wsld-gui.exe --tray` → 不顯示視窗，只有 Tray。
4. Tray「結束」→ 程序退出。
5. 設定 → 勾「登入時啟動 Tray」→ `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\wsl-docker` 出現；取消勾即刪除。

## 已知限制（v1）

- 無 `.manifest`／`.syso`：DPI 由 Wails 自行宣告，可正常縮放；未含版本資源與圖示（Phase 4 視需要以 go-winres 補）。
- 深色主題跟隨系統（`windows.SystemDefault`），但前端配色僅淺色。
- Tray 選單文字在啟動時依語系決定，執行中切換 OS 語系不會即時更新。
