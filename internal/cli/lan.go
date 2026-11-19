package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/wsl"
)

// cmdLan：wsld lan on|off|status — 區網存取（mirrored 網路 + Hyper-V 防火牆）。
func (a *App) cmdLan(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "on|off|status")))
	}
	sub, rest := args[0], args[1:]
	fs := a.newFlags("lan " + sub)
	yes := fs.Bool("yes", false, "restart WSL without asking")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := parse(fs, rest); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(ExitUsage, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if a.LAN == nil {
		return fail(ExitError, errors.New(i18n.T("cli.lan.unavailable")))
	}
	switch sub {
	case "status":
		return a.lanStatus(ctx, *asJSON)
	case "on":
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.enable_notice"))
		if err := a.LAN.Enable(ctx); err != nil {
			if errors.Is(err, lan.ErrUnsupported) {
				return fail(ExitError, errors.New(i18n.T("cli.lan.unsupported", a.LAN.Host.Build())))
			}
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.enabled"))
	case "off":
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.disable_notice"))
		if err := a.LAN.Disable(ctx); err != nil {
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.disabled"))
	default:
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "on|off|status")))
	}
	if err := a.lanRestart(ctx, *yes); err != nil {
		return err
	}
	return a.lanStatus(ctx, false)
}

// lanRestart 在確認後 wsl --shutdown，並重新啟動 active machine（讓 .wslconfig 生效）。
func (a *App) lanRestart(ctx context.Context, yes bool) error {
	if !yes && !a.confirm(i18n.T("cli.lan.restart_prompt")) {
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.restart_skipped"))
		return nil
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.lan.restarting"))
	if err := wsl.Shutdown(ctx, a.WSL); err != nil {
		return fail(ExitError, err)
	}
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return nil // 尚未 init：只需 shutdown
	}
	if _, ok := cfg.ActiveMachine(); !ok {
		return nil
	}
	st, cfg, err := a.bringUp(ctx, cfg)
	if err != nil {
		return err
	}
	a.printStatus(st)
	fmt.Fprintln(a.Stdout, i18n.T("cli.start_done", cfg.APIEndpoint))
	return nil
}

// confirm 從 Stdin 讀一行 y/yes（不分大小寫）；Stdin 為 nil 或 EOF 視為否。
func (a *App) confirm(prompt string) bool {
	if a.Stdin == nil {
		return false
	}
	fmt.Fprint(a.Stdout, prompt)
	line, _ := bufio.NewReader(a.Stdin).ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

func (a *App) lanStatus(ctx context.Context, asJSON bool) error {
	distro := ""
	if cfg, err := config.Load(a.ConfigPath); err == nil {
		if m, ok := cfg.ActiveMachine(); ok {
			distro = m.Distro
		}
	}
	st, err := a.LAN.Status(ctx, distro)
	if err != nil {
		return fail(ExitError, err)
	}
	if asJSON {
		return a.writeJSON(st)
	}
	w := a.Stdout
	state := i18n.T("cli.lan.state_off")
	if st.Enabled {
		state = i18n.T("cli.lan.state_on")
	}
	fmt.Fprintln(w, i18n.T("cli.lan.title", state))
	configured := st.Configured
	if configured == "" {
		configured = i18n.T("cli.lan.default_nat")
	}
	fmt.Fprintln(w, i18n.T("cli.lan.configured", configured))
	fmt.Fprintln(w, i18n.T("cli.lan.effective", orUnknown(st.Effective)))
	fmt.Fprintln(w, i18n.T("cli.lan.firewall", firewallText(st.Firewall)))
	fmt.Fprintln(w, i18n.T("cli.lan.profiles", orNone(strings.Join(st.Profiles, ", "))))
	fmt.Fprintln(w, i18n.T("cli.lan.addresses", orNone(strings.Join(st.Addresses, ", "))))
	if st.Ready() {
		for _, ip := range st.Addresses {
			fmt.Fprintln(w, i18n.T("cli.lan.connect", ip))
		}
		fmt.Fprintln(w, i18n.T("cli.lan.hint_isolation"))
	}
	if !st.Supported {
		fmt.Fprintln(w, i18n.T("cli.lan.unsupported", st.Build))
	}
	if st.RestartPending {
		fmt.Fprintln(w, i18n.T("cli.lan.warn_restart"))
	}
	if st.Enabled && st.Firewall == lan.FirewallMissing {
		fmt.Fprintln(w, i18n.T("cli.lan.warn_firewall"))
	}
	if st.Enabled && st.PublicNetwork {
		fmt.Fprintln(w, i18n.T("cli.lan.warn_public"))
	}
	return nil
}

func firewallText(s string) string {
	switch s {
	case lan.FirewallPresent:
		return i18n.T("cli.lan.fw_present")
	case lan.FirewallMissing:
		return i18n.T("cli.lan.fw_missing")
	default:
		return i18n.T("cli.lan.unknown")
	}
}

func orUnknown(s string) string {
	if s == "" {
		return i18n.T("cli.lan.unknown")
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return i18n.T("cli.lan.none")
	}
	return s
}

// lanStatusFunc 給 doctor 用；未設定 LAN 時回 nil（略過該項）。
func (a *App) lanStatusFunc() func(context.Context, string) (lan.Status, error) {
	if a.LAN == nil {
		return nil
	}
	return a.LAN.Status
}
