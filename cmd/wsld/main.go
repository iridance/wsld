// wsld：管理 WSL2 Ubuntu 內 Docker Engine 的 Windows CLI（SPEC §5.4）。
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/irice/wsl-docker/internal/cli"
	"github.com/irice/wsl-docker/internal/config"
	"github.com/irice/wsl-docker/internal/doctor"
	"github.com/irice/wsl-docker/internal/i18n"
	"github.com/irice/wsl-docker/internal/wsl"
	"github.com/irice/wsl-docker/scripts"
)

// version 由 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	i18n.Set(i18n.Detect())

	app := &cli.App{
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		ConfigPath: config.DefaultPath(),
		WSL:        wsl.ExecRunner{},
		Processes:  doctor.WindowsProcesses,
		Bundle:     scripts.BootstrapBundle(),
		Exec:       cli.DefaultExec,
		Version:    version,
	}
	code := app.Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
