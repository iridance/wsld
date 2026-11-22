package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/k8s"
)

const k8sSubcommands = "on|off|status|reset|kubeconfig|uninstall"

func (a *App) k8sManager(cfg config.Config, distro string) *k8s.Manager {
	return &k8s.Manager{WSL: a.WSL, Distro: distro, Bundle: a.K3sBundle, Output: a.Stdout, APIPort: cfg.K8sAPIPort}
}

func (a *App) kubeconfigPath() string {
	if a.KubeconfigPath != "" {
		return a.KubeconfigPath
	}
	return k8s.DefaultKubeconfigPath()
}

// cmdK8s：wsld k8s on|off|status|reset|kubeconfig|uninstall。
// 每台 machine 可選開啟單節點 k3s；同一時間僅允許一台（所有 WSL2 distro 共用網路命名空間）。
func (a *App) cmdK8s(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", k8sSubcommands)))
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "on", "off", "status", "reset", "kubeconfig", "uninstall":
	default:
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", k8sSubcommands)))
	}
	fs := a.newFlags("k8s " + sub)
	machine := fs.String("machine", "", "machine (distro) to act on (default: the machine with k8s on; on: the primary machine)")
	asJSON := fs.Bool("json", false, "JSON output (status)")
	yes := fs.Bool("yes", false, "do not ask for confirmation (reset, uninstall)")
	if err := parse(fs, rest); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(ExitUsage, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	switch sub {
	case "on":
		return a.k8sOn(ctx, cfg, *machine)
	case "off":
		return a.k8sOff(ctx, cfg)
	case "status":
		return a.k8sStatus(ctx, cfg, *asJSON)
	case "reset":
		return a.k8sReset(ctx, cfg, *yes)
	case "kubeconfig":
		return a.k8sKubeconfig(ctx, cfg)
	default:
		return a.k8sUninstall(ctx, cfg, *machine, *yes)
	}
}

