// wsld-gui-testhost：以 FakeEngine／FakeLifecycle 服務 GUI 前端，供 Playwright 測試與前端開發。
// 額外提供 /__test/* 端點控制場景（引擎斷線、推送日誌、切換語系…）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/gui"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/wsl"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:34199", "listen address")
	dist := flag.String("dist", "frontend/dist", "built frontend directory")
	lang := flag.String("lang", "en", "initial language (en|zh-TW)")
	cfgPath := flag.String("config", "", "config path (default: temp file)")
	flag.Parse()

	if *cfgPath == "" {
		dir, err := os.MkdirTemp("", "wsld-testhost")
		if err != nil {
			log.Fatal(err)
		}
		*cfgPath = filepath.Join(dir, "config.toml")
	}
	i18n.Set(i18n.Lang(*lang))

	engine := gui.NewFakeEngine()
	life := &gui.FakeLifecycle{Engine: engine, Distro: "Ubuntu-26.04", Endpoint: "tcp://127.0.0.1:23750"}
	auto := &gui.FakeAutostart{}
	fwsl := gui.NewFakeWSL()
	// 新增機器的預設 Linux 帳號固定為 irice（不隨跑測試的 OS 使用者變動）
	wsl.CurrentUsername = func() (string, error) { return `PC\irice`, nil }
	defer wsl.BypassSampleSleepForTest()()
	reset := func() {
		c := config.Default()
		c.UpsertMachine(config.Machine{Distro: "Ubuntu-26.04", User: "irice", APIEndpoint: config.EndpointForPort(config.DefaultPort)})
		_ = c.SetEngine("Ubuntu-26.04") // 這台就是共用 engine
		_ = c.SetActive("Ubuntu-26.04")
		if err := config.Save(*cfgPath, c); err != nil {
			log.Fatal(err)
		}
	}
	reset()

	// 區網存取：.wslconfig 寫在暫存目錄；轉發器不真的綁 192.168.x.x（改綁 127.0.0.1 隨機埠）。
	lanDir, err := os.MkdirTemp("", "wsld-testhost-lan")
	if err != nil {
		log.Fatal(err)
	}
	lanHost := &lan.FakeHost{Dir: lanDir}
	lanMgr := &lan.Manager{Host: lanHost, Exe: `C:\wsld\wsld-gui.exe`, Sleep: func(time.Duration) {}}
	var lanMu sync.Mutex
	lanBusy := map[int]bool{} // /__test/lan?busy=8080：模擬該埠監聽失敗
	forwarder := &lan.Forwarder{Listen: func(network, addr string) (net.Listener, error) {
		_, p, _ := net.SplitHostPort(addr)
		port, _ := strconv.Atoi(p)
		lanMu.Lock()
		busy := lanBusy[port]
		lanMu.Unlock()
		if busy {
			return nil, fmt.Errorf("listen tcp %s: bind: Only one usage of each socket address is normally permitted.", addr)
		}
		return net.Listen("tcp", "127.0.0.1:0")
	}}
	lanConfigPath, _ := lanHost.ConfigPath()
	resetLAN := func() {
		_ = os.Remove(lanConfigPath)
		_ = os.Remove(lanConfigPath + ".wsld.bak")
		lanHost.Set(func(h *lan.FakeHost) {
			h.Rules, h.FirewallErr, h.RemoveCalls = 0, nil, 0
			if h.AllowHold != nil {
				close(h.AllowHold)
			}
			h.AppState, h.AllowErr, h.AllowCalls, h.AllowHold, h.Elevated = "", nil, 0, nil, nil
		})
		lanMu.Lock()
		lanBusy = map[int]bool{}
		lanMu.Unlock()
		forwarder.Close()
	}

	var winMu sync.Mutex
	var windowActions []string

	srv := &gui.Server{
		Engine: engine, Life: life, Autostart: auto, Version: "testhost", WSL: fwsl,
		Files:           gui.NewFakeFileHost(),
		DistroDisk:      fwsl.VHDXSize,
		DistroDiskLimit: fwsl.VHDXLimit,
		DiskCacheTTL:    -1, // /__test/reset 等直接改 fake 狀態，不能吃到快取
		LAN:             lanMgr,
		Forwarder:       forwarder,
		Shell:           gui.FakeDistroShell{},
		LoadConfig:      func() (config.Config, error) { return config.Load(*cfgPath) },
		SaveConfig:      func(c config.Config) error { return config.Save(*cfgPath, c) },
		Assets:          spaHandler(*dist),
		OnWindowAction: func(a string) {
			winMu.Lock()
			windowActions = append(windowActions, a)
			winMu.Unlock()
		},
		OnDistroAction: func(a string) error {
			winMu.Lock()
			windowActions = append(windowActions, a)
			winMu.Unlock()
			return nil
		},
		OnOpenURL: func(u string) error {
			winMu.Lock()
			windowActions = append(windowActions, "open-url:"+u)
			winMu.Unlock()
			return nil
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /__test/engine/{state}", func(w http.ResponseWriter, r *http.Request) {
		engine.SetDown(r.PathValue("state") == "down")
		ok(w)
	})
	mux.HandleFunc("POST /__test/action-delay", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		engine.SetActionDelay(time.Duration(ms) * time.Millisecond)
		ok(w)
	})
	mux.HandleFunc("POST /__test/list-hold", func(w http.ResponseWriter, r *http.Request) {
		engine.ArmListHold()
		ok(w)
	})
	mux.HandleFunc("POST /__test/list-release", func(w http.ResponseWriter, r *http.Request) {
		engine.ReleaseListHold()
		ok(w)
	})
	mux.HandleFunc("GET /__test/list-held", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"waiting": engine.ListHoldWaiting()})
	})
	mux.HandleFunc("POST /__test/lifecycle/{state}", func(w http.ResponseWriter, r *http.Request) {
		life.Broken = r.PathValue("state") == "broken"
		ok(w)
	})
	mux.HandleFunc("POST /__test/log", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		engine.AppendLog(q.Get("name"), q.Get("line"))
		ok(w)
	})
	mux.HandleFunc("POST /__test/container", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		if state == "" {
			state = "running"
		}
		if proj := q.Get("project"); proj != "" {
			engine.AddComposeContainer(proj, q.Get("service"), q.Get("image"), state)
		} else {
			// ports=3000,3001：以 0.0.0.0 發佈（區網轉發測試用）
			var ports []dockerapi.Port
			for _, p := range strings.Split(q.Get("ports"), ",") {
				if n, err := strconv.Atoi(p); err == nil {
					ports = append(ports, dockerapi.Port{IP: "0.0.0.0", PrivatePort: n, PublicPort: n, Type: "tcp"})
				}
			}
			engine.AddContainer(q.Get("name"), q.Get("image"), state, ports)
		}
		ok(w)
	})
	mux.HandleFunc("POST /__test/image", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		size, _ := strconv.ParseInt(q.Get("size"), 10, 64)
		used, _ := strconv.Atoi(q.Get("containers"))
		engine.AddImage(q.Get("tag"), size, used)
		ok(w)
	})
	mux.HandleFunc("POST /__test/volume", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		engine.AddVolume(q.Get("name"), q.Get("in_use") == "1")
		ok(w)
	})
	mux.HandleFunc("POST /__test/network", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		engine.AddNetwork(q.Get("name"), q.Get("driver"), q.Get("in_use") == "1")
		ok(w)
	})
	mux.HandleFunc("POST /__test/wsl/fail-install", func(w http.ResponseWriter, r *http.Request) {
		fwsl.FailInstall = r.URL.Query().Get("on") != "0"
		ok(w)
	})
	mux.HandleFunc("POST /__test/wsl/fail-resize", func(w http.ResponseWriter, r *http.Request) {
		fwsl.FailResize = r.URL.Query().Get("on") != "0"
		ok(w)
	})
	mux.HandleFunc("POST /__test/wsl/fs-min", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("blocks"), 10, 64)
		fwsl.SetFSMinBlocks(r.URL.Query().Get("distro"), n)
		ok(w)
	})
	mux.HandleFunc("POST /__test/wsl/self-in-wsl", func(w http.ResponseWriter, r *http.Request) {
		srv.SelfInWSL = r.URL.Query().Get("on") != "0"
		ok(w)
	})
	mux.HandleFunc("GET /__test/wsl/last-install", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"args": fwsl.LastInstallArgs()})
	})
	mux.HandleFunc("GET /__test/wsl/installed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fwsl.Installed())
	})
	mux.HandleFunc("GET /__test/wsl/account", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		pw, set := fwsl.Password(q.Get("distro"), q.Get("user"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"password_set": set, "password": pw, "wsl_conf": fwsl.Conf(q.Get("distro"))})
	})
	mux.HandleFunc("POST /__test/lang/{lang}", func(w http.ResponseWriter, r *http.Request) {
		i18n.Set(i18n.Lang(r.PathValue("lang")))
		ok(w)
	})
	mux.HandleFunc("POST /__test/active-not-ready", func(w http.ResponseWriter, r *http.Request) {
		c, err := config.Load(*cfgPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		m, okm := c.ActiveMachine()
		if !okm {
			http.Error(w, "no active machine", http.StatusBadRequest)
			return
		}
		m.User = ""
		m.APIEndpoint = ""
		c.UpsertMachine(m)
		if err := config.Save(*cfgPath, c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ok(w)
	})
	// /__test/lan?mirrored=1&fail=1&busy=8080&fw=missing&fwfail=1&fwhold=1：模擬 0.9.0 遺留的 mirrored 設定、取消 UAC、埠被佔用、
	// wsld-gui 尚未通過防火牆（missing／blocked）、加入防火牆規則時取消 UAC、UAC 尚未回應（fwhold=0 放行）。
	mux.HandleFunc("POST /__test/lan", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("mirrored") == "1" {
			_ = os.WriteFile(lanConfigPath, []byte("[wsl2]\r\nmemory=4GB\r\nnetworkingMode=mirrored\r\n"), 0o644)
			lanHost.Set(func(h *lan.FakeHost) { h.Rules = 2 })
		}
		lanHost.Set(func(h *lan.FakeHost) {
			switch q.Get("fail") {
			case "1":
				h.FirewallErr = errors.New("The operation was canceled by the user.")
			case "0":
				h.FirewallErr = nil
			}
			if fw := q.Get("fw"); fw != "" {
				h.AppState = lan.FirewallState(fw)
			}
			switch q.Get("fwfail") {
			case "1":
				h.AllowErr = lan.ErrUACCanceled
			case "0":
				h.AllowErr = nil
			}
			switch q.Get("fwhold") {
			case "1":
				if h.AllowHold == nil {
					h.AllowHold = make(chan struct{})
				}
			case "0":
				if h.AllowHold != nil {
					close(h.AllowHold)
					h.AllowHold = nil
				}
			}
		})
		if p, err := strconv.Atoi(q.Get("busy")); err == nil {
			lanMu.Lock()
			lanBusy[p] = true
			lanMu.Unlock()
		}
		ok(w)
	})
	mux.HandleFunc("GET /__test/lan", func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile(lanConfigPath)
		var removes, allows int
		elevated := []lan.Elevation{}
		lanHost.Set(func(h *lan.FakeHost) {
			removes, allows = h.RemoveCalls, h.AllowCalls
			elevated = append(elevated, h.Elevated...)
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"wslconfig": string(b), "firewall_removes": removes, "firewall_allows": allows, "firewall_elevated": elevated,
			"shutdowns": fwsl.Shutdowns(),
		})
	})
	// 自動帶起（Supervise）：預設關閉，讓「引擎斷線」畫面可被觀察；on 時每 200ms 檢查、失敗 300ms 後重試。
	var supMu sync.Mutex
	var supCancel context.CancelFunc
	stopSupervise := func() {
		supMu.Lock()
		defer supMu.Unlock()
		if supCancel != nil {
			supCancel()
			supCancel = nil
		}
	}
	mux.HandleFunc("POST /__test/supervise/{state}", func(w http.ResponseWriter, r *http.Request) {
		stopSupervise()
		if r.PathValue("state") == "on" {
			supMu.Lock()
			ctx, cancel := context.WithCancel(context.Background())
			supCancel = cancel
			supMu.Unlock()
			srv.SuperviseBackoff = 300 * time.Millisecond
			go srv.Supervise(ctx, 200*time.Millisecond)
		}
		ok(w)
	})
	// 新增一個（停止中的）假 distro，例如 Debian1，用來測 Engine／Client 角色。
	mux.HandleFunc("POST /__test/distro", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		if state == "" {
			state = "Stopped"
		}
		fwsl.AddDistro(r.URL.Query().Get("name"), state)
		ok(w)
	})
	// 曝露：?distro=Debian1&on=1 設定該 distro 的曝露掛載（並刷新 Machines 頁用的快取）；
	// ?container=web&src=/mnt/wsl/distro/Debian1/home/u/app 讓該容器帶有 bind 掛載。
	mux.HandleFunc("POST /__test/expose", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if d := q.Get("distro"); d != "" {
			fwsl.SetExposed(d, q.Get("on") == "1")
		}
		if c := q.Get("container"); c != "" {
			engine.SetBinds(c, q.Get("src"))
		}
		srv.RefreshExposure()
		ok(w)
	})
	mux.HandleFunc("POST /__test/reset", func(w http.ResponseWriter, r *http.Request) {
		stopSupervise()
		srv.ResetStart()
		reset()
		resetLAN()
		engine.Reset()
		fwsl.Reset()
		srv.RefreshExposure()
		srv.SelfInWSL = false
		life.Broken = false
		life.ResetStarts()
		_ = auto.Set(false)
		i18n.Set(i18n.Lang(*lang))
		winMu.Lock()
		windowActions = nil
		winMu.Unlock()
		ok(w)
	})
	mux.HandleFunc("GET /__test/window", func(w http.ResponseWriter, r *http.Request) {
		winMu.Lock()
		defer winMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"actions": windowActions, "starts": life.Starts()})
	})
	mux.HandleFunc("GET /wsld-config.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = fmt.Fprint(w, `window.__WSLD__={base:"",token:""};`)
	})
	mux.Handle("/", srv.Handler())

	log.Printf("wsld-gui-testhost on http://%s (dist=%s config=%s)", *addr, *dist, *cfgPath)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func ok(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// spaHandler 服務靜態檔；找不到時回 index.html。
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean(strings.TrimPrefix(r.URL.Path, "/")))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
