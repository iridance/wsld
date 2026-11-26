package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/k8s"
)

const k8sSubcommands = "status|on|off|restart|reset|pods|svc|forward|kubeconfig|apply|compose|stack"

// KubeconfigPath 是 Windows 端匯出的 kubeconfig（與 config.toml 同目錄；GUI 也用同一個）。
func KubeconfigPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "kubeconfig.yaml")
}

func (a *App) k8sManager(cfg config.Config) *k8s.Manager {
	m := &k8s.Manager{
		WSL: a.WSL, Distro: cfg.K8sDistro(), Base: cfg.EngineBaseOrDefault(), APIPort: cfg.K8sAPIPortOrDefault(),
		TLSSAN: cfg.K8sTLSSAN, KubeletArgs: cfg.K8sKubeletArgs,
		Bundle: a.K8sBundle, Output: a.Stderr,
		Progress: func(step string) { fmt.Fprintln(a.Stdout, "→ "+i18n.T("gui.k8s.step."+step)) },
	}
	if e, ok := cfg.EngineMachine(); ok {
		m.EngineDistro = e.Distro
	}
	return m
}

// cmdK8s：wsld k8s <子命令>。k3s 跑在專用的 k8s-engine，runtime 是 engine 的 dockerd。
func (a *App) cmdK8s(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", k8sSubcommands)))
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "forward":
		return a.cmdK8sForward(ctx, rest)
	case "apply":
		return a.cmdK8sApply(ctx, rest)
	case "compose":
		return a.cmdK8sCompose(ctx, rest)
	case "stack":
		return a.cmdK8sStack(ctx, rest)
	}
	fs := a.newFlags("k8s " + sub)
	asJSON := fs.Bool("json", false, "JSON output (status, pods, svc)")
	yes := fs.Bool("yes", false, "confirm (reset)")
	current := fs.Bool("current", true, "also set current-context to wsld (kubeconfig)")
	if err := parse(fs, rest); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(ExitUsage, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	switch sub {
	case "status":
		cfg, err := a.loadConfig()
		if err != nil {
			return err
		}
		st := k8s.Status{Distro: cfg.K8sDistro(), State: k8s.StateOff, Endpoint: k8s.Endpoint(cfg.K8sAPIPortOrDefault())}
		if cfg.K8s {
			st = a.k8sManager(cfg).Status(ctx)
		}
		if *asJSON {
			return json.NewEncoder(a.Stdout).Encode(map[string]any{"enabled": cfg.K8s, "status": st, "kubeconfig": KubeconfigPath(a.ConfigPath)})
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.status", k8sOnOff(cfg.K8s), st.Distro, i18n.T("gui.k8s.state."+string(st.State)), st.Endpoint))
		if st.Version != "" {
			fmt.Fprintln(a.Stdout, "  k3s "+st.Version)
		}
		if cfg.K8s {
			fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.kubeconfig", KubeconfigPath(a.ConfigPath), k8s.SharedKubeconfig))
		}
		return nil
	case "on":
		if err := a.ensureEngine(ctx); err != nil {
			return err
		}
		cfg, err := a.loadConfig()
		if err != nil {
			return err
		}
		if err := cfg.SetK8sMachine(cfg.K8sDistro()); err != nil {
			return fail(ExitError, err)
		}
		if err := config.Save(a.ConfigPath, cfg); err != nil {
			return fail(ExitError, err)
		}
		kc, err := a.k8sManager(cfg).Enable(ctx)
		if err != nil {
			return fail(ExitNotReady, err)
		}
		cfg.K8s = true
		if err := config.Save(a.ConfigPath, cfg); err != nil {
			return fail(ExitError, err)
		}
		if err := a.exportKubeconfig(cfg, kc); err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.ready", k8s.Endpoint(cfg.K8sAPIPortOrDefault())))
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.kubeconfig", KubeconfigPath(a.ConfigPath), k8s.SharedKubeconfig))
		return nil
	case "off":
		cfg, err := a.loadConfig()
		if err != nil {
			return err
		}
		cfg.K8s = false
		if err := config.Save(a.ConfigPath, cfg); err != nil {
			return fail(ExitError, err)
		}
		if _, ok := cfg.K8sMachine(); ok {
			if err := a.k8sManager(cfg).Disable(ctx); err != nil {
				return fail(ExitError, err)
			}
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.off"))
		return nil
	case "restart", "reset":
		if sub == "reset" && !*yes {
			return fail(ExitUsage, errors.New(i18n.T("cli.k8s.reset_confirm")))
		}
		cfg, err := a.k8sOn()
		if err != nil {
			return err
		}
		if err := a.ensureEngine(ctx); err != nil {
			return err
		}
		m := a.k8sManager(cfg)
		var kc []byte
		if sub == "reset" {
			kc, err = m.Reset(ctx)
		} else {
			kc, err = m.Restart(ctx)
		}
		if err != nil {
			return fail(ExitNotReady, err)
		}
		if err := a.exportKubeconfig(cfg, kc); err != nil {
			return err
		}
		if sub == "reset" {
			fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.reset_done"))
		} else {
			fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.restarted"))
		}
		return nil
	case "pods":
		cfg, err := a.k8sOn()
		if err != nil {
			return err
		}
		pods, err := a.k8sManager(cfg).ListPods(ctx)
		if err != nil {
			return fail(ExitError, err)
		}
		if *asJSON {
			return json.NewEncoder(a.Stdout).Encode(pods)
		}
		if len(pods) == 0 {
			fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.no_pods"))
			return nil
		}
		tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tREADY\tSTATUS\tRESTARTS\tIP\tAGE")
		for _, p := range pods {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", p.Namespace, p.Name, p.Ready, p.Status, p.Restarts, dash(p.IP), age(p.Created))
		}
		return tw.Flush()
	case "svc", "services":
		cfg, err := a.k8sOn()
		if err != nil {
			return err
		}
		svcs, err := a.k8sManager(cfg).ListServices(ctx)
		if err != nil {
			return fail(ExitError, err)
		}
		if *asJSON {
			return json.NewEncoder(a.Stdout).Encode(svcs)
		}
		if len(svcs) == 0 {
			fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.no_services"))
			return nil
		}
		tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tTYPE\tCLUSTER-IP\tPORTS\tURL")
		for _, s := range svcs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Namespace, s.Name, s.Type, dash(s.ClusterIP), FormatServicePorts(s.Ports), dash(strings.Join(s.URLs, " ")))
		}
		return tw.Flush()
	case "kubeconfig":
		cfg, err := a.k8sOn()
		if err != nil {
			return err
		}
		kc, err := os.ReadFile(KubeconfigPath(a.ConfigPath))
		if err != nil {
			if kc, err = a.k8sManager(cfg).Kubeconfig(ctx); err != nil {
				return fail(ExitError, err)
			}
		}
		p := k8s.UserKubeconfigPath()
		if err := k8s.MergeKubeconfigFile(p, kc, *current); err != nil {
			return fail(ExitError, fmt.Errorf("%s: %w", i18n.T("k8s.merge_failed", p), err))
		}
		cfg.K8sMergeKubeconfig = true
		if err := config.Save(a.ConfigPath, cfg); err != nil {
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.merged", p))
		return nil
	default:
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", k8sSubcommands)))
	}
}

// cmdK8sForward：wsld k8s forward <目標> <本機>[:<遠端>] | forward ls | forward rm <本機埠>。
func (a *App) cmdK8sForward(ctx context.Context, args []string) error {
	cfg, err := a.k8sOn()
	if err != nil {
		return err
	}
	m := a.k8sManager(cfg)
	switch {
	case len(args) == 0 || (len(args) == 1 && (args[0] == "ls" || args[0] == "list")):
		fws, err := m.ListForwards(ctx)
		if err != nil {
			return fail(ExitError, err)
		}
		if len(fws) == 0 {
			fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.forward_none"))
			return nil
		}
		tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "LOCAL\tTARGET\tREMOTE\tRUNNING")
		for _, f := range fws {
			fmt.Fprintf(tw, "%d\t%s/%s/%s\t%d\t%v\n", f.LocalPort, f.Namespace, f.Kind, f.Name, f.RemotePort, f.Running)
		}
		return tw.Flush()
	case len(args) == 2 && (args[0] == "rm" || args[0] == "stop"):
		port, err := strconv.Atoi(args[1])
		if err != nil {
			return fail(ExitUsage, errors.New(i18n.T("k8s.bad_port", args[1])))
		}
		if err := m.StopForward(ctx, port); err != nil {
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.forward_removed", port))
		return nil
	case len(args) == 2:
		ns, kind, name, err := k8s.ParseForwardTarget(args[0])
		if err != nil {
			return fail(ExitUsage, err)
		}
		local, remote, err := k8s.ParsePortPair(args[1])
		if err != nil {
			return fail(ExitUsage, err)
		}
		f, err := m.Forward(ctx, k8s.ForwardRequest{Namespace: ns, Kind: kind, Name: name, LocalPort: local, RemotePort: remote})
		if err != nil {
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.forward_started", fmt.Sprintf("%s/%s/%s:%d", ns, kind, name, f.RemotePort), f.URL))
		return nil
	default:
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "forward [namespace/]svc|pod/<name> <local>[:<remote>] | forward ls | forward rm <local>")))
	}
}

