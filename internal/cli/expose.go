package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/expose"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/wsl"
)

func (a *App) exposeManager(cfg config.Config) (*expose.Manager, bool) {
	e, ok := cfg.EngineMachine()
	if !ok {
		return nil, false
	}
	return &expose.Manager{WSL: a.WSL, Engine: a.apiFor(cfg), EngineDistro: e.Distro}, true
}

func containerNames(u []expose.Using, onlyRunning bool) []string {
	var out []string
	for _, c := range u {
		if onlyRunning && c.State != "running" {
			continue
		}
		out = append(out, c.Name)
	}
	return out
}

// cmdExpose：wsld expose status|release <distro> [--stop-containers]|gc。
// 曝露＝client 的根目錄掛在 /mnt/wsl/distro/<名稱>，讓共用 engine 以相同路徑存取（bind mount 用）。
func (a *App) cmdExpose(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.expose.usage")))
	}
	sub := args[0]
	// --stop-containers 可放在 distro 之後（標準 flag 套件遇到位置參數就停止解析，所以先自己挑出來）
	stopFlag := false
	var rest []string
	for _, x := range args[1:] {
		if x == "--stop-containers" || x == "-stop-containers" {
			stopFlag = true
			continue
		}
		rest = append(rest, x)
	}
	fs := a.newFlags("expose " + sub)
	if err := parse(fs, rest); err != nil {
		return err
	}
	stop := &stopFlag
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	m, ok := a.exposeManager(cfg)
	if !ok {
		return fail(ExitNotReady, errors.New(i18n.T("cli.expose.no_engine")))
	}
	list, err := wsl.ListDistros(ctx, a.WSL)
	if err != nil {
		return fail(ExitNotReady, err)
	}
	switch sub {
	case "status":
		rep, err := m.Report(ctx, list)
		if err != nil {
			return fail(ExitError, err)
		}
		if len(rep) == 0 {
			fmt.Fprintln(a.Stdout, i18n.T("cli.expose.none"))
			return nil
		}
		for _, e := range rep {
			state := "exposed"
			if !e.Mounted {
				state = "not mounted"
			}
			fmt.Fprintf(a.Stdout, "%-20s %s  %s\n", e.Distro, state, expose.MountPath(e.Distro))
			if e.Stale {
				fmt.Fprintln(a.Stdout, "  "+i18n.T("cli.expose.stale"))
			}
			for _, c := range e.Containers {
				fmt.Fprintf(a.Stdout, "  %s (%s)\n", c.Name, c.State)
			}
		}
		return nil
	case "release":
		if fs.NArg() != 1 {
			return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "distro")))
		}
		name := fs.Arg(0)
		if *stop {
			if _, err := m.StopUsers(ctx, name); err != nil {
				return fail(ExitError, err)
			}
		}
		if err := m.Release(ctx, name, *stop); err != nil {
			var inUse *expose.InUseError
			if errors.As(err, &inUse) {
				return fail(ExitError, errors.New(i18n.T("cli.expose.in_use", name, strings.Join(containerNames(inUse.Containers, false), ", "), name)))
			}
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.expose.released", name))
		return nil
	case "gc":
		released, err := m.GC(ctx, list)
		if err != nil {
			fmt.Fprintln(a.Stderr, i18n.T("cli.error", err))
		}
		if len(released) == 0 {
			fmt.Fprintln(a.Stdout, i18n.T("cli.expose.gc_none"))
		} else {
			fmt.Fprintln(a.Stdout, i18n.T("cli.expose.gc_done", strings.Join(released, ", ")))
		}
		if err != nil {
			return fail(ExitError, errors.New(i18n.T("cli.expose.gc_failed")))
		}
		return nil
	}
	return fail(ExitUsage, errors.New(i18n.T("cli.expose.usage")))
}

// releaseBefore 在刪除／壓縮／調整大小一個 distro 之前放掉它的曝露。
// 仍有「執行中」的容器在用就拒絕（請先 wsld expose release <distro> --stop-containers）；
// 沒設定 engine、Docker API 不通或根本沒曝露時什麼都不做。
func (a *App) releaseBefore(ctx context.Context, name string) error {
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return nil
	}
	m, ok := a.exposeManager(cfg)
	if !ok || a.apiFor(cfg) == nil || a.apiFor(cfg).Ping(ctx) != nil {
		return nil
	}
	mounted, err := m.Mounted(ctx)
	if err != nil {
		return nil
	}
	found := false
	for _, n := range mounted {
		if n == name {
			found = true
		}
	}
	if !found {
		return nil
	}
	users, err := m.Users(ctx)
	if err != nil {
		return fail(ExitError, err)
	}
	if running := containerNames(users[name], true); len(running) > 0 {
		return fail(ExitError, errors.New(i18n.T("cli.expose.in_use", name, strings.Join(running, ", "), name)))
	}
	if err := m.Release(ctx, name, true); err != nil {
		return fail(ExitError, err)
	}
	return nil
}
