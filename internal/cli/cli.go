// Package cli 實作 wsld 命令列（SPEC §5.4），所有依賴可注入以便測試。
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/doctor"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/lifecycle"
	"github.com/iridance/wsld/internal/wsl"
)

// 退出碼（SPEC §5.4）。
const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitNotReady = 3
	ExitAPI      = 4
)

// API 是 CLI 需要的 Docker API 子集（dockerapi.Client 滿足）。
type API interface {
	Ping(ctx context.Context) error
	ListContainers(ctx context.Context, all bool) ([]dockerapi.Container, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeoutSec int) error
	Logs(ctx context.Context, id string, opts dockerapi.LogsOptions) (io.ReadCloser, error)
	Inspect(ctx context.Context, id string) (dockerapi.ContainerDetail, error)
}

// App 聚合依賴。
type App struct {
	Stdout, Stderr io.Writer
	// Stdin 供 init --password-stdin 讀取新使用者密碼。
	Stdin io.Reader
	// ReadPassword 以隱藏輸入詢問密碼（nil 則不詢問；非終端機回 ErrNoTerminal）。
	ReadPassword func(prompt string) (string, error)
	ConfigPath   string
	WSL          wsl.Runner
	NewAPI       func(cfg config.Config) API
	Processes    func(ctx context.Context) ([]string, error)
	Dial         func(ctx context.Context, addr string) error
	Bundle       []byte
	// K8sBundle 是 scripts.K8sBundle()（wsld k8s）。
	K8sBundle []byte
	// Exec 以繼承的 stdio 執行互動式程式（wsld exec 用）。
	Exec    func(ctx context.Context, name string, args ...string) error
	Version string
	Lang    i18n.Lang // 空則沿用目前設定
	// LAN 管理區網存取（wsld lan）；nil 表示不支援。
	LAN *lan.Manager
	// PingInterval 覆寫 lifecycle 重試間隔（測試用；0 = 預設）。
	PingInterval time.Duration
	// SelfInWSL：wsld.exe 位於 WSL 檔案系統（\\wsl.localhost\…）；此時拒絕 wsl --shutdown。
	SelfInWSL bool
	// DiskSize 回傳 distro 的 vhdx 在 Windows 上的檔案大小（wsl.VHDXSize；nil = 不報告釋放量）。
	DiskSize func(name string) (int64, error)
}

type exitErr struct {
	code int
	err  error
}

func (e *exitErr) Error() string { return e.err.Error() }
func (e *exitErr) Unwrap() error { return e.err }

func fail(code int, err error) error { return &exitErr{code: code, err: err} }

// Run 執行命令並回傳退出碼。
func (a *App) Run(ctx context.Context, args []string) int {
	if a.Lang != "" {
		i18n.Set(a.Lang)
	}
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, i18n.T("cli.usage", a.ConfigPath))
		return ExitUsage
	}
	cmd, rest := args[0], args[1:]
	var err error
	if needsEngine(cmd) && !wantsHelp(rest) {
		// 啟動即帶起：需要引擎的指令自動建立（首次）、喚醒並等到 API 就緒，不再有獨立的 start。
		if err := a.ensureEngine(ctx); err != nil {
			return a.report(err)
		}
	}
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprintln(a.Stdout, i18n.T("cli.usage", a.ConfigPath))
	case "version", "--version":
		fmt.Fprintf(a.Stdout, "wsld %s\n", a.Version)
	case "init":
		err = a.cmdInit(ctx, rest)
	case "status":
		err = a.cmdStatus(ctx, rest)
	case "ps":
		err = a.cmdPs(ctx, rest)
	case "logs":
		err = a.cmdLogs(ctx, rest)
	case "start-ct":
		err = a.cmdStartCt(ctx, rest)
	case "stop-ct":
		err = a.cmdStopCt(ctx, rest)
	case "exec":
		err = a.cmdExec(ctx, rest)
	case "doctor":
		err = a.cmdDoctor(ctx, rest)
	case "unmanage":
		err = a.cmdUnmanage(ctx, rest)
	case "unregister":
		err = a.cmdUnregister(ctx, rest)
	case "resize-disk":
		err = a.cmdResizeDisk(ctx, rest)
	case "compact-disk":
		err = a.cmdCompactDisk(ctx, rest)
	case "migrate":
		err = a.cmdMigrate(ctx, rest)
	case "expose":
		err = a.cmdExpose(ctx, rest)
	case "lan":
		err = a.cmdLan(ctx, rest)
	case "k8s":
		err = a.cmdK8s(ctx, rest)
	case "kubectl":
		err = a.cmdKubectl(ctx, rest)
	default:
		err = fail(ExitUsage, errors.New(i18n.T("cli.unknown_command", cmd)))
	}
	return a.report(err)
}

