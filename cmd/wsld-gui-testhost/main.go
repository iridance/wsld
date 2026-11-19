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
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/config"
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
		c.Distro, c.User = "Ubuntu-26.04", "irice"
		if err := config.Save(*cfgPath, c); err != nil {
			log.Fatal(err)
		}
	}
	reset()

	// 區網存取：.wslconfig 寫在暫存目錄；wsl --shutdown 時把設定模式「生效」。
	lanDir, err := os.MkdirTemp("", "wsld-testhost-lan")
	if err != nil {
		log.Fatal(err)
	}
	lanHost := &lan.FakeHost{Dir: lanDir, WinBuild: 26300}
	var lanMu sync.Mutex
	lanEffective := lan.ModeNAT
	lanMgr := &lan.Manager{Host: lanHost, Mode: func(context.Context, string) (string, error) {
		lanMu.Lock()
		defer lanMu.Unlock()
		return lanEffective, nil
	}}
	readLANConfig := func() string {
		p, _ := lanHost.ConfigPath()
		b, _ := os.ReadFile(p)
		return string(b)
	}
	fwsl.OnShutdown = func() {
		mode := strings.ToLower(lan.GetKey(readLANConfig(), "wsl2", "networkingMode"))
		if mode == "" {
			mode = lan.ModeNAT
		}
		lanMu.Lock()
		lanEffective = mode
		lanMu.Unlock()
	}
	resetLAN := func() {
		p, _ := lanHost.ConfigPath()
		_ = os.Remove(p)
		_ = os.Remove(p + ".wsld.bak")
		lanHost.Set(func(h *lan.FakeHost) {
			h.WinBuild, h.Rules, h.NetProfiles, h.FirewallErr, h.FirewallCalls = 26300, 0, nil, nil, nil
		})
		lanMu.Lock()
		lanEffective = lan.ModeNAT
		lanMu.Unlock()
	}

	var winMu sync.Mutex
	var windowActions []string

	srv := &gui.Server{
		Engine: engine, Life: life, Autostart: auto, Version: "testhost", WSL: fwsl,
		LAN:        lanMgr,
		Shell:      gui.FakeDistroShell{},
		LoadConfig: func() (config.Config, error) { return config.Load(*cfgPath) },
		SaveConfig: func(c config.Config) error { return config.Save(*cfgPath, c) },
		Assets:     spaHandler(*dist),
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
			engine.AddContainer(q.Get("name"), q.Get("image"), state, nil)
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
	// /__test/lan?build=19045&profile=Public&fail=1：調整假的 Windows 環境。
	mux.HandleFunc("POST /__test/lan", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		lanHost.Set(func(h *lan.FakeHost) {
			if b, err := strconv.Atoi(q.Get("build")); err == nil {
				h.WinBuild = uint32(b)
			}
			if p := q.Get("profile"); p != "" {
				h.NetProfiles = []string{p}
			}
			if q.Get("fail") == "1" {
				h.FirewallErr = errors.New("The operation was canceled by the user.")
			} else if q.Get("fail") == "0" {
				h.FirewallErr = nil
			}
		})
		ok(w)
	})
	mux.HandleFunc("GET /__test/lan", func(w http.ResponseWriter, r *http.Request) {
		var calls []bool
		lanHost.Set(func(h *lan.FakeHost) { calls = append([]bool{}, h.FirewallCalls...) })
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"wslconfig": readLANConfig(), "firewall_calls": calls, "shutdowns": fwsl.Shutdowns(),
		})
	})
	mux.HandleFunc("POST /__test/reset", func(w http.ResponseWriter, r *http.Request) {
		reset()
		resetLAN()
		engine.Reset()
		fwsl.Reset()
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
