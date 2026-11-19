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
	"github.com/iridance/wsld/internal/lifecycle"
	"github.com/iridance/wsld/internal/wsl"
)

const lanSubcommands = "on|off|status|restore-nat"

// cmdLan：wsld lan on|off|status|restore-nat。
// 區網存取由 wsld-gui 轉發（config lan_access）；restore-nat 清掉 0.9.0 留下的 mirrored 設定。
func (a *App) cmdLan(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", lanSubcommands)))
	}
	sub, rest := args[0], args[1:]
	fs := a.newFlags("lan " + sub)
	yes := fs.Bool("yes", false, "restart WSL without asking (restore-nat)")
	asJSON := fs.Bool("json", false, "JSON output (status)")
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
	case "on", "off":
		cfg, err := a.loadConfig()
		if err != nil {
			return err
		}
		cfg.LANAccess = sub == "on"
		if err := config.Save(a.ConfigPath, cfg); err != nil {
			return fail(ExitError, err)
		}
		if cfg.LANAccess {
			fmt.Fprintln(a.Stdout, i18n.T("cli.lan.enabled"))
		} else {
			fmt.Fprintln(a.Stdout, i18n.T("cli.lan.disabled"))
		}
		return a.lanStatus(ctx, false)
	case "restore-nat":
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.restore_notice"))
		if err := a.LAN.RestoreNAT(ctx); err != nil {
			return fail(ExitError, err)
		}
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.restored"))
		return a.lanRestart(ctx, *yes)
	default:
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", lanSubcommands)))
	}
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
	am, ok := cfg.ActiveMachine()
	if !ok {
		return nil
	}
	// 網路模式切換後舊連線可能暫佔 API 埠（TIME_WAIT）；等埠釋放後再起 dockerd。
	if port, err := cfg.APIPort(); err == nil {
		fmt.Fprintln(a.Stdout, i18n.T("cli.lan.waiting_port", port))
		if !a.LAN.WaitPortFree(ctx, port, lan.PortWaitTimeout) {
			fmt.Fprintln(a.Stdout, i18n.T("cli.lan.port_still_busy", port))
		}
	}
	var st lifecycle.Status
	if am.Ready() {
		// 已 init：只需重新啟動 docker.service（API 本來就不通，不要重跑 bootstrap）
		m := a.manager(cfg, a.apiFor(cfg))
		st, err = m.Start(ctx)
		if err != nil {
			return err
		}
		cfg = m.Cfg
		if err := config.Save(a.ConfigPath, cfg); err != nil {
			return fail(ExitError, err)
		}
	} else if st, cfg, err = a.bringUp(ctx, cfg); err != nil {
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

// lanStatusJSON 是 wsld lan status --json 的輸出。
type lanStatusJSON struct {
	Enabled   bool     `json:"enabled"`
	Addresses []string `json:"addresses"`
	Mirrored  bool     `json:"mirrored"`
}

func (a *App) lanStatus(ctx context.Context, asJSON bool) error {
	st := lanStatusJSON{Addresses: []string{}}
	if cfg, err := config.Load(a.ConfigPath); err == nil {
		st.Enabled = cfg.LANAccess
	}
	if ifs, err := a.LAN.Host.Interfaces(); err == nil {
		st.Addresses = lan.LANAddresses(ifs)
	}
	legacy, err := a.LAN.Legacy(ctx)
	if err != nil {
		return fail(ExitError, err)
	}
	st.Mirrored = legacy.Mirrored
	if asJSON {
		return a.writeJSON(st)
	}
	w := a.Stdout
	state := i18n.T("cli.lan.state_off")
	if st.Enabled {
		state = i18n.T("cli.lan.state_on")
	}
	fmt.Fprintln(w, i18n.T("cli.lan.title", state))
	addrs := strings.Join(st.Addresses, ", ")
	if addrs == "" {
		addrs = i18n.T("cli.lan.none")
	}
	fmt.Fprintln(w, i18n.T("cli.lan.addresses", addrs))
	if st.Enabled {
		for _, ip := range st.Addresses {
			fmt.Fprintln(w, i18n.T("cli.lan.connect", ip))
		}
		fmt.Fprintln(w, i18n.T("cli.lan.gui_note"))
	}
	if st.Mirrored {
		fmt.Fprintln(w, i18n.T("cli.lan.warn_mirrored"))
	}
	return nil
}

// lanLegacyFunc 給 doctor 用；未設定 LAN 時回 nil（略過遺留檢查）。
func (a *App) lanLegacyFunc() func(context.Context) (lan.Legacy, error) {
	if a.LAN == nil {
		return nil
	}
	return a.LAN.Legacy
}