func (a *App) report(err error) int {
	if err == nil {
		return ExitOK
	}
	code := classify(err)
	var ee *exitErr
	if errors.As(err, &ee) && ee.code != 0 {
		code = ee.code
	}
	msg := err.Error()
	switch code {
	case ExitAPI:
		if !strings.Contains(msg, i18n.T("cli.exit.api")) {
			msg = i18n.T("cli.exit.api") + ": " + msg
		}
	case ExitNotReady:
		if !strings.Contains(msg, i18n.T("cli.exit.not_ready")) {
			msg = i18n.T("cli.exit.not_ready") + ": " + msg
		}
	}
	if code == ExitUsage {
		fmt.Fprintln(a.Stderr, msg)
		fmt.Fprintln(a.Stderr, i18n.T("cli.usage", a.ConfigPath))
		return code
	}
	fmt.Fprintln(a.Stderr, i18n.T("cli.error", msg))
	return code
}

func classify(err error) int {
	switch {
	case errors.Is(err, lifecycle.ErrNotReady):
		return ExitNotReady
	case errors.Is(err, lifecycle.ErrAPI), errors.Is(err, dockerapi.ErrUnreachable):
		return ExitAPI
	default:
		return ExitError
	}
}

// ---- helpers ----------------------------------------------------------------

func (a *App) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fail(ExitUsage, err)
	}
	return nil
}

func (a *App) loadConfig() (config.Config, error) {
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		if config.IsNotFound(err) {
			return cfg, fail(ExitNotReady, errors.New(i18n.T("cli.config_missing", a.ConfigPath)))
		}
		return cfg, fail(ExitError, errors.New(i18n.T("cli.config_invalid", err)))
	}
	return cfg, nil
}

func (a *App) manager(cfg config.Config, api API) *lifecycle.Manager {
	return &lifecycle.Manager{
		WSL: a.WSL, Cfg: cfg, API: api, Bundle: a.Bundle, Output: a.Stdout, PingInterval: a.PingInterval,
		NewAPI: func(c config.Config) lifecycle.Pinger { return a.apiFor(c) },
	}
}

func (a *App) apiFor(cfg config.Config) API {
	if a.NewAPI != nil {
		return a.NewAPI(cfg)
	}
	return dockerapi.New(cfg.APIEndpoint, time.Duration(cfg.APITimeoutSec)*time.Second)
}

func (a *App) writeJSON(v any) error {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---- commands ---------------------------------------------------------------

func (a *App) cmdInit(ctx context.Context, args []string) error {
	fs := a.newFlags("init")
	var opts lifecycle.InitOptions
	fs.StringVar(&opts.Distro, "distro", "", "WSL distro to use (default: existing Ubuntu)")
	fs.StringVar(&opts.User, "user", "", "Linux user (default: distro default user)")
	fs.BoolVar(&opts.NoCompose, "no-compose", false, "skip docker-compose-plugin")
	fs.StringVar(&opts.Role, "role", "client", "client = docker CLI only, uses the shared engine (the engine itself is the docker-engine distro wsld creates)")
	fs.BoolVar(&opts.SkipInstall, "skip-install", false, "do not run wsl --install when no Ubuntu exists")
	fs.IntVar(&opts.VHDSizeGB, "vhd-size", wsl.DefaultVHDSizeGB, "disk limit in GB when installing Ubuntu (0 = WSL default, 1 TB)")
	pwStdin := fs.Bool("password-stdin", false, "read the password for a newly created Linux user from stdin (first line)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if opts.VHDSizeGB != 0 && !wsl.ValidVHDSizeGB(opts.VHDSizeGB) {
		return fail(ExitUsage, errors.New(i18n.T("gui.machines.vhd_invalid", wsl.MinVHDSizeGB, wsl.MaxVHDSizeGB)))
	}
	if opts.Role != config.RoleClient {
		return fail(ExitUsage, errors.New(i18n.T("lc.init_engine_dedicated", opts.Distro)))
	}
	opts.Password = a.newUserPassword(*pwStdin)
	// engine 走自己的流程：沒有就先建立 docker-engine，再把指定的 distro 加入成 client
	if err := a.ensureEngine(ctx); err != nil {
		return err
	}
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return fail(ExitError, errors.New(i18n.T("cli.config_invalid", err)))
	}
	m := a.manager(cfg, nil)
	newCfg, err := m.Init(ctx, opts)
	if err != nil {
		return err
	}
	// 驗證 API
	api := a.apiFor(newCfg)
	if err := api.Ping(ctx); err != nil {
		return fail(ExitAPI, fmt.Errorf("%s: %w", newCfg.APIEndpoint, err))
	}
	if err := config.Save(a.ConfigPath, newCfg); err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.config_saved", a.ConfigPath))
	fmt.Fprintln(a.Stdout, i18n.T("cli.init_done"))
	return nil
}

