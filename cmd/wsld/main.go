// wsld：管理 WSL2 Ubuntu 內 Docker Engine 的 Windows CLI（SPEC §5.4）。
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/iridance/wsld/internal/cli"
	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/doctor"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/wsl"
	"github.com/iridance/wsld/scripts"
)

// version 由 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 語系：預設英文；config.lang（與 GUI 右下角切換共用）；WSLD_LANG 環境變數可強制覆寫
	_ = config.MigrateLegacyAppDir()
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
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Stdin:        os.Stdin,
		ReadPassword: cli.TerminalPassword,
		ConfigPath:   cfgPath,
		WSL:          wsl.ExecRunner{},
		Processes:    doctor.WindowsProcesses,
		Bundle:       scripts.BootstrapBundle(),
		K8sBundle:    scripts.K8sBundle(),
		Exec:         cli.DefaultExec,
		LAN:          &lan.Manager{Host: lan.NewHost()},
		Version:      version,
		SelfInWSL:    selfInWSL(),
		DiskSize:     wsl.VHDXSize,
	}
	code := app.Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

// selfInWSL：wsld.exe 是否從 WSL 檔案系統執行（此時 wsl --shutdown 會讓它中途結束）。
func selfInWSL() bool {
	exe, err := os.Executable()
	return err == nil && wsl.RunningFromWSL(exe)
}
