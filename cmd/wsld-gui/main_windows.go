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
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/iridance/wsld/frontend"
	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/doctor"
	"github.com/iridance/wsld/internal/gui"
	"github.com/iridance/wsld/internal/host"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/lifecycle"
	"github.com/iridance/wsld/internal/wsl"
	"github.com/iridance/wsld/scripts"
)

//go:embed assets/tray.ico
var trayIcon []byte

var version = "dev"

func main() {
	// 以 UAC 啟動的子行程：只替自己加入防火牆規則（區網存取）後結束；結束碼 = HRESULT，供父行程回報
	if len(os.Args) == 2 && os.Args[1] == lan.AllowFirewallFlag {
		exe, err := os.Executable()
		if err == nil {
			err = lan.AddAppRule(exe)
		}
		if err != nil {
			os.Exit(int(lan.HRESULTOf(err)))
		}
		os.Exit(0)
	}

	startHidden := false
	for _, a := range os.Args[1:] {
		if a == "--tray" || a == "--hidden" {
			startHidden = true
		}
	}

	si, primary, err := acquireSingleInstance()
	if err != nil {
		log.Fatalf("single instance: %v", err)
	}
	if !primary {
		// 已有實例：一般啟動則喚醒主視窗；--tray 靜默退出（不重複 systray）
		if !startHidden {
			notifyExistingInstance()
		}
		os.Exit(0)
	}
	defer si.Release()

	_ = config.MigrateLegacyAppDir()
	cfgPath := config.DefaultPath()
	setupCrashLog(filepath.Dir(cfgPath))
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
	engine := dockerClient(cfg)
	newMgr := func(c config.Config) *lifecycle.Manager {
		eng := dockerClient(c)
		return &lifecycle.Manager{
			WSL: wsl.ExecRunner{}, Cfg: c, API: eng, Bundle: scripts.BootstrapBundle(),
			NewAPI: func(nc config.Config) lifecycle.Pinger { return dockerClient(nc) },
		}
	}
	mgr := newMgr(cfg)
	var life gui.Lifecycle = mgr
	if cfgErr != nil {
		life = gui.ConfigMissingLifecycle{Path: cfgPath}
	}

	exe, _ := os.Executable()
	exe, _ = filepath.Abs(exe)
	auto := gui.PlatformAutostart("wsld", fmt.Sprintf(`"%s" --tray`, exe))

	app := &guiApp{si: si}
	srv := &gui.Server{
		Engine:          engine,
		Life:            life,
		Autostart:       auto,
		Version:         version,
		Token:           gui.NewToken(),
		LoadConfig:      func() (config.Config, error) { return config.Load(cfgPath) },
		SaveConfig:      func(c config.Config) error { return config.Save(cfgPath, c) },
		WSL:             wsl.ExecRunner{},
		KeepAlive:       &wsl.KeepAlive{Spawner: wsl.ExecRunner{}},
		DistroDisk:      wsl.VHDXSize,
		DistroDiskLimit: wsl.VHDXLimit,
		SelfInWSL:       wsl.RunningFromWSL(exe),
		Processes:       doctor.WindowsProcesses,
		OnWindowAction:  app.onWindowAction,
		OnDistroAction:  app.onDistroAction,
		OnOpenURL:       app.onOpenURL,
		Shell:           gui.HostDistroShell{},
		OnLangChange:    func(i18n.Lang) { app.relabelTray() },
		NewEngine:       func(c config.Config) gui.Engine { return dockerClient(c) },
		NewLifecycle:    func(c config.Config) gui.Lifecycle { return newMgr(c) },
		InstallProbe:    gui.WSLInstallProbe{Client: &http.Client{Timeout: 20 * time.Second}},
		K8sFor:          gui.NewK8sFor(wsl.ExecRunner{}, scripts.K8sBundle()),
		KubeconfigPath:  filepath.Join(filepath.Dir(cfgPath), "kubeconfig.yaml"),
		InstallK8sDistro: func(ctx context.Context, base, name string, size int) error {
			return wsl.Install(ctx, wsl.ExecRunner{}, base, name, size)
		},
		LAN:       &lan.Manager{Host: lan.NewHost(), Exe: exe},
		Forwarder: &lan.Forwarder{},
	}
	base, stop, err := srv.ListenLoopback()
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer stop()
	// 區網存取：依 config lan_access 在內網 IP 轉發容器 published 埠（程式結束時關閉）
	lanCtx, stopLAN := context.WithCancel(context.Background())
	defer stopLAN()
	go srv.RunLAN(lanCtx, 3*time.Second)
	// 啟動 wsld 就是啟動引擎：首次使用先建立 docker-engine，之後帶起 docker；
	// 之後由 Supervise 持續確認，engine 掉了（WSL 關閉、當機）自動拉起，失敗退避重試。
	superviseCtx, stopSupervise := context.WithCancel(context.Background())
	defer stopSupervise()
	go func() {
		if err := srv.AutoStart(); err != nil {
			log.Printf("auto start: %v", err)
		}
		srv.Supervise(superviseCtx, 5*time.Second)
	}()
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
	si     *singleInstance

	trayStatus *systray.MenuItem
	trayOpen   *systray.MenuItem
	trayQuit   *systray.MenuItem
	lastState  string // 最近一次 tray 狀態鍵（gui.status.*），語系切換時重套
	quitting   atomic.Bool
}