func (a *App) cmdStatus(ctx context.Context, args []string) error {
	fs := a.newFlags("status")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	st := a.manager(cfg, a.apiFor(cfg)).Status(ctx)
	if *asJSON {
		if err := a.writeJSON(st); err != nil {
			return err
		}
	} else {
		a.printStatus(st)
	}
	switch {
	case st.Ready():
		return nil
	case st.DistroFound && st.SystemdActive && st.DockerActive:
		return fail(ExitAPI, errors.New(i18n.T("cli.exit.api")))
	default:
		return fail(ExitNotReady, errors.New(i18n.T("cli.exit.not_ready")))
	}
}

func (a *App) printStatus(st lifecycle.Status) {
	w := a.Stdout
	fmt.Fprintln(w, i18n.T("status.distro", st.Distro))
	var wslLine string
	switch {
	case !st.DistroFound:
		wslLine = i18n.T("status.not_found")
	case st.DistroRunning:
		wslLine = i18n.T("status.found_running", st.DistroVersion)
	default:
		wslLine = i18n.T("status.found_stopped", st.DistroVersion)
	}
	fmt.Fprintf(w, "  %-15s %s\n", i18n.T("status.wsl"), wslLine)
	fmt.Fprintf(w, "  %-15s %s\n", i18n.T("status.systemd"), onOff(st.SystemdActive))
	fmt.Fprintf(w, "  %-15s %s\n", i18n.T("status.docker"), onOff(st.DockerActive))
	if st.APIReachable {
		fmt.Fprintf(w, "  %-15s %s\n", i18n.T("status.api"), i18n.T("status.reachable", st.APIEndpoint))
	} else {
		fmt.Fprintf(w, "  %-15s %s\n", i18n.T("status.api"), i18n.T("status.unreachable", st.APIEndpoint, st.APIError))
	}
	if st.Ready() {
		fmt.Fprintln(w, i18n.T("status.ready"))
	} else {
		fmt.Fprintln(w, i18n.T("status.not_ready"))
	}
}

func onOff(b bool) string {
	if b {
		return i18n.T("status.active")
	}
	return i18n.T("status.inactive")
}

func (a *App) cmdPs(ctx context.Context, args []string) error {
	fs := a.newFlags("ps")
	all := fs.Bool("a", false, "include stopped containers")
	fs.BoolVar(all, "all", false, "include stopped containers")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if err := a.requireActiveReady(cfg); err != nil {
		return err
	}
	api := a.apiFor(cfg)
	list, err := api.ListContainers(ctx, *all)
	if err != nil {
		return err
	}
	if *asJSON {
		if list == nil {
			list = []dockerapi.Container{}
		}
		return a.writeJSON(list)
	}
	if len(list) == 0 {
		fmt.Fprintln(a.Stdout, i18n.T("cli.no_containers"))
		return nil
	}
	fmt.Fprintln(a.Stdout, i18n.T("ps.header"))
	for _, c := range list {
		fmt.Fprintf(a.Stdout, "%-13s %-20s %-26s %-9s %s\n", c.ShortID(), trunc(c.Name(), 20), trunc(c.Image, 26), c.State, c.Status)
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (a *App) cmdLogs(ctx context.Context, args []string) error {
	fs := a.newFlags("logs")
	tail := fs.Int("tail", 100, "lines from the end (0 = all)")
	follow := fs.Bool("follow", false, "stream new output")
	fs.BoolVar(follow, "f", false, "stream new output")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "<container>")))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	rc, err := a.apiFor(cfg).Logs(ctx, fs.Arg(0), dockerapi.LogsOptions{Tail: *tail, Follow: *follow})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(a.Stdout, rc)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (a *App) cmdStartCt(ctx context.Context, args []string) error {
	fs := a.newFlags("start-ct")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "<container>")))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	api := a.apiFor(cfg)
	for _, id := range fs.Args() {
		if err := api.StartContainer(ctx, id); err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.container_started", id))
	}
	return nil
}