func (a *App) k8sOn(ctx context.Context, cfg config.Config, machine string) error {
	var target config.Machine
	if machine != "" {
		m, ok := cfg.FindMachine(machine)
		if !ok {
			return fail(ExitError, errors.New(i18n.T("cli.k8s.unknown_machine", machine)))
		}
		target = m
	} else {
		m, err := a.requireActive(cfg)
		if err != nil {
			return err
		}
		target = m
	}
	if !target.Ready() {
		return fail(ExitNotReady, errors.New(i18n.T("cli.active_not_ready", target.Distro, target.Distro)))
	}
	if err := cfg.SetK8s(target.Distro, true); err != nil {
		return fail(ExitError, err)
	}
	mgr := a.k8sManager(cfg, target.Distro)
	if err := mgr.Start(ctx); err != nil {
		return fail(ExitError, err)
	}
	// 啟動成功才記錄；失敗時 config 維持原狀，避免旗標與實況不符
	if err := config.Save(a.ConfigPath, cfg); err != nil {
		return fail(ExitError, err)
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.enabled", target.Distro))
	// 不自動寫 Windows 的 ~/.kube/config；機器內的 k3s.yaml 為準，要在 Windows 用 kubectl 再手動匯出。
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.kubeconfig_hint"))
	return nil
}

// k8sExport 把 k3s kubeconfig 合併進 Windows 使用者的 ~/.kube/config（`wsld k8s kubeconfig` 或重置後同步）。
func (a *App) k8sExport(ctx context.Context, mgr *k8s.Manager, distro string) error {
	raw, err := mgr.Kubeconfig(ctx)
	if err != nil {
		return fail(ExitError, err)
	}
	path := a.kubeconfigPath()
	if err := k8s.ExportKubeconfig(path, raw, distro); err != nil {
		return fail(ExitError, fmt.Errorf("%s: %w", i18n.T("cli.k8s.kubeconfig_failed", path), err))
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.kubeconfig_done", path, k8s.ContextName(distro)))
	return nil
}

// owner 回傳目前開啟 k3s 的 machine（--machine 可覆寫）。
func (a *App) k8sOwner(cfg config.Config, machine string) (config.Machine, error) {
	if machine != "" {
		m, ok := cfg.FindMachine(machine)
		if !ok {
			return config.Machine{}, fail(ExitError, errors.New(i18n.T("cli.k8s.unknown_machine", machine)))
		}
		return m, nil
	}
	m, ok := cfg.K8sMachine()
	if !ok {
		return config.Machine{}, fail(ExitError, errors.New(i18n.T("cli.k8s.none_enabled")))
	}
	return m, nil
}

func (a *App) k8sOff(ctx context.Context, cfg config.Config) error {
	m, ok := cfg.K8sMachine()
	if !ok {
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.already_off"))
		return nil
	}
	if err := a.k8sManager(cfg, m.Distro).Stop(ctx); err != nil {
		return fail(ExitError, err)
	}
	if err := cfg.SetK8s(m.Distro, false); err != nil {
		return fail(ExitError, err)
	}
	if err := config.Save(a.ConfigPath, cfg); err != nil {
		return fail(ExitError, err)
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.disabled", m.Distro))
	return nil
}

func (a *App) k8sStatus(ctx context.Context, cfg config.Config, asJSON bool) error {
	m, ok := cfg.K8sMachine()
	if !ok {
		if asJSON {
			return a.writeJSON(map[string]string{"state": "off"})
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.all_off"))
		return nil
	}
	st := a.k8sManager(cfg, m.Distro).Status(ctx)
	if asJSON {
		return a.writeJSON(st)
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status_machine", st.Distro))
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status_state", string(st.State)))
	if st.Version != "" {
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status_version", st.Version))
	}
	if st.NodeName != "" {
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status_node", st.NodeName, st.NodeIP))
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status_endpoint", st.Endpoint, st.Context))
	if st.MemoryBytes > 0 {
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status_memory", st.MemoryBytes/(1<<20)))
	}
	if st.IPMismatch {
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status_ip_mismatch"))
	}
	return nil
}

func (a *App) k8sReset(ctx context.Context, cfg config.Config, yes bool) error {
	m, err := a.k8sOwner(cfg, "")
	if err != nil {
		return err
	}
	if !yes && !a.confirm(i18n.T("cli.k8s.reset_prompt", m.Distro)) {
		return fail(ExitError, errors.New(i18n.T("cli.k8s.cancelled")))
	}
	mgr := a.k8sManager(cfg, m.Distro)
	if err := mgr.Reset(ctx); err != nil {
		return fail(ExitError, err)
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.reset_done", m.Distro))
	// 重置後憑證已換新：只有曾匯出過才同步更新，不主動建立。
	if k8s.HasExportedContext(a.kubeconfigPath(), m.Distro) {
		return a.k8sExport(ctx, mgr, m.Distro)
	}
	return nil
}

func (a *App) k8sKubeconfig(ctx context.Context, cfg config.Config) error {
	m, err := a.k8sOwner(cfg, "")
	if err != nil {
		return err
	}
	return a.k8sExport(ctx, a.k8sManager(cfg, m.Distro), m.Distro)
}

func (a *App) k8sUninstall(ctx context.Context, cfg config.Config, machine string, yes bool) error {
	m, err := a.k8sOwner(cfg, machine)
	if err != nil {
		return err
	}
	if !yes && !a.confirm(i18n.T("cli.k8s.uninstall_prompt", m.Distro)) {
		return fail(ExitError, errors.New(i18n.T("cli.k8s.cancelled")))
	}
	mgr := a.k8sManager(cfg, m.Distro)
	if err := mgr.Uninstall(ctx); err != nil {
		return fail(ExitError, err)
	}
	if m.K8s {
		if err := cfg.SetK8s(m.Distro, false); err != nil {
			return fail(ExitError, err)
		}
		if err := config.Save(a.ConfigPath, cfg); err != nil {
			return fail(ExitError, err)
		}
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.uninstalled", m.Distro))
	return nil
}

// resumeK8s：wsld start 之後，若 active machine 設定為 k8s = true 就一併啟動 k3s。
// 失敗只警告（docker 已就緒，不讓整個 start 失敗）。
func (a *App) resumeK8s(ctx context.Context, cfg config.Config) {
	m, ok := cfg.K8sMachine()
	if !ok || !strings.EqualFold(m.Distro, cfg.Active) {
		return
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.resuming", m.Distro))
	if err := a.k8sManager(cfg, m.Distro).Start(ctx); err != nil {
		fmt.Fprintln(a.Stderr, i18n.T("cli.k8s.resume_failed", err))
	}
}
