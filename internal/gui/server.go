// Package gui 提供 wsld-gui 前端使用的本機 HTTP+WebSocket 橋接（loopback + bearer token）。
// Wails 視窗與 Playwright 測試主機共用同一個 Server，前端只有一條資料路徑。
package gui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/irice/wsl-docker/internal/config"
	"github.com/irice/wsl-docker/internal/dockerapi"
	"github.com/irice/wsl-docker/internal/doctor"
	"github.com/irice/wsl-docker/internal/i18n"
	"github.com/irice/wsl-docker/internal/lifecycle"
	"github.com/irice/wsl-docker/internal/wsl"
)

// Engine 是 GUI 需要的 Docker API 子集（*dockerapi.Client 滿足）。
type Engine interface {
	Ping(ctx context.Context) error
	ListContainers(ctx context.Context, all bool) ([]dockerapi.Container, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeoutSec int) error
	Logs(ctx context.Context, id string, opts dockerapi.LogsOptions) (io.ReadCloser, error)
	Inspect(ctx context.Context, id string) (dockerapi.ContainerDetail, error)
	ExecCreate(ctx context.Context, containerID string, cmd []string, tty bool) (string, error)
	ExecStart(ctx context.Context, execID string, tty bool) (net.Conn, error)
	ExecResize(ctx context.Context, execID string, rows, cols int) error
	Resources
}

// Lifecycle 是 GUI 需要的生命週期子集（*lifecycle.Manager 滿足）。
type Lifecycle interface {
	Status(ctx context.Context) lifecycle.Status
	Start(ctx context.Context) (lifecycle.Status, error)
}

// Autostart 控制「登入時啟動 Tray」。
type Autostart interface {
	Enabled() (bool, error)
	Set(on bool) error
}

// Server 聚合依賴並提供 http.Handler。
type Server struct {
	Engine    Engine
	Life      Lifecycle
	Autostart Autostart
	Version   string
	Token     string // 空 = 不驗證（測試主機）
	// Config 讀取／寫入（GUI 設定頁）。
	LoadConfig func() (config.Config, error)
	SaveConfig func(config.Config) error
	// Doctor 依賴（可為 nil → /api/doctor 回 501）
	WSL       wsl.Runner
	Processes func(ctx context.Context) ([]string, error)
	// Assets 可為 nil；非 nil 時非 /api 路徑交給它（測試主機用來服務 frontend/dist）。
	Assets http.Handler
	// OnWindowAction 讓 Wails 端處理 show/hide/quit（測試主機記錄即可）。
	OnWindowAction func(action string)
	// OnLangChange 於設定頁切換語系後呼叫（Tray 選單文字需重建）。
	OnLangChange func(l i18n.Lang)

	startMu   sync.Mutex
	starting  bool
	lastStart *lifecycle.Status
}

// NewToken 產生隨機 bearer token。
func NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Handler 建立路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/engine/start", s.handleEngineStart)
	mux.HandleFunc("GET /api/containers", s.handleContainers)
	mux.HandleFunc("POST /api/containers/{id}/start", s.handleContainerStart)
	mux.HandleFunc("POST /api/containers/{id}/stop", s.handleContainerStop)
	mux.HandleFunc("GET /api/containers/{id}/logs", s.handleLogs)
	mux.HandleFunc("GET /api/containers/{id}/logs/stream", s.handleLogsStream)
	mux.HandleFunc("GET /api/containers/{id}/exec", s.handleExec)
	mux.HandleFunc("GET /api/images", s.handleImages)
	mux.HandleFunc("DELETE /api/images/{ref}", s.handleImageRemove)
	mux.HandleFunc("POST /api/images/prune", s.handleImagesPrune)
	mux.HandleFunc("GET /api/volumes", s.handleVolumes)
	mux.HandleFunc("DELETE /api/volumes/{name}", s.handleVolumeRemove)
	mux.HandleFunc("POST /api/volumes/prune", s.handleVolumesPrune)
	mux.HandleFunc("GET /api/networks", s.handleNetworks)
	mux.HandleFunc("GET /api/machines", s.handleMachines)
	mux.HandleFunc("GET /api/machines/online", s.handleMachinesOnline)
	mux.HandleFunc("GET /api/machines/{name}/config", s.handleMachineConfig)
	mux.HandleFunc("POST /api/machines/install", s.handleMachineInstall)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/doctor", s.handleDoctor)
	mux.HandleFunc("POST /api/window/{action}", s.handleWindow)
	if s.Assets != nil {
		mux.Handle("/", s.Assets)
	}
	return s.auth(cors(mux))
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" || r.Method == http.MethodOptions || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if tok != s.Token {
			writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("bad token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----------------------------------------------------------------

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"` // not_ready | api | not_found | conflict | error | usage
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, apiError{Error: err.Error(), Code: code})
}