func (a *App) cmdStopCt(ctx context.Context, args []string) error {
	fs := a.newFlags("stop-ct")
	timeout := fs.Int("time", 10, "seconds to wait before killing")
	fs.IntVar(timeout, "t", 10, "seconds to wait before killing")
	// 允許 id 在旗標之前：先抽出非旗標參數
	var pos, flags []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if (args[i] == "--time" || args[i] == "-t") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, args[i])
	}
	if err := parse(fs, flags); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "<container>")))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	api := a.apiFor(cfg)
	for _, id := range pos {
		if err := api.StopContainer(ctx, id, *timeout); err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.container_stopped", id))
	}
	return nil
}

// ExecShellCommand 回傳容器內要執行的命令：config 覆寫 → 否則 bash→sh 自動退回。
func ExecShellCommand(cfg config.Config, explicit []string) []string {
	return cfg.ExecCommand(explicit)
}

func (a *App) cmdExec(ctx context.Context, args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "<container> [command...]")))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	id := args[0]
	cmd := ExecShellCommand(cfg, args[1:])
	wslArgs := append([]string{"-d", cfg.Distro, "--", "docker", "exec", "-it", id}, cmd...)
	if a.Exec == nil {
		return errors.New("exec not supported in this build")
	}
	if err := a.Exec(ctx, "wsl.exe", wslArgs...); err != nil {
		var ee *wsl.ExitError
		if errors.As(err, &ee) {
			return fail(ExitError, err)
		}
		return err
	}
	return nil
}

func (a *App) cmdDoctor(ctx context.Context, args []string) error {
	fs := a.newFlags("doctor")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	rep := doctor.Run(ctx, doctor.Deps{
		WSL:       a.WSL,
		Cfg:       cfg,
		API:       a.apiFor(cfg),
		Processes: a.Processes,
		Dial:      a.Dial,
		LAN:       a.lanLegacyFunc(),
	})
	if *asJSON {
		type item struct {
			ID, Title, Level, Detail, Hint string
		}
		items := make([]item, 0, len(rep.Results))
		for _, r := range rep.Results {
			items = append(items, item{r.ID, r.Title, r.Level.String(), r.Detail, r.Hint})
		}
		if err := a.writeJSON(map[string]any{"results": items, "exit_code": rep.ExitCode()}); err != nil {
			return err
		}
	} else {
		for _, r := range rep.Results {
			fmt.Fprintf(a.Stdout, "%-6s %s", "["+r.Level.String()+"]", r.Title)
			if r.Detail != "" {
				fmt.Fprintf(a.Stdout, " — %s", r.Detail)
			}
			fmt.Fprintln(a.Stdout)
			if r.Hint != "" && r.Level != doctor.OK {
				fmt.Fprintf(a.Stdout, "        ↳ %s\n", r.Hint)
			}
		}
		ok, warn, failN := rep.Counts()
		fmt.Fprintln(a.Stdout, i18n.T("cli.doctor_summary", ok, warn, failN))
	}
	switch rep.ExitCode() {
	case 0:
		return nil
	case ExitAPI:
		return fail(ExitAPI, errors.New(i18n.T("cli.exit.api")))
	default:
		return fail(ExitNotReady, errors.New(i18n.T("cli.exit.not_ready")))
	}
}

func (a *App) requireActiveReady(cfg config.Config) error {
	m, err := a.requireActive(cfg)
	if err != nil {
		return err
	}
	if !m.Ready() {
		return fail(ExitNotReady, errors.New(i18n.T("cli.active_not_ready", m.Distro, m.Distro)))
	}
	return nil
}

func (a *App) requireActive(cfg config.Config) (config.Machine, error) {
	m, ok := cfg.ActiveMachine()
	if !ok {
		return config.Machine{}, fail(ExitNotReady, errors.New(i18n.T("cli.no_active")))
	}
	return m, nil
}

