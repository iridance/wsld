//go:build windows

// wsld-gui：Wails v2 視窗 + 系統匣，與 wsld CLI 共用 internal core（SPEC §5.5）。
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/irice/wsl-docker/frontend"
	"github.com/irice/wsl-docker/internal/config"
	"github.com/irice/wsl-docker/internal/dockerapi"
	"github.com/irice/wsl-docker/internal/doctor"
	"github.com/irice/wsl-docker/internal/gui"
	"github.com/irice/wsl-docker/internal/i18n"
	"github.com/irice/wsl-docker/internal/lifecycle"
	"github.com/irice/wsl-docker/internal/wsl"
	"github.com/irice/wsl-docker/scripts"
)

//go:embed assets/tray.ico
var trayIcon []byte

var version = "dev"

func main() {
	startHidden := false
	for _, a := range os.Args[1:] {
		if a == "--tray" || a == "--hidden" {
			startHidden = true
		}
	}
	cfgPath := config.DefaultPath()
	cfg, cfgErr := config.Load(cfgPath)
	if cfgErr != nil {
		cfg = config.Default()
	}
	// 語系：預設英文；config.lang（GUI 右下角切換後寫入）；WSLD_LANG 環境變數可強制覆寫
	if l, ok := i18n.Parse(cfg.Lang); ok {
		i18n.Set(l)
	}
	if l, ok := i18n.Parse(os.Getenv(i18n.EnvOverride)); ok {
		i18n.Set(l)
	}
	engine := dockerapi.New(cfg.APIEndpoint, time.Duration(cfg.APITimeoutSec)*time.Second)
	mgr := &lifecycle.Manager{WSL: wsl.ExecRunner{}, Cfg: cfg, API: engine, Bundle: scripts.BootstrapBundle()}
	var life gui.Lifecycle = mgr
	if cfgErr != nil {
		life = gui.ConfigMissingLifecycle{Path: cfgPath}
	}

	exe, _ := os.Executable()
	exe, _ = filepath.Abs(exe)
	auto := gui.PlatformAutostart("wsld", fmt.Sprintf(`"%s" --tray`, exe))

	app := &guiApp{}
	srv := &gui.Server{
		Engine:         engine,
		Life:           life,
		Autostart:      auto,
		Version:        version,
		Token:          gui.NewToken(),
		LoadConfig:     func() (config.Config, error) { return config.Load(cfgPath) },
		SaveConfig:     func(c config.Config) error { return config.Save(cfgPath, c) },
		WSL:            wsl.ExecRunner{},
		Processes:      doctor.WindowsProcesses,
		OnWindowAction: app.onWindowAction,
		OnLangChange:   func(i18n.Lang) { app.relabelTray() },
	}
	base, stop, err := srv.ListenLoopback()
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer stop()
	app.srv, app.base, app.token, app.engine = srv, base, srv.Token, engine

	configJS := fmt.Sprintf(`window.__WSLD__={base:%q,token:%q};`, base, srv.Token)
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.TrimPrefix(r.URL.Path, "/") == "wsld-config.js" {
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write([]byte(configJS))
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	err = wails.Run(&options.App{
		Title:             i18n.T("gui.title"),
		Width:             1000,
		Height:            640,
		MinWidth:          720,
		MinHeight:         420,
		StartHidden:       startHidden,
		HideWindowOnClose: true, // 關閉 → 縮到系統匣；Tray「結束」才真正離開
		AssetServer:       &assetserver.Options{Assets: frontend.Dist(), Middleware: mw},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Windows: &windows.Options{
			WebviewUserDataPath: filepath.Join(filepath.Dir(cfgPath), "webview2"),
			Theme:               windows.SystemDefault,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// guiApp 持有 Wails context 與 tray 狀態。
type guiApp struct {
	ctx    context.Context
	srv    *gui.Server
	base   string
	token  string
	engine *dockerapi.Client

	trayStatus *systray.MenuItem
	trayOpen   *systray.MenuItem
	trayStart  *systray.MenuItem
	trayQuit   *systray.MenuItem
	lastState  string // 最近一次 tray 狀態鍵（gui.status.*），語系切換時重套
	quitting   atomic.Bool
}

func (a *guiApp) startup(ctx context.Context) {
	a.ctx = ctx
	go a.runTray()
}

func (a *guiApp) shutdown(context.Context) {
	if a.quitting.CompareAndSwap(false, true) {
		systray.Quit()
	}
}

func (a *guiApp) onWindowAction(action string) {
	if a.ctx == nil {
		return
	}
	switch action {
	case "hide":
		wruntime.WindowHide(a.ctx)
	case "show":
		wruntime.WindowShow(a.ctx)
	case "quit":
		a.quit()
	}
}

func (a *guiApp) quit() {
	if a.quitting.CompareAndSwap(false, true) {
		systray.Quit()
		if a.ctx != nil {
			wruntime.Quit(a.ctx)
		}
	}
}

// runTray 在專屬 OS 執行緒跑 systray 訊息迴圈（與 Wails 主執行緒並存）。
func (a *guiApp) runTray() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	systray.Run(a.trayReady, func() {})
}

func (a *guiApp) trayReady() {
	systray.SetIcon(trayIcon)
	systray.SetTooltip("wsld")
	a.trayStatus = systray.AddMenuItem(i18n.T("gui.status.disconnected"), "")
	a.trayStatus.Disable()
	systray.AddSeparator()
	a.trayOpen = systray.AddMenuItem(i18n.T("gui.tray.open"), "")
	a.trayStart = systray.AddMenuItem(i18n.T("gui.ensure"), "")
	systray.AddSeparator()
	a.trayQuit = systray.AddMenuItem(i18n.T("gui.tray.quit"), "")
	open, start, quit := a.trayOpen, a.trayStart, a.trayQuit

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				wruntime.WindowShow(a.ctx)
				wruntime.WindowUnminimise(a.ctx)
			case <-start.ClickedCh:
				go func() {
					a.setTray("gui.status.starting")
					req, _ := http.NewRequest(http.MethodPost, a.base+"/api/engine/start", nil)
					req.Header.Set("Authorization", "Bearer "+a.token)
					resp, err := (&http.Client{Timeout: 4 * time.Minute}).Do(req)
					if err == nil {
						resp.Body.Close()
					}
					a.pollOnce()
				}()
			case <-quit.ClickedCh:
				a.quit()
				return
			}
		}
	}()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		a.pollOnce()
		for range t.C {
			if a.quitting.Load() {
				return
			}
			a.pollOnce()
		}
	}()
}

func (a *guiApp) pollOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.engine.Ping(ctx); err != nil {
		if errors.Is(err, dockerapi.ErrUnreachable) {
			a.setTray("gui.status.disconnected")
		} else {
			a.setTray("gui.status.disconnected", err.Error())
		}
		return
	}
	a.setTray("gui.status.connected")
}

// setTray 以訊息鍵設定狀態（保留鍵值，語系切換時可重套）。
func (a *guiApp) setTray(key string, detail ...string) {
	a.lastState = key
	s := i18n.T(key)
	if len(detail) > 0 && detail[0] != "" {
		s += ": " + detail[0]
	}
	if a.trayStatus != nil {
		a.trayStatus.SetTitle(s)
	}
	systray.SetTooltip("wsld — " + s)
}

// relabelTray 於語系切換後重設 Tray 選單文字。
func (a *guiApp) relabelTray() {
	if a.trayOpen == nil {
		return
	}
	a.trayOpen.SetTitle(i18n.T("gui.tray.open"))
	a.trayStart.SetTitle(i18n.T("gui.ensure"))
	a.trayQuit.SetTitle(i18n.T("gui.tray.quit"))
	if a.lastState != "" {
		a.setTray(a.lastState)
	}
}