// cmdKubectl：wsld kubectl <參數…> 在 k8s-engine 跑 k3s kubectl（繼承 stdio；Windows 不必另裝 kubectl）。
func (a *App) cmdKubectl(ctx context.Context, args []string) error {
	cfg, err := a.k8sOn()
	if err != nil {
		return err
	}
	if a.Exec == nil {
		return errors.New("exec not supported in this build")
	}
	wslArgs := append([]string{"-d", cfg.K8sDistro(), "-u", "root", "--exec", "k3s", "kubectl"}, args...)
	if err := a.Exec(ctx, "wsl.exe", wslArgs...); err != nil {
		return fail(ExitError, err)
	}
	return nil
}

// k8sOn 讀設定並要求 Kubernetes 已開啟。
func (a *App) k8sOn() (config.Config, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return cfg, err
	}
	if !cfg.K8s {
		return cfg, fail(ExitNotReady, errors.New(i18n.T("k8s.not_running")))
	}
	return cfg, nil
}

// exportKubeconfig 寫出 Windows 端的 kubeconfig；開啟了 k8s_merge_kubeconfig 時同步更新 ~/.kube/config。
func (a *App) exportKubeconfig(cfg config.Config, kc []byte) error {
	if len(kc) == 0 {
		return nil
	}
	p := KubeconfigPath(a.ConfigPath)
	if err := os.WriteFile(p, kc, 0o600); err != nil {
		return fail(ExitError, err)
	}
	if cfg.K8sMergeKubeconfig {
		up := k8s.UserKubeconfigPath()
		if err := k8s.MergeKubeconfigFile(up, kc, false); err != nil {
			return fail(ExitError, fmt.Errorf("%s: %w", i18n.T("k8s.merge_failed", up), err))
		}
	}
	return nil
}

// FormatServicePorts 近似 kubectl 的 PORT(S) 欄：80:30080/TCP。
func FormatServicePorts(ps []k8s.ServicePort) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		s := strconv.Itoa(p.Port)
		if p.NodePort > 0 {
			s += ":" + strconv.Itoa(p.NodePort)
		}
		parts = append(parts, s+"/"+p.Protocol)
	}
	return dash(strings.Join(parts, ","))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

func k8sOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