// needsEngine：這些指令一定要用到 Docker 引擎。status／doctor 是診斷、init／unmanage 等是設定，不自動帶起。
func needsEngine(cmd string) bool {
	switch cmd {
	case "ps", "logs", "start-ct", "stop-ct", "exec", "migrate", "expose":
		return true
	}
	return false
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// ensureEngine：沒有 engine 就自動建立 docker-engine（首次）；engine 已就緒且 API 通就什麼都不做；否則 init／喚醒到 API 就緒。
func (a *App) ensureEngine(ctx context.Context) error {
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		if !config.IsNotFound(err) {
			return fail(ExitError, errors.New(i18n.T("cli.config_invalid", err)))
		}
		cfg = config.Default()
	}
	e, has := cfg.EngineMachine()
	if has && e.Ready() {
		if api := a.apiFor(cfg); api != nil && api.Ping(ctx) == nil {
			return nil
		}
	}
	if !has || !e.Ready() {
		m := a.manager(cfg, a.apiFor(cfg))
		cfg, err = m.EnsureEngine(ctx, lifecycle.InitOptions{Password: a.newUserPassword(false)})
		if err != nil {
			return err
		}
	}
	if _, cfg, err = a.bringUp(ctx, cfg); err != nil {
		return err
	}
	return nil
}

// bringUp：必要時 Init（含 API 不通時重選埠），再 Start；成功後寫回設定。
func (a *App) bringUp(ctx context.Context, cfg config.Config) (lifecycle.Status, config.Config, error) {
	m := a.manager(cfg, a.apiFor(cfg))
	am, ok := cfg.ActiveMachine()
	if !ok {
		return lifecycle.Status{}, cfg, fail(ExitNotReady, errors.New(i18n.T("cli.no_active")))
	}
	// 重跑 Init（bootstrap）會重啟 dockerd：只在還沒 init，或 docker 已經在跑卻一直連不上（埠被占／endpoint 錯）時才做。
	// 引擎只是停著（冷啟動）時，Start 喚醒並等 API 就夠了。
	reinit := func() error {
		fmt.Fprint(a.Stdout, i18n.T("lc.start_bootstrap", am.Distro))
		newCfg, err := m.Init(ctx, lifecycle.InitOptions{Distro: am.Distro, Role: am.Role, Password: a.newUserPassword(false)})
		if err != nil {
			return err
		}
		cfg = newCfg
		m.Cfg = cfg
		if m.NewAPI != nil {
			m.API = m.NewAPI(cfg)
		}
		return nil
	}
	if !am.Ready() {
		if err := reinit(); err != nil {
			return lifecycle.Status{}, cfg, err
		}
	}
	st, err := m.Start(ctx)
	if errors.Is(err, lifecycle.ErrAPI) {
		if rerr := reinit(); rerr != nil {
			return st, cfg, rerr
		}
		st, err = m.Start(ctx)
	}
	if err != nil {
		return st, cfg, err
	}
	cfg = m.Cfg
	if err := config.Save(a.ConfigPath, cfg); err != nil {
		return st, cfg, fail(ExitError, err)
	}
	return st, cfg, nil
}

func (a *App) cmdUnmanage(ctx context.Context, args []string) error {
	fs := a.newFlags("unmanage")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "distro")))
	}
	name := fs.Arg(0)
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	m, ok := cfg.FindMachine(name)
	if !ok {
		return fail(ExitError, errors.New(i18n.T("cli.machine_missing", name)))
	}
	if m.IsEngine() {
		return fail(ExitError, errors.New(i18n.T("gui.machines.engine_protected", m.Distro)))
	}
	_ = wsl.StopDocker(ctx, a.WSL, name) // best-effort：distro 未醒／無 docker 仍刪 profile
	if !cfg.RemoveMachine(name) {
		return fail(ExitError, errors.New(i18n.T("cli.machine_missing", name)))
	}
	if err := config.Save(a.ConfigPath, cfg); err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.unmanage_done", name))
	return nil
}