// writeEngineErr 把後端錯誤對應到 HTTP 狀態與錯誤碼（對齊 CLI 退出碼分類）。
func writeEngineErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, lifecycle.ErrNotReady):
		writeErr(w, http.StatusServiceUnavailable, "not_ready", err)
	case errors.Is(err, lifecycle.ErrAPI), errors.Is(err, dockerapi.ErrUnreachable):
		writeErr(w, http.StatusBadGateway, "api", err)
	case dockerapi.IsNotFound(err):
		writeErr(w, http.StatusNotFound, "not_found", err)
	default:
		var ae *dockerapi.APIError
		if errors.As(err, &ae) {
			if ae.Status == http.StatusConflict {
				writeErr(w, http.StatusConflict, "conflict", err)
				return
			}
			writeErr(w, http.StatusBadRequest, "error", err)
			return
		}
		writeErr(w, http.StatusInternalServerError, "error", err)
	}
}

func (s *Server) cfg() config.Config {
	if s.LoadConfig != nil {
		if c, err := s.LoadConfig(); err == nil {
			return c
		}
	}
	return config.Default()
}

// ---- handlers ---------------------------------------------------------------

// Meta 是 /api/meta 回應。
type Meta struct {
	Version            string            `json:"version"`
	Lang               string            `json:"lang"`
	Messages           map[string]string `json:"messages"`
	Config             Settings          `json:"config"`
	Distro             string            `json:"distro"`
	APIEndpoint        string            `json:"api_endpoint"`
	AutostartSupported bool              `json:"autostart_supported"`
}

// Settings 是 GUI 可編輯的設定子集。
type Settings struct {
	RefreshIntervalSec int    `json:"refresh_interval_sec"`
	ExecShell          string `json:"exec_shell"`
	TrayAutostart      bool   `json:"tray_autostart"`
	Lang               string `json:"lang,omitempty"` // en／zh-TW；PUT 時省略＝不變
}

func (s *Server) settingsFrom(c config.Config) Settings {
	st := Settings{RefreshIntervalSec: c.RefreshIntervalSec, ExecShell: c.ExecShell, TrayAutostart: c.TrayAutostart, Lang: c.LangOrDefault()}
	if s.Autostart != nil {
		if on, err := s.Autostart.Enabled(); err == nil {
			st.TrayAutostart = on
		}
	}
	return st
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	c := s.cfg()
	_, unsupported := s.Autostart.(UnsupportedAutostart)
	writeJSON(w, http.StatusOK, Meta{
		Version:            s.Version,
		Lang:               string(i18n.Current()),
		Messages:           i18n.All(i18n.Current()),
		Config:             s.settingsFrom(c),
		Distro:             c.Distro,
		APIEndpoint:        c.APIEndpoint,
		AutostartSupported: s.Autostart != nil && !unsupported,
	})
}

