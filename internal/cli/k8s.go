package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/k8s"
)

const k8sSubcommands = "status|on|off"

// KubeconfigPath 是 Windows 端匯出的 kubeconfig（與 config.toml 同目錄；GUI 也用同一個）。
func KubeconfigPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "kubeconfig.yaml")
}

func (a *App) k8sManager(cfg config.Config) *k8s.Manager {
	m := &k8s.Manager{
		WSL: a.WSL, Distro: cfg.K8sDistro(), Base: cfg.EngineBaseOrDefault(), APIPort: cfg.K8sAPIPortOrDefault(),
		Bundle: a.K8sBundle, Output: a.Stderr,
		Progress: func(step string) { fmt.Fprintln(a.Stdout, "→ "+i18n.T("gui.k8s.step."+step)) },
	}
	if e, ok := cfg.EngineMachine(); ok {
		m.EngineDistro = e.Distro
	}
	return m
}

// cmdK8s：wsld k8s status|on|off。k3s 跑在專用的 k8s-engine，runtime 是 engine 的 dockerd。
func (a *App) cmdK8s(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", k8sSubcommands)))
	}
	sub, rest := args[0], args[1:]
	fs := a.newFlags("k8s " + sub)
	asJSON := fs.Bool("json", false, "JSON output (status)")
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
		p := KubeconfigPath(a.ConfigPath)
		if err := os.WriteFile(p, kc, 0o600); err != nil {
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.ready", k8s.Endpoint(cfg.K8sAPIPortOrDefault())))
		fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.kubeconfig", p, k8s.SharedKubeconfig))
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
	default:
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", k8sSubcommands)))
	}
}

func k8sOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