func (a *App) cmdUnregister(ctx context.Context, args []string) error {
	name, confirm, err := parseUnregisterArgs(args)
	if err != nil {
		return err
	}
	if name == "" {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "distro")))
	}
	if confirm == "" || confirm != name {
		return fail(ExitUsage, errors.New(i18n.T("cli.unregister_confirm", name)))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if m, ok := cfg.FindMachine(name); ok && m.IsEngine() {
		return fail(ExitError, errors.New(i18n.T("gui.machines.engine_protected", m.Distro)))
	}
	if err := a.releaseBefore(ctx, name); err != nil {
		return err
	}
	_ = wsl.StopDocker(ctx, a.WSL, name)
	if err := wsl.Unregister(ctx, a.WSL, name); err != nil {
		return fail(ExitError, err)
	}
	cfg.RemoveMachine(name)
	if err := config.Save(a.ConfigPath, cfg); err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.unregister_done", name))
	return nil
}

func parseUnregisterArgs(args []string) (name, confirm string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--confirm":
			if i+1 >= len(args) {
				return "", "", fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "--confirm <name>")))
			}
			confirm = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			return "", "", fail(ExitUsage, fmt.Errorf("unknown flag %s", a))
		default:
			if name != "" {
				return "", "", fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "distro")))
			}
			name = a
		}
	}
	return name, confirm, nil
}

// parseDiskArgs 解析 [--shutdown] 與位置參數（旗標可放前後）。
func parseDiskArgs(args []string, want int, usage string) (pos []string, shutdown bool, err error) {
	for _, x := range args {
		switch {
		case x == "--shutdown":
			shutdown = true
		case strings.HasPrefix(x, "-"):
			return nil, false, fail(ExitUsage, fmt.Errorf("unknown flag %s", x))
		default:
			pos = append(pos, x)
		}
	}
	if len(pos) != want {
		return nil, false, fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", usage)))
	}
	if !wsl.ValidDistroName(pos[0]) {
		return nil, false, fail(ExitUsage, fmt.Errorf("invalid distro name %q", pos[0]))
	}
	return pos, shutdown, nil
}

// diskOp 以 wsl.WithFreeVHD 執行 op（VHD 未被占用時直接做；否則需 --shutdown：停 docker → 關 WSL → op → 重啟）。
// needsShutdown 產生未帶 --shutdown 時的說明（參數為會被停止的機器）。
func (a *App) diskOp(ctx context.Context, name string, shutdown bool, needsShutdown func(running string) string, op func(context.Context) error) error {
	list, err := wsl.ListDistros(ctx, a.WSL)
	if err != nil {
		return fail(ExitNotReady, err)
	}
	if _, ok := wsl.FindDistro(list, name); !ok {
		return fail(ExitNotReady, errors.New(i18n.T("lc.init_distro_missing", name)))
	}
	primary := ""
	if cfg, err := config.Load(a.ConfigPath); err == nil {
		primary = cfg.Active
	}
	// 對已停止的 distro 做壓縮／調整大小之前，先放掉它殘留的曝露（避免同一份 VHD 被接兩次）
	if d, ok := wsl.FindDistro(list, name); ok && !strings.EqualFold(d.State, "Running") {
		if err := a.releaseBefore(ctx, name); err != nil {
			return err
		}
	}
	_, err = wsl.WithFreeVHD(ctx, a.WSL, name, wsl.FreeVHDOptions{
		Consent: shutdown, SelfInWSL: a.SelfInWSL, Primary: primary,
		BeforeShutdown: func(running []string) {
			fmt.Fprintln(a.Stdout, i18n.T("cli.resize_shutting_down", joinOrDash(running)))
			for _, x := range running {
				_ = wsl.StopDocker(ctx, a.WSL, x)
			}
		},
	}, op)
	var ns *wsl.NeedsShutdownError
	var re *wsl.RestartError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ns):
		return fail(ExitNotReady, errors.New(needsShutdown(joinOrDash(ns.Running))))
	case errors.Is(err, wsl.ErrSelfInWSL):
		return fail(ExitUsage, errors.New(i18n.T("cli.resize_self_in_wsl")))
	case errors.As(err, &re):
		return fail(ExitError, fmt.Errorf("%s: %w", i18n.T("gui.machines.resize_restart_failed", re.Name), re.Err))
	}
	return err
}

func joinOrDash(xs []string) string {
	if len(xs) == 0 {
		return "-"
	}
	return strings.Join(xs, ", ")
}

// diskSize 回傳 vhdx 在 Windows 上的檔案大小（取不到為 -1）。
func (a *App) diskSize(name string) int64 {
	if a.DiskSize == nil {
		return -1
	}
	n, err := a.DiskSize(name)
	if err != nil {
		return -1
	}
	return n
}