func (a *guiApp) startup(ctx context.Context) {
	a.ctx = ctx
	if a.si != nil {
		a.si.WatchShow(ctx, func() {
			wruntime.WindowShow(ctx)
			wruntime.WindowUnminimise(ctx)
		})
	}
	go a.runTray()
}

func (a *guiApp) shutdown(context.Context) {
	a.srv.ReleaseKeepAlive()
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

func (a *guiApp) onDistroAction(action, distro, user string) error {
	switch action {
	case "open-folder":
		return host.OpenFolder(distro, user)
	case "open-terminal":
		return host.OpenTerminal(distro, user)
	default:
		return fmt.Errorf("unknown distro action %q", action)
	}
}

func (a *guiApp) onOpenURL(url string) error {
	if a.ctx == nil {
		return fmt.Errorf("app not ready")
	}
	wruntime.BrowserOpenURL(a.ctx, url)
	return nil
}

func (a *guiApp) quit() {
	a.srv.ReleaseKeepAlive()
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
	systray.AddSeparator()
	a.trayQuit = systray.AddMenuItem(i18n.T("gui.tray.quit"), "")
	open, quit := a.trayOpen, a.trayQuit

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				wruntime.WindowShow(a.ctx)
				wruntime.WindowUnminimise(a.ctx)
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
	a.trayQuit.SetTitle(i18n.T("gui.tray.quit"))
	if a.lastState != "" {
		a.setTray(a.lastState)
	}
}

// dockerClient 依 config 建立 Docker client。
// api_endpoint 為空（占位／未 init）時不得退回預設 23750，否則會誤連其他 distro 的引擎。
func dockerClient(c config.Config) *dockerapi.Client {
	to := time.Duration(c.APITimeoutSec) * time.Second
	if to <= 0 {
		to = time.Duration(config.DefaultAPITimeoutSec) * time.Second
	}
	// 空 endpoint（尚未 init）→ 所有請求回「machine not initialized」，UI 顯示未就緒而非舊列表
	return dockerapi.New(c.APIEndpoint, to)
}

// setupCrashLog 把致命錯誤（panic、fatal error）的訊息與 stack 寫到 <設定目錄>\crash.log。
// wsld-gui 以 -H windowsgui 建置、沒有主控台，否則閃退時什麼都不會留下。
func setupCrashLog(dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "\n=== wsld-gui %s started %s (pid %d) ===\n", version, time.Now().Format(time.RFC3339), os.Getpid())
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	log.SetOutput(f) // log.Fatal 等也記下
}
