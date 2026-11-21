# 測試（TESTING）

一鍵：

```bash
make test          # scripts + Go + UI
make test-go       # 只跑 Go（含 windows 交叉 vet）
make test-scripts  # bats + shellcheck + Pester
make test-ui       # 建前端 + Playwright（見 docs/GUI.md）
```

前置：Go 1.22+（本機用 `~/.local/go`）、bats-core、shellcheck；Pester 用 Windows PowerShell 5.1 內建版本（透過 WSL interop 呼叫 `powershell.exe`）；Node 20+ 與 `npx playwright install chromium`。

Playwright 的 Chromium 在無 sudo 的 WSL 可能缺 `libnss3` `libnspr4` `libasound2t64`。`make test-ui`／`cd frontend && npm test` 會自動跑 `scripts/ensure-playwright-libs.sh`：以 `apt-get download` + `dpkg-deb -x` 解到 `~/.local/lib/pw` 並設定 `LD_LIBRARY_PATH`。有 sudo 時也可改跑 `npx playwright install-deps chromium`。

## 層級

| 層 | 工具 | 範圍 | 說明 |
|---|---|---|---|
| Bootstrap 腳本 | bats、shellcheck、Pester | `scripts/lib/*`、`scripts/tests/*` | 純函式：埠選擇、daemon.json、wsl.conf、參數、`wsl -l -v` 解析 |
| Go core | `go test ./...` | `internal/{config,dockerapi,wsl,lifecycle,doctor,i18n,cli,gui,lan,k8s,host}`、`scripts`（embed bundle） | 全部以 fake／httptest，不碰真 WSL 或 Docker |
| GUI 橋接 | `go test ./internal/gui` | `/api/*` HTTP + WebSocket（token、CORS、logs 串流、exec、images／volumes／networks／machines） | `FakeEngine`／`FakeLifecycle`／`FakeWSL`／`lan.FakeHost` |
| GUI 畫面 | Playwright | `frontend/tests/*.spec.ts`（`list`、`columns`、`groups`、`panels`、`settings`、`shell`、`views`、`info`、`lan`、`k8s`） | `wsld-gui-testhost` 服務 `frontend/dist` + fake；見 `docs/GUI.md` |
| CLI／GUI 端到端 | 手動／CI Windows runner | `dist/wsld.exe`、`dist/wsld-gui.exe` | 見下方「實機驗收」與 `docs/GUI.md` |

## Go 測試設計

- **`wsl.Runner`**：所有 `wsl.exe` 呼叫走介面；測試以 `fakeRunner` 依 `args` 串接鍵回應（含 UTF-16LE 輸出）。
- **`dockerapi`**：`httptest.Server` 模擬 Engine API；exec 以 `http.Hijacker` 模擬 101 Upgrade。
- **`lan.Forwarder`**：以真的 loopback 監聽測轉發、關閉、重試與排序；`Listen` 可注入（GUI 測試與 testhost 改綁 127.0.0.1 隨機埠）。`lan.PublishedTargets` 為純函式。
- **`lan.Manager`**（0.9.0 mirrored 清理與埠等待）：Windows 端副作用（`.wslconfig` 路徑、UAC 移除防火牆規則、規則數、網卡、netstat）走 `lan.Host`；測試用 `lan.FakeHost`。testhost 以 `/__test/lan?mirrored=1&fail=1&busy=8080` 模擬遺留設定、取消 UAC、埠被佔用；`fw=missing|blocked` 模擬 wsld-gui 尚未通過防火牆，`fwfail=1` 模擬加規則時取消 UAC，`fwhold=1`／`fwhold=0` 讓 UAC 停在等待中／放行；`GET /__test/lan` 的 `firewall_elevated` 列出提權過的程式與參數（netsh 或 `wsld-gui.exe --allow-firewall`）。防火牆判定與授權方式（`lan.EvaluateFirewall`、`lan.PlanFirewall`、`lan.NetshAddRuleArgs`、`lan.SamePath`）為純函式單元測試；COM 讀寫、netsh 與 UAC（`firewall_windows.go`）需在 Windows 實機驗證。
- **`lifecycle.Manager`**：`Sleep`／`PingInterval` 可注入，避免重試等待。
- **`doctor.Deps`**：`Processes`（tasklist）與 `Dial`（埠探測）可注入。
- **`cli.App`**：`NewAPI`、`Exec` 注入；測試斷言退出碼 0／1／2／3／4 與輸出片段。
- **i18n**：訊息鍵集中在 `internal/i18n/messages.go`；測試保證每個鍵有英文；`WSLD_LANG=en|zh-TW` 可強制。

### 退出碼對應（SPEC §5.4）

