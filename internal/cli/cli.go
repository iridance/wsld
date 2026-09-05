// Package cli 實作 wsld 命令列（SPEC §5.4），所有依賴可注入以便測試。
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/irice/wsl-docker/internal/config"
	"github.com/irice/wsl-docker/internal/dockerapi"
	"github.com/irice/wsl-docker/internal/doctor"
	"github.com/irice/wsl-docker/internal/i18n"
	"github.com/irice/wsl-docker/internal/lifecycle"
	"github.com/irice/wsl-docker/internal/wsl"
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
	ConfigPath     string
	WSL            wsl.Runner
	NewAPI         func(cfg config.Config) API
	Processes      func(ctx context.Context) ([]string, error)
	Dial           func(ctx context.Context, addr string) error
	Bundle         []byte
	// Exec 以繼承的 stdio 執行互動式程式（wsld exec 用）。
	Exec    func(ctx context.Context, name string, args ...string) error
	Version string
	Lang    i18n.Lang // 空則沿用目前設定
	// PingInterval 覆寫 lifecycle 重試間隔（測試用；0 = 預設）。
	PingInterval time.Duration
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
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprintln(a.Stdout, i18n.T("cli.usage", a.ConfigPath))
	case "version", "--version":
		fmt.Fprintf(a.Stdout, "wsld %s\n", a.Version)
	case "init":
		err = a.cmdInit(ctx, rest)
	case "start":
		err = a.cmdStart(ctx, rest)
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
	return &lifecycle.Manager{WSL: a.WSL, Cfg: cfg, API: api, Bundle: a.Bundle, Output: a.Stdout, PingInterval: a.PingInterval}
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
	fs.IntVar(&opts.Port, "port", 0, "starting API port (default 23750)")
	fs.BoolVar(&opts.NoCompose, "no-compose", false, "skip docker-compose-plugin")
	fs.BoolVar(&opts.SkipInstall, "skip-install", false, "do not run wsl --install when no Ubuntu exists")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		cfg = config.Default()
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

func (a *App) cmdStart(ctx context.Context, args []string) error {
	if err := parse(a.newFlags("start"), args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	m := a.manager(cfg, a.apiFor(cfg))
	st, err := m.Start(ctx)
	if err != nil {
		return err
	}
	a.printStatus(st)
	fmt.Fprintln(a.Stdout, i18n.T("cli.start_done", cfg.APIEndpoint))
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
	if len(explicit) > 0 {
		return explicit
	}
	if cfg.ExecShell != "" {
		return []string{cfg.ExecShell}
	}
	return []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
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

// DefaultExec 以繼承 stdio 執行外部程式（真實環境用）。
func DefaultExec(ctx context.Context, name string, args ...string) error {
	return runInherit(ctx, name, args...)
}
