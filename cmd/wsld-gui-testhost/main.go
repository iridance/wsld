// wsld-gui-testhost：以 FakeEngine／FakeLifecycle 服務 GUI 前端，供 Playwright 測試與前端開發。
// 額外提供 /__test/* 端點控制場景（引擎斷線、推送日誌、切換語系…）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/irice/wsl-docker/internal/config"
	"github.com/irice/wsl-docker/internal/gui"
	"github.com/irice/wsl-docker/internal/i18n"
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
	reset := func() {
		c := config.Default()
		c.Distro, c.User = "Ubuntu-26.04", "irice"
		if err := config.Save(*cfgPath, c); err != nil {
			log.Fatal(err)
		}
	}
	reset()

	var winMu sync.Mutex
	var windowActions []string

	srv := &gui.Server{
		Engine: engine, Life: life, Autostart: auto, Version: "testhost",
		LoadConfig: func() (config.Config, error) { return config.Load(*cfgPath) },
		SaveConfig: func(c config.Config) error { return config.Save(*cfgPath, c) },
		Assets:     spaHandler(*dist),
		OnWindowAction: func(a string) {
			winMu.Lock()
			windowActions = append(windowActions, a)
			winMu.Unlock()
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /__test/engine/{state}", func(w http.ResponseWriter, r *http.Request) {
		engine.SetDown(r.PathValue("state") == "down")
		ok(w)
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
		engine.AddContainer(q.Get("name"), q.Get("image"), state, nil)
		ok(w)
	})
	mux.HandleFunc("POST /__test/lang/{lang}", func(w http.ResponseWriter, r *http.Request) {
		i18n.Set(i18n.Lang(r.PathValue("lang")))
		ok(w)
	})
	mux.HandleFunc("POST /__test/reset", func(w http.ResponseWriter, r *http.Request) {
		reset()
		engine.Reset()
		life.Broken = false
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