// StatusResponse 包含 lifecycle.Status 與 starting 旗標。
type StatusResponse struct {
	lifecycle.Status
	Ready    bool `json:"ready"`
	Starting bool `json:"starting"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.Life.Status(r.Context())
	s.startMu.Lock()
	starting := s.starting
	s.startMu.Unlock()
	writeJSON(w, http.StatusOK, StatusResponse{Status: st, Ready: st.Ready(), Starting: starting})
}

func (s *Server) handleEngineStart(w http.ResponseWriter, r *http.Request) {
	s.startMu.Lock()
	if s.starting {
		s.startMu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"starting": true})
		return
	}
	s.starting = true
	s.startMu.Unlock()
	defer func() {
		s.startMu.Lock()
		s.starting = false
		s.startMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st, err := s.Life.Start(ctx)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, StatusResponse{Status: st, Ready: st.Ready()})
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true"
	list, err := s.Engine.ListContainers(r.Context(), all)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if list == nil {
		list = []dockerapi.Container{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleContainerStart(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.StartContainer(r.Context(), r.PathValue("id")); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "started"})
}

func (s *Server) handleContainerStop(w http.ResponseWriter, r *http.Request) {
	t, _ := strconv.Atoi(r.URL.Query().Get("t"))
	if t <= 0 {
		t = 10
	}
	if err := s.Engine.StopContainer(r.Context(), r.PathValue("id"), t); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "stopped"})
}

func tailParam(r *http.Request) int {
	tail := 100
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			tail = n
		}
	}
	return tail
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	rc, err := s.Engine.Logs(r.Context(), r.PathValue("id"), dockerapi.LogsOptions{Tail: tailParam(r)})
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.Copy(w, rc)
}

func (s *Server) acceptWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // 已由 token 驗證；Origin 為 wails:// 或 http://127.0.0.1
	})
}

func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	rc, err := s.Engine.Logs(r.Context(), r.PathValue("id"), dockerapi.LogsOptions{Tail: tailParam(r), Follow: true})
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	c, err := s.acceptWS(w, r)
	if err != nil {
		rc.Close()
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		// 讀端關閉即結束
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				rc.Close()
				return
			}
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			if werr := c.Write(ctx, websocket.MessageText, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			_ = c.Close(websocket.StatusNormalClosure, "eof")
			return
		}
	}
}

type execControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := s.cfg()
	var explicit []string
	if v := r.URL.Query().Get("cmd"); v != "" {
		explicit = strings.Fields(v)
	}
	cmd := cfg.ExecCommand(explicit)
	execID, err := s.Engine.ExecCreate(r.Context(), id, cmd, true)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	conn, err := s.Engine.ExecStart(context.Background(), execID, true)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	c, err := s.acceptWS(w, r)
	if err != nil {
		conn.Close()
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols > 0 && rows > 0 {
		_ = s.Engine.ExecResize(ctx, execID, rows, cols)
	}
	// ws → 容器 stdin
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				conn.Close()
				return
			}
			if typ == websocket.MessageText {
				var ctl execControl
				if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" && ctl.Cols > 0 && ctl.Rows > 0 {
					_ = s.Engine.ExecResize(ctx, execID, ctl.Rows, ctl.Cols)
				}
				continue
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}()
	// 容器輸出 → ws
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			_ = c.Close(websocket.StatusNormalClosure, "exec ended")
			return
		}
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsFrom(s.cfg()))
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	c, err := s.LoadConfig()
	if err != nil {
		writeEngineErr(w, fmt.Errorf("%w: %v", lifecycle.ErrNotReady, err))
		return
	}
	if in.RefreshIntervalSec < 1 || in.RefreshIntervalSec > 3600 {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("refresh_interval_sec must be 1..3600"))
		return
	}
	var newLang i18n.Lang
	if in.Lang != "" {
		l, ok := i18n.Parse(in.Lang)
		if !ok {
			writeErr(w, http.StatusBadRequest, "usage", fmt.Errorf("unsupported lang %q (en, zh-TW)", in.Lang))
			return
		}
		newLang = l
		c.Lang = string(l)
	}
	c.RefreshIntervalSec = in.RefreshIntervalSec
	c.ExecShell = strings.TrimSpace(in.ExecShell)
	c.TrayAutostart = in.TrayAutostart
	if s.Autostart != nil {
		if err := s.Autostart.Set(in.TrayAutostart); err != nil && in.TrayAutostart {
			writeErr(w, http.StatusBadRequest, "error", err)
			return
		}
	}
	if err := s.SaveConfig(c); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	if newLang != "" && newLang != i18n.Current() {
		i18n.Set(newLang) // 後端訊息（meta.messages、Tray）一起切換
		if s.OnLangChange != nil {
			s.OnLangChange(newLang)
		}
	}
	writeJSON(w, http.StatusOK, s.settingsFrom(c))
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if s.WSL == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("doctor unavailable"))
		return
	}
	rep := doctor.Run(r.Context(), doctor.Deps{WSL: s.WSL, Cfg: s.cfg(), API: s.Engine, Processes: s.Processes})
	type item struct {
		ID, Title, Level, Detail, Hint string
	}
	items := make([]item, 0, len(rep.Results))
	for _, x := range rep.Results {
		items = append(items, item{x.ID, x.Title, x.Level.String(), x.Detail, x.Hint})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": items, "exit_code": rep.ExitCode()})
}

func (s *Server) handleWindow(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	switch action {
	case "hide", "show", "quit":
	default:
		writeErr(w, http.StatusBadRequest, "usage", errors.New("unknown window action"))
		return
	}
	if s.OnWindowAction != nil {
		s.OnWindowAction(action)
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": action})
}

// ListenLoopback 在 127.0.0.1 隨機埠啟動 server，回傳 base URL 與關閉函式。
func (s *Server) ListenLoopback() (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}
