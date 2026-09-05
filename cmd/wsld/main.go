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

	// 語系：預設英文；config.lang（與 GUI 右下角切換共用）；WSLD_LANG 環境變數可強制覆寫
	cfgPath := config.DefaultPath()
	if c, err := config.Load(cfgPath); err == nil {
		if l, ok := i18n.Parse(c.Lang); ok {
			i18n.Set(l)
		}
	}
	if l, ok := i18n.Parse(os.Getenv(i18n.EnvOverride)); ok {
		i18n.Set(l)
	}

	app := &cli.App{
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		ConfigPath: cfgPath,
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
