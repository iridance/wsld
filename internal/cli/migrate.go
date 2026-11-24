package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/migrate"
)

// cmdMigrate：把 client distro 內舊的本機 dockerd 資料（image、具名 volume）複製到共用 engine。不刪來源。
func (a *App) cmdMigrate(ctx context.Context, args []string) error {
	fs := a.newFlags("migrate")
	distro := fs.String("distro", "", "client distro to migrate from (default: every client)")
	images := fs.Bool("images", true, "copy images")
	volumes := fs.Bool("volumes", true, "copy named volumes")
	dry := fs.Bool("dry-run", false, "list what would be copied without copying")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	var targets []config.Machine
	if *distro != "" {
		m, ok := cfg.FindMachine(*distro)
		switch {
		case !ok:
			return fail(ExitError, errors.New(i18n.T("cli.migrate.unknown", *distro)))
		case m.IsEngine():
			return fail(ExitError, errors.New(i18n.T("cli.migrate.is_engine", m.Distro)))
		case m.Role != config.RoleClient:
			return fail(ExitError, errors.New(i18n.T("cli.migrate.not_client", m.Distro)))
		}
		targets = append(targets, m)
	} else {
		for _, m := range cfg.Machines {
			if m.Role == config.RoleClient && m.Ready() {
				targets = append(targets, m)
			}
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(a.Stdout, i18n.T("cli.migrate.no_clients"))
		return nil
	}
	opts := migrate.Options{Images: *images, Volumes: *volumes, DryRun: *dry}
	var failed []string
	for _, m := range targets {
		res, err := migrate.Run(ctx, a.WSL, m.Distro, m.User, opts)
		if err != nil {
			if errors.Is(err, migrate.ErrNoLocalDocker) && *distro == "" {
				fmt.Fprintln(a.Stdout, i18n.T("cli.migrate.no_local", m.Distro))
				continue
			}
			fmt.Fprintln(a.Stderr, i18n.T("cli.error", fmt.Errorf("%s: %w", m.Distro, err)))
			failed = append(failed, m.Distro)
			continue
		}
		key := "cli.migrate.done"
		if *dry {
			key = "cli.migrate.would"
		}
		fmt.Fprintln(a.Stdout, i18n.T(key, m.Distro, len(res.Images), len(res.Volumes)))
		for _, s := range res.Skipped {
			fmt.Fprintln(a.Stdout, "  "+i18n.T("cli.migrate.skipped", s))
		}
		if !*dry {
			fmt.Fprintln(a.Stdout, "  "+i18n.T("cli.migrate.retire", m.Distro))
		}
	}
	if len(failed) > 0 {
		return fail(ExitError, errors.New(i18n.T("cli.migrate.failed", strings.Join(failed, ", "))))
	}
	return nil
}
