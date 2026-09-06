package i18n

// messages：key → 語系 → 文字。%s/%d 等佈局需在各語系一致。
var messages = map[string]map[Lang]string{
	// ---- CLI 一般 ----
	"cli.usage": {
		EN: `wsld — manage Docker Engine inside a WSL2 Ubuntu distro

Usage: wsld <command> [options]

Commands:
  init       Pick/install Ubuntu, bootstrap Docker, write config
  start      Wake distro, ensure docker.service, wait for API
  use        Set active machine (first time runs init + start)
  unmanage   Stop docker for a machine and remove its profile (keeps WSL)
  unregister Unregister a WSL distro (requires --confirm <name>)
  status     Show WSL / docker / API status (--json)
  ps         List containers (-a: include stopped)
  logs       Show container logs (--tail N, default 100; --follow)
  start-ct   Start a container
  stop-ct    Stop a container (--time N)
  exec       Open a shell in a container (bash → sh, or exec_shell)
  doctor     Run diagnostics (--json)
  version    Print version

Exit codes: 0 ok, 1 error, 2 usage, 3 WSL/engine not ready, 4 API unreachable
Config: %s`,
		ZhTW: `wsld — 管理 WSL2 Ubuntu 內的 Docker Engine

用法：wsld <command> [options]

命令：
  init       選擇／安裝 Ubuntu、bootstrap Docker、寫入 config
  start      喚醒 distro、確保 docker.service、等待 API
  use        設為目前 machine（首次會自動 init + start）
  unmanage   停止該 machine 的 docker 並刪除 profile（保留 WSL）
  unregister 解除註冊 WSL 發行版（需 --confirm <名稱>）
  status     顯示 WSL／docker／API 狀態（--json）
  ps         列出容器（-a 含已停止）
  logs       容器日誌（--tail N，預設 100；--follow）
  start-ct   啟動容器
  stop-ct    停止容器（--time N）
  exec       進入容器 shell（bash → sh，或 exec_shell）
  doctor     診斷（--json）
  version    顯示版本

退出碼：0 成功、1 一般錯誤、2 用法錯誤、3 WSL／引擎未就緒、4 API 連線失敗
設定檔：%s`,
	},
	"cli.unknown_command": {EN: "unknown command %q (see: wsld help)", ZhTW: "未知命令 %q（見 wsld help）"},
	"cli.missing_arg":     {EN: "missing argument: %s", ZhTW: "缺少參數：%s"},
	"cli.config_missing": {
		EN:   "config not found at %s — run `wsld init` first",
		ZhTW: "找不到設定檔 %s — 請先執行 wsld init",
	},
	"cli.config_invalid":  {EN: "invalid config: %v", ZhTW: "設定檔無效：%v"},
	"cli.exit.not_ready":  {EN: "WSL/engine not ready", ZhTW: "WSL／引擎未就緒"},
	"cli.exit.api":        {EN: "Docker API unreachable", ZhTW: "Docker API 無法連線"},
	"cli.error":           {EN: "error: %v", ZhTW: "錯誤：%v"},
	"cli.config_saved":    {EN: "config written: %s", ZhTW: "已寫入設定：%s"},
	"cli.init_done":       {EN: "init complete. Next: wsld start / wsld ps, or open wsld-gui.", ZhTW: "init 完成。下一步：wsld start／wsld ps，或開啟 wsld-gui。"},
	"cli.start_done":      {EN: "engine ready at %s", ZhTW: "引擎就緒：%s"},
	"cli.use_done":        {EN: "active machine set to %s", ZhTW: "已將目前 machine 設為 %s"},
	"cli.unmanage_done":   {EN: "unmanaged %s (WSL distro kept)", ZhTW: "已取消管理 %s（保留 WSL 發行版）"},
	"cli.unregister_done": {EN: "unregistered %s", ZhTW: "已解除註冊 %s"},
	"cli.unregister_confirm": {
		EN:   "refusing unregister: pass --confirm %s (exact distro name)",
		ZhTW: "拒絕 unregister：請傳 --confirm %s（須與發行版名稱完全一致）",
	},
	"cli.machine_missing": {EN: "machine %q is not managed", ZhTW: "尚未管理 machine %q"},
	"cli.no_active":       {EN: "no active machine — run `wsld use <distro>` or `wsld init`", ZhTW: "尚無 active machine — 請執行 wsld use <distro> 或 wsld init"},
	"cli.active_not_ready": {
		EN:   "active machine %q is not initialized — set it as current again (wsld use %s) or run wsld start",
		ZhTW: "目前 machine %q 尚未 init — 請再設為目前（wsld use %s）或執行 wsld start",
	},
	"cli.no_containers":     {EN: "no containers", ZhTW: "沒有容器"},
	"cli.container_started": {EN: "started %s", ZhTW: "已啟動 %s"},
	"cli.container_stopped": {EN: "stopped %s", ZhTW: "已停止 %s"},
	"cli.doctor_summary":    {EN: "doctor: %d ok, %d warn, %d fail", ZhTW: "doctor：%d 正常、%d 警告、%d 失敗"},

	// ---- status ----
	"status.distro":        {EN: "Distro: %s", ZhTW: "發行版：%s"},
	"status.wsl":           {EN: "WSL", ZhTW: "WSL"},
	"status.systemd":       {EN: "systemd", ZhTW: "systemd"},
	"status.docker":        {EN: "docker.service", ZhTW: "docker.service"},
	"status.api":           {EN: "API", ZhTW: "API"},
	"status.found_running": {EN: "found, running (WSL%d)", ZhTW: "存在、執行中（WSL%d）"},
	"status.found_stopped": {EN: "found, stopped (WSL%d)", ZhTW: "存在、已停止（WSL%d）"},
	"status.not_found":     {EN: "not found", ZhTW: "不存在"},
	"status.active":        {EN: "active", ZhTW: "運作中"},
	"status.inactive":      {EN: "inactive", ZhTW: "未運作"},
	"status.reachable":     {EN: "reachable (%s)", ZhTW: "可連線（%s）"},
	"status.unreachable":   {EN: "unreachable (%s): %s", ZhTW: "無法連線（%s）：%s"},
	"status.ready":         {EN: "READY", ZhTW: "就緒"},
	"status.not_ready":     {EN: "NOT READY", ZhTW: "未就緒"},

	// ---- ps 表頭 ----
	"ps.header": {EN: "CONTAINER ID  NAME                 IMAGE                      STATE     STATUS", ZhTW: "CONTAINER ID  NAME                 IMAGE                      STATE     STATUS"},

	// ---- doctor 標題／提示 ----
	"doctor.wsl.title":            {EN: "wsl.exe available", ZhTW: "wsl.exe 可用"},
	"doctor.wsl.hint":             {EN: "run scripts/01-enable-wsl.ps1 as Administrator and reboot", ZhTW: "以系統管理員執行 scripts/01-enable-wsl.ps1 並重開機"},
	"doctor.wsl.detail":           {EN: "%d distro(s)", ZhTW: "%d 個發行版"},
	"doctor.skipped":              {EN: "skipped: prerequisite failed", ZhTW: "skipped: 前置檢查失敗"},
	"doctor.distro.title":         {EN: "target distro is WSL2", ZhTW: "目標發行版為 WSL2"},
	"doctor.distro.missing":       {EN: "config distro %q not installed", ZhTW: "config 指定的 %q 不存在"},
	"doctor.distro.missing_hint":  {EN: "run wsld init or fix distro in config.toml", ZhTW: "執行 wsld init 或修正 config.toml 的 distro"},
	"doctor.distro.wsl1":          {EN: "%s is WSL%d", ZhTW: "%s 是 WSL%d"},
	"doctor.wake.title":           {EN: "distro can be woken", ZhTW: "可喚醒發行版"},
	"doctor.wake.hint":            {EN: "try wsl --shutdown, then wsl -d %s", ZhTW: "wsl --shutdown 後重試；或檢查 wsl -d %s"},
	"doctor.systemd.title":        {EN: "systemd enabled", ZhTW: "systemd 已啟用"},
	"doctor.systemd.hint":         {EN: "add [boot] systemd=true to /etc/wsl.conf, then wsl --shutdown", ZhTW: "/etc/wsl.conf 加入 [boot] systemd=true，然後 wsl --shutdown"},
	"doctor.docker_service.title": {EN: "docker.service active", ZhTW: "docker.service active"},
	"doctor.docker_service.hint":  {EN: "wsld start; or wsl -d %s -u root -- systemctl start docker; if not installed: wsld init", ZhTW: "wsld start；或 wsl -d %s -u root -- systemctl start docker；未安裝則 wsld init"},
	"doctor.api.title":            {EN: "Docker API _ping", ZhTW: "Docker API _ping"},
	"doctor.api.not_docker":       {EN: "%s is listening but is not Docker: %v", ZhTW: "%s 有服務監聽但不是 Docker（not Docker）：%v"},
	"doctor.api.not_docker_hint":  {EN: "port taken by another program; re-run wsld init to pick a new port, or edit api_endpoint in config.toml", ZhTW: "埠被其他程式占用；重新執行 wsld init 讓 bootstrap 換埠，或修改 config.toml 的 api_endpoint"},
	"doctor.api.unreachable":      {EN: "%s not responding: %v", ZhTW: "%s 無回應：%v"},
	"doctor.api.unreachable_hint": {EN: "run wsld start; if it still fails, check /etc/docker/daemon.json hosts includes tcp://127.0.0.1:<port> matching config", ZhTW: "執行 wsld start；仍失敗則確認 /etc/docker/daemon.json 的 hosts 含 tcp://127.0.0.1:<port> 且與 config 一致"},
	"doctor.loopback.title":       {EN: "API bound to loopback only", ZhTW: "API 只綁 loopback"},
	"doctor.loopback.bad":         {EN: "non-loopback listeners: %s", ZhTW: "非 loopback 監聽：%s"},
	"doctor.loopback.hint":        {EN: "daemon.json hosts may only contain the unix socket and tcp://127.0.0.1:<port>; then systemctl restart docker", ZhTW: "daemon.json 的 hosts 只允許 unix socket 與 tcp://127.0.0.1:<port>；修正後 systemctl restart docker"},
	"doctor.linger.title":         {EN: "linger enabled", ZhTW: "linger 已啟用"},
	"doctor.linger.detail":        {EN: "loginctl Linger=%q (user %s)", ZhTW: "loginctl Linger=%q（使用者 %s）"},
	"doctor.linger.skipped":       {EN: "skipped: systemd not ready", ZhTW: "skipped: systemd 未就緒"},
	"doctor.desktop.title":        {EN: "Docker Desktop not running concurrently", ZhTW: "Docker Desktop 未同時運行"},
	"doctor.desktop.hint":         {EN: "avoid two engines: quit Docker Desktop (or disable its WSL integration); CLI/GUI only see the wsld-managed engine", ZhTW: "建議不要雙引擎混用：關閉 Docker Desktop（或停用其 WSL integration），CLI/GUI 只看 wsld 管理的引擎"},
	"doctor.config.title":         {EN: "config", ZhTW: "設定"},
	"doctor.config.hint":          {EN: "file: %s", ZhTW: "檔案：%s"},
	"doctor.repo.title":           {EN: "project location", ZhTW: "專案位置"},
	"doctor.repo.detail":          {EN: "keep source under WSL $HOME (e.g. ~/Projects), not C:\\ (/mnt/c)", ZhTW: "原始碼請放在 WSL 的 $HOME（例如 ~/Projects），不要放 C:\\（/mnt/c）"},
	"doctor.repo.hint":            {EN: "cross-filesystem bind mounts are slow and have different permission semantics", ZhTW: "跨檔案系統 bind mount 慢且權限行為不同"},

	// ---- lifecycle ----
	"lc.wsl_unavailable":      {EN: "wsl.exe unavailable: %v", ZhTW: "wsl.exe 不可用：%v"},
	"lc.wsl_unavailable_init": {EN: "wsl.exe unavailable (run scripts/01-enable-wsl.ps1 as Administrator first): %v", ZhTW: "wsl.exe 不可用（請先以系統管理員執行 scripts/01-enable-wsl.ps1）：%v"},
	"lc.distro_missing":       {EN: "WSL distro %q not found; run wsld init first", ZhTW: "找不到 WSL 發行版 %q；請先執行 wsld init"},
	"lc.distro_wsl1":          {EN: "distro %q is WSL%d; WSL2 required (wsl --set-version %s 2)", ZhTW: "發行版 %q 是 WSL%d，需要 WSL2（wsl --set-version %s 2）"},
	"lc.wake_failed":          {EN: "cannot wake %q: %v", ZhTW: "無法喚醒 %q：%v"},
	"lc.systemd_off":          {EN: "systemd is not enabled in %q; ensure /etc/wsl.conf has [boot] systemd=true, run wsl --shutdown and retry", ZhTW: "%q 內 systemd 未啟用；請確認 /etc/wsl.conf 有 [boot] systemd=true，並執行 wsl --shutdown 後重試"},
	"lc.docker_start_failed":  {EN: "systemctl start docker failed: %v %s", ZhTW: "systemctl start docker 失敗：%v %s"},
	"lc.docker_missing":       {EN: "docker.service not installed in %q; run wsld init", ZhTW: "%q 尚未安裝 docker.service；請執行 wsld init"},
	"lc.start_bootstrap":      {EN: "docker.service missing in %s; running bootstrap …\n", ZhTW: "%s 缺少 docker.service，執行 bootstrap …\n"},
	"lc.api_no_response":      {EN: "%s not responding: %v (check daemon.json hosts and run wsld doctor)", ZhTW: "%s 無回應：%v（檢查 daemon.json hosts 與 wsld doctor）"},
	"lc.init_distro_missing":  {EN: "distro %q does not exist (wsl -l -v)", ZhTW: "指定的發行版 %q 不存在（wsl -l -v）"},
	"lc.init_distro_wsl1":     {EN: "%q is WSL%d; WSL2 required", ZhTW: "%q 是 WSL%d，需要 WSL2"},
	"lc.init_no_ubuntu":       {EN: "no Ubuntu WSL2 distro; install one and retry (wsl --install -d %s)", ZhTW: "沒有 Ubuntu WSL2 發行版；請安裝後重試（wsl --install -d %s）"},
	"lc.init_installing":      {EN: "no Ubuntu WSL2 distro; running wsl --install -d %s --no-launch …\n", ZhTW: "沒有 Ubuntu WSL2 發行版，執行 wsl --install -d %s --no-launch …\n"},
	"lc.init_install_failed":  {EN: "wsl --install -d %s failed: %v", ZhTW: "wsl --install -d %s 失敗：%v"},
	"lc.init_relaunch":        {EN: "%s installed; open %s once from the Start menu to create your Linux user, then run wsld init again", ZhTW: "已安裝 %s；請從開始選單開啟 %s 一次建立 Linux 使用者，然後重新執行 wsld init"},
	"lc.init_user_detect": {EN: "cannot detect default user in %q: %v", ZhTW: "無法在 %q 內偵測預設使用者：%v"},
	"lc.init_user_root": {
		EN:   "%q has no normal Linux user yet (still root). Open it once with: wsl -d %s — create a user, then Start engine again.",
		ZhTW: "%q 尚無一般 Linux 使用者（仍是 root）。請先執行 wsl -d %s 建立使用者，再按 Start engine。",
	},
	"lc.init_creating_user": {EN: "no default user in %s; creating %q …\n", ZhTW: "%s 尚無預設使用者，建立 %q …\n"},
	"lc.init_user_create":   {EN: "cannot create Linux user in %q: %v", ZhTW: "無法在 %q 內建立 Linux 使用者：%v"},
	"lc.init_running":       {EN: "running bootstrap inside %s (as root) …\n", ZhTW: "在 %s 內執行 bootstrap（root）…\n"},
	"lc.init_exit3":           {EN: "bootstrap prerequisites not met (usually systemd just enabled); run wsl --shutdown, reopen %s, then wsld init again", ZhTW: "bootstrap 前置條件不符（多半是 systemd 剛啟用）；請在 Windows 執行 wsl --shutdown，重開 %s 後再跑 wsld init"},
	"lc.init_exit4":           {EN: "Docker verification failed after bootstrap; see wsl -d %s -- journalctl -u docker -n 50", ZhTW: "bootstrap 後 Docker 驗證失敗；wsl -d %s -- journalctl -u docker -n 50"},
	"lc.init_failed":          {EN: "bootstrap failed: %v", ZhTW: "bootstrap 失敗：%v"},
	"lc.init_no_port":         {EN: "bootstrap output lacks WSLD_API_PORT; cannot confirm API port", ZhTW: "bootstrap 輸出缺少 WSLD_API_PORT，無法確認 API 埠"},
	"lc.init_port_changed":    {EN: "port %d in use; using %d (saved to config)\n", ZhTW: "埠 %d 已被占用，改用 %d（已寫入 config）\n"},

	// ---- GUI（Phase 3 前端會透過 All() 取用）----
	"gui.title":               {EN: "wsld", ZhTW: "wsld"},
	"gui.start":               {EN: "Start", ZhTW: "啟動"},
	"gui.stop":                {EN: "Stop", ZhTW: "停止"},
	"gui.logs":                {EN: "Logs", ZhTW: "日誌"},
	"gui.terminal":            {EN: "Terminal", ZhTW: "終端"},
	"gui.info":                {EN: "Info", ZhTW: "資訊"},
	"gui.open_folder":         {EN: "Open folder", ZhTW: "開啟資料夾"},
	"gui.open_terminal":       {EN: "Open terminal", ZhTW: "開啟終端"},
	"gui.ensure":              {EN: "Start engine", ZhTW: "啟動引擎"},
	"gui.live":                {EN: "Live", ZhTW: "即時"},
	"gui.col.name":            {EN: "Name", ZhTW: "名稱"},
	"gui.col.image":           {EN: "Image", ZhTW: "映像"},
	"gui.col.state":           {EN: "State", ZhTW: "狀態"},
	"gui.col.status":          {EN: "Status", ZhTW: "狀態描述"},
	"gui.col.ports":           {EN: "Ports", ZhTW: "埠"},
	"gui.col.actions":         {EN: "Actions", ZhTW: "操作"},
	"gui.status.connected":    {EN: "Connected", ZhTW: "已連線"},
	"gui.status.disconnected": {EN: "Disconnected", ZhTW: "未連線"},
	"gui.status.starting":     {EN: "Starting…", ZhTW: "啟動中…"},
	"gui.status.switching":    {EN: "Switching…", ZhTW: "切換中…"},
	"gui.status.loading":      {EN: "Loading…", ZhTW: "載入中…"},
	"gui.ct.starting":         {EN: "Starting…", ZhTW: "啟動中…"},
	"gui.ct.stopping":         {EN: "Stopping…", ZhTW: "停止中…"},
	"gui.lang":                {EN: "Language", ZhTW: "語言"},
	"gui.github":              {EN: "GitHub repository", ZhTW: "GitHub 儲存庫"},
	"gui.status.paused":       {EN: "Refresh paused", ZhTW: "已暫停刷新"},
	"gui.empty":               {EN: "No containers", ZhTW: "沒有容器"},
	"gui.error.api":           {EN: "Cannot reach Docker API. Click “Start engine” or run wsld doctor.", ZhTW: "無法連線 Docker API。請按「啟動引擎」或執行 wsld doctor。"},
	"gui.tray.open":           {EN: "Open window", ZhTW: "開啟主視窗"},
	"gui.tray.quit":           {EN: "Quit", ZhTW: "結束"},
	"gui.settings":            {EN: "Settings", ZhTW: "設定"},
	"gui.settings.autostart":  {EN: "Start at login", ZhTW: "開機自啟動"},
	"gui.settings.refresh":    {EN: "Refresh interval (s)", ZhTW: "刷新間隔（秒）"},
	"gui.settings.exec_shell": {EN: "Terminal shell (empty = bash→sh)", ZhTW: "終端 shell（空 = bash→sh）"},
	"gui.save":                {EN: "Save", ZhTW: "儲存"},
	"gui.cancel":              {EN: "Cancel", ZhTW: "取消"},
	"gui.confirm":             {EN: "Confirm", ZhTW: "確認"},
	"gui.remove":              {EN: "Remove", ZhTW: "移除"},
	"gui.confirm.remove_ct": {
		EN:   "Remove container %s? If it is running it will be stopped first.",
		ZhTW: "要移除容器 %s 嗎？若正在執行會先停止。",
	},
	"gui.info.labels":           {EN: "Labels", ZhTW: "標籤"},
	"gui.info.networks":         {EN: "Networks", ZhTW: "網路"},
	"gui.info.mounts":           {EN: "Mounts", ZhTW: "掛載"},
	"gui.info.command":          {EN: "Command", ZhTW: "命令"},
	"gui.info.created":          {EN: "Created", ZhTW: "建立時間"},
	"gui.info.started":          {EN: "Started", ZhTW: "啟動時間"},
	"gui.info.restart":          {EN: "Restart policy", ZhTW: "重啟策略"},
	"gui.info.name":             {EN: "Name", ZhTW: "名稱"},
	"gui.prune":                 {EN: "Prune unused", ZhTW: "清除未使用"},
	"gui.in_use":                {EN: "in use", ZhTW: "使用中"},
	"gui.nav.containers":        {EN: "Containers", ZhTW: "容器"},
	"gui.nav.images":            {EN: "Images", ZhTW: "映像"},
	"gui.nav.volumes":           {EN: "Volumes", ZhTW: "儲存卷"},
	"gui.nav.networks":          {EN: "Networks", ZhTW: "網路"},
	"gui.nav.machines":          {EN: "Machines", ZhTW: "機器"},
	"gui.nav.settings":          {EN: "Settings", ZhTW: "設定"},
	"gui.settings.theme":        {EN: "Theme", ZhTW: "主題"},
	"gui.settings.theme.light":  {EN: "Light", ZhTW: "淺色"},
	"gui.settings.theme.dark":   {EN: "Dark", ZhTW: "深色"},
	"gui.settings.theme.system": {EN: "System", ZhTW: "跟隨系統"},
	"gui.machines.wslconfig":    {EN: ".wslconfig", ZhTW: ".wslconfig"},
	"gui.machines.usage":        {EN: "Usage", ZhTW: "用量"},
	"gui.machines.cpu":          {EN: "CPU", ZhTW: "CPU"},
	"gui.machines.mem":          {EN: "Memory", ZhTW: "記憶體"},
	"gui.machines.endpoint":     {EN: "API endpoint", ZhTW: "API 端點"},
	"gui.machines.user":         {EN: "User", ZhTW: "使用者"},
	"gui.col.id":                {EN: "ID", ZhTW: "ID"},
	"gui.col.size":              {EN: "Size", ZhTW: "大小"},
	"gui.col.created":           {EN: "Created", ZhTW: "建立時間"},
	"gui.col.in_use":            {EN: "In use", ZhTW: "使用中"},
	"gui.col.driver":            {EN: "Driver", ZhTW: "驅動"},
	"gui.col.mountpoint":        {EN: "Mountpoint", ZhTW: "掛載點"},
	"gui.col.scope":             {EN: "Scope", ZhTW: "範圍"},
	"gui.col.version":           {EN: "WSL", ZhTW: "WSL"},
	"gui.col.flags":             {EN: "Role", ZhTW: "角色"},
	"gui.empty.images":          {EN: "No images", ZhTW: "沒有映像"},
	"gui.empty.volumes":         {EN: "No volumes", ZhTW: "沒有儲存卷"},
	"gui.empty.networks":        {EN: "No networks", ZhTW: "沒有網路"},
	"gui.empty.machines":        {EN: "No WSL distributions", ZhTW: "沒有 WSL 發行版"},
	"gui.confirm.remove":        {EN: "Remove %s?", ZhTW: "要移除 %s 嗎？"},
	"gui.confirm.prune_images":  {EN: "Remove all images not used by any container?", ZhTW: "要移除所有未被容器使用的映像嗎？"},
	"gui.confirm.prune_volumes": {EN: "Remove all volumes not used by any container? Data in them will be lost.", ZhTW: "要移除所有未被容器使用的儲存卷嗎？其中的資料將會遺失。"},
	"gui.pruned":                {EN: "Removed %d item(s), reclaimed %s", ZhTW: "已移除 %d 項，回收 %s"},
	"gui.removed":               {EN: "Removed %s", ZhTW: "已移除 %s"},
	"gui.machines.unavailable":  {EN: "wsl.exe is not available in this environment.", ZhTW: "此環境沒有 wsl.exe。"},
	"gui.machines.add":          {EN: "Add distro", ZhTW: "新增發行版"},
	"gui.machines.select":       {EN: "Distribution", ZhTW: "發行版"},
	"gui.machines.select_placeholder": {EN: "Please select a distro", ZhTW: "請選擇發行版"},
	"gui.machines.name":         {EN: "Custom name", ZhTW: "自訂名稱"},
	"gui.machines.install":      {EN: "Install", ZhTW: "安裝"},
	"gui.machines.installing":   {EN: "Installing…", ZhTW: "安裝中…"},
	"gui.machines.installed":    {EN: "%s installed. Launch it once (wsl -d %s) to create its default user, then set it as current (init + start run automatically).", ZhTW: "%s 已安裝。請先執行一次 wsl -d %s 建立預設使用者，再設為目前（會自動 init + start）。"},
	"gui.machines.loading":      {EN: "Loading available distributions…", ZhTW: "載入可安裝的發行版…"},
	"gui.machines.config":       {EN: "Settings", ZhTW: "設定"},
	"gui.machines.systemd_on":   {EN: "systemd enabled", ZhTW: "systemd 已啟用"},
	"gui.machines.systemd_off":  {EN: "systemd disabled", ZhTW: "systemd 未啟用"},
	"gui.machines.no_conf":      {EN: "(no /etc/wsl.conf)", ZhTW: "（沒有 /etc/wsl.conf）"},
	"gui.machines.default":      {EN: "default", ZhTW: "預設"},
	"gui.machines.active":       {EN: "active", ZhTW: "目前"},
	"gui.machines.use":          {EN: "Set as current", ZhTW: "設為目前"},
	"gui.machines.used":         {EN: "Now using %s", ZhTW: "已切換至 %s"},
	"gui.machines.unmanage":     {EN: "Unmanage", ZhTW: "取消管理"},
	"gui.machines.remove":       {EN: "Remove distro", ZhTW: "刪除發行版"},
	"gui.machines.removing":     {EN: "Removing…", ZhTW: "刪除中…"},
	"gui.machines.unregister":   {EN: "Unregister", ZhTW: "解除註冊"},
	"gui.machines.unregister_prompt": {
		EN:   "Type the distro name to confirm Remove distro (permanent):",
		ZhTW: "輸入發行版名稱以確認刪除發行版（永久）：",
	},
	"gui.machines.folder":   {EN: "Open folder", ZhTW: "開啟資料夾"},
	"gui.machines.terminal": {EN: "Open terminal", ZhTW: "開啟終端"},
	"gui.machines.not_ready": {
		EN:   "Active machine is not ready yet — set it as current again to finish init, or click Start engine.",
		ZhTW: "目前 machine 尚未就緒 — 請再設為目前以完成 init，或按 Start engine。",
	},
	"gui.machines.info": {EN: "Info", ZhTW: "資訊"},
	"gui.close":         {EN: "Close", ZhTW: "關閉"},
}