// cmdResizeDisk：wsld resize-disk [--shutdown] <distro> <GB>，調整虛擬磁碟容量上限（wsl --manage --resize），
// 成功後在同一個空檔壓縮 VHDX（縮小會讓 Windows 上的檔案變大）。
func (a *App) cmdResizeDisk(ctx context.Context, args []string) error {
	pos, shutdown, err := parseDiskArgs(args, 2, "<distro> <GB>")
	if err != nil {
		return err
	}
	name := pos[0]
	size, err := strconv.Atoi(pos[1])
	if err != nil || !wsl.ValidVHDSizeGB(size) {
		return fail(ExitUsage, errors.New(i18n.T("gui.machines.vhd_invalid", wsl.MinVHDSizeGB, wsl.MaxVHDSizeGB)))
	}
	// 執行中先估計能縮到多小：放不下就不必關閉 WSL
	if list, err := wsl.ListDistros(ctx, a.WSL); err == nil {
		if d, ok := wsl.FindDistro(list, name); ok && strings.EqualFold(d.State, "Running") {
			if min, err := wsl.MinDiskGB(ctx, a.WSL, d.Name); err == nil && size < min {
				return fail(ExitError, errors.New(i18n.T("gui.machines.resize_too_small", d.Name, size, min)))
			}
		}
	}
	before := a.diskSize(name)
	compacted := false
	err = a.diskOp(ctx, name, shutdown,
		func(running string) string { return i18n.T("cli.resize_needs_shutdown", name, running, name, size) },
		func(ctx context.Context) error {
			if err := wsl.ResizeDisk(ctx, a.WSL, name, size); err != nil {
				return err
			}
			compacted = wsl.CompactDisk(ctx, a.WSL, name) == nil // best-effort
			return nil
		})
	var ts *wsl.TooSmallError
	if errors.As(err, &ts) {
		return fail(ExitError, errors.New(i18n.T("gui.machines.resize_too_small", name, size, ts.MinGB)))
	}
	if err != nil {
		var ee *exitErr
		if errors.As(err, &ee) {
			return err
		}
		return fail(ExitError, err)
	}
	if after := a.diskSize(name); compacted && before >= 0 && after >= 0 && before > after {
		fmt.Fprintln(a.Stdout, i18n.T("gui.machines.resized_freed", name, strconv.Itoa(size), humanBytes(before-after)))
	} else {
		fmt.Fprintln(a.Stdout, i18n.T("gui.machines.resized", name, strconv.Itoa(size)))
	}
	return nil
}

// cmdCompactDisk：wsld compact-disk [--shutdown] <distro>，壓縮 VHDX 把不再使用的空間還給 Windows。
func (a *App) cmdCompactDisk(ctx context.Context, args []string) error {
	pos, shutdown, err := parseDiskArgs(args, 1, "<distro>")
	if err != nil {
		return err
	}
	name := pos[0]
	before := a.diskSize(name)
	err = a.diskOp(ctx, name, shutdown,
		func(running string) string { return i18n.T("cli.compact_needs_shutdown", name, running, name) },
		func(ctx context.Context) error { return wsl.CompactDisk(ctx, a.WSL, name) })
	if err != nil {
		var ee *exitErr
		if errors.As(err, &ee) {
			return err
		}
		return fail(ExitError, err)
	}
	after := a.diskSize(name)
	switch {
	case before >= 0 && after >= 0 && before > after:
		fmt.Fprintln(a.Stdout, i18n.T("gui.machines.compacted", name, humanBytes(before-after), humanBytes(after)))
	case after >= 0:
		fmt.Fprintln(a.Stdout, i18n.T("gui.machines.compact_nothing", name, humanBytes(after)))
	default:
		fmt.Fprintln(a.Stdout, i18n.T("cli.compact_done", name))
	}
	return nil
}

// humanBytes 與 GUI 的 humanSize 相同（1024 進位、一位小數）。
func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, u := float64(n), 0
	for v >= 1024 && u < len(units)-1 {
		v /= 1024
		u++
	}
	if u == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

// DefaultExec 以繼承 stdio 執行外部程式（真實環境用）。
func DefaultExec(ctx context.Context, name string, args ...string) error {
	return runInherit(ctx, name, args...)
}