| 碼 | 觸發 |
|---|---|
| 0 | 成功；`status`／`doctor` 全就緒 |
| 1 | 一般錯誤（如容器 404） |
| 2 | 未知命令、旗標錯誤、缺參數 |
| 3 | `config` 不存在、`lifecycle.ErrNotReady`、doctor engine 類 Fail |
| 4 | `lifecycle.ErrAPI`／`dockerapi.ErrUnreachable`、doctor 僅 api 類 Fail |

## 實機驗收（Windows）

```bash
make build-win                        # dist/wsld.exe + wsld-gui.exe（VERSION←git exact tag，預設 0.16.0）
./dist/wsld.exe init                  # 在 WSL Ubuntu 內安裝 Docker、寫 %APPDATA%\wsld\config.toml
curl.exe http://127.0.0.1:23750/_ping # OK
./dist/wsld.exe status && ./dist/wsld.exe doctor
DOCKER_HOST=tcp://127.0.0.1:23750 docker run -d --name t -v "$PWD:/app" alpine sleep 600
./dist/wsld.exe ps && ./dist/wsld.exe logs t && ./dist/wsld.exe stop-ct t && ./dist/wsld.exe start-ct t
```

區網存取（需另一台同網段的設備）：

```bash
./dist/wsld.exe lan restore-nat --yes # 若 0.9.0 曾開過 mirrored：UAC 移除規則、還原 NAT、重啟 WSL
DOCKER_HOST=tcp://127.0.0.1:23750 docker run -d -p 8080:80 nginx:alpine
# 啟動 wsld-gui（尚未授權時）→ 不跳 UAC；Settings › 網路 › 區網存取 開啟 → 說明對話框 → 繼續 → UAC 顯示「網路命令殼層」（Microsoft Windows，詳細資料有完整 netsh 規則）→ 是；授權前不出現 Windows 安全性警訊
# 以 wf.msc 替 wsld-gui.exe 加一條對內封鎖規則 → 再授權：對話框說明由 wsld-gui 處理，UAC 顯示 wsld-gui，完成後封鎖規則停用
# 另一台設備：http://<內網IP>:8080 → nginx 首頁；curl http://<內網IP>:23750/_ping → 必須連不到（API 不轉發）
# 區網關閉時容器頁 nginx 列的地球按鈕開 http://localhost:8080；開啟後改開 http://<內網IP>:8080；停掉容器：該位址消失；關閉開關：另一台設備連不到，本機 127.0.0.1:8080 仍可用
```

本機 Kubernetes（k3s；需 Windows 端裝有 `kubectl`）：

```bash
./dist/wsld.exe k8s on                # 首次安裝＋啟動＋合併 kubeconfig
kubectl --context wsld-Ubuntu-26.04 get nodes   # Windows 與 WSL 內皆 Ready
docker build -t poc-web:local . && kubectl --context wsld-Ubuntu-26.04 create deployment poc-web --image=poc-web:local --dry-run=client -o yaml   # 改 imagePullPolicy: Never 後 apply → Running
./dist/wsld.exe k8s status && ./dist/wsld.exe doctor   # k3s OK；記憶體／node IP／埠
# wsl --shutdown → ./dist/wsld.exe start：k3s 自動恢復（node Ready、pod 還在）
./dist/wsld.exe k8s on --machine <另一台>   # 必須拒絕並指出目前開啟者
./dist/wsld.exe k8s off                # 釋放 6443 與記憶體；Containers 頁沒有殘留 k8s_* 容器
./dist/wsld.exe k8s reset --yes        # 乾淨叢集（先 on）；既有 kubeconfig 的其他 context 不受影響（~\.kube\config.wsld-bak 為原檔備份）
# GUI：Kubernetes 頁一鍵啟動→Ready；Pods 列表、logs 串流、終端、刪除；Deployment 調副本數；Service NodePort 地球鈕；一鍵停止→Off
```

2026-11-21（0.16.0）k3s 實機：以拋棄式 distro、隔離的 `WSLD_CONFIG`／`USERPROFILE` 跑 CLI 全流程（見 ROADMAP「實機發現」的 5 個問題與修正）；GUI Kubernetes 頁與 Windows `kubectl.exe` 仍待手測。

2026-09-06 於 Ubuntu-26.04（WSL2）實測：Docker CE 29.8.0、Compose v5.5.1、埠 23750、doctor 11 OK。
同日 `wsld-gui.exe` 實測：WebView2 開窗、已連線、列出 `nginx:alpine` 容器（埠去重）、按 X 縮 Tray、`--tray` 隱藏啟動。

## 規範

- TDD：先寫失敗測試再實作（`.cursor/rules/tdd-and-ui-testing.mdc`）。
- 新增 `wsl.exe` 互動 → 先加 fake 回應；新增 API 端點 → 先加 httptest 案例。
- UI 改動必附 Playwright 測試；需要新情境時先擴充 `FakeEngine`／`/__test/*` 再寫 spec。
