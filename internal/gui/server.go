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

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/doctor"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lifecycle"
	"github.com/iridance/wsld/internal/wsl"
)

// Engine 是 GUI 需要的 Docker API 子集（*dockerapi.Client 滿足）。
type Engine interface {
	Ping(ctx context.Context) error
	ListContainers(ctx context.Context, all bool) ([]dockerapi.Container, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeoutSec int) error
	RemoveContainer(ctx context.Context, id string, force bool) error
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
	// OnDistroAction 開啟 distro 資料夾／終端（測試主機記錄即可）；回傳錯誤會轉成 500。
	OnDistroAction func(action string) error
	// OnOpenURL 以系統瀏覽器開啟 URL（例如 footer GitHub）；nil 則僅回成功（測試主機）。
	OnOpenURL func(url string) error
	// Shell 開啟 Machines 列表內嵌 terminal（nil → 501）。
	Shell DistroShell
	// OnLangChange 於設定頁切換語系後呼叫（Tray 選單文字需重建）。
	OnLangChange func(l i18n.Lang)
	// NewEngine 於切換 active 時建立新 Docker client（nil＝測試用 FakeEngine 不換）。
	NewEngine func(cfg config.Config) Engine
	// NewLifecycle 於 config 從無到有時，把 ConfigMissingLifecycle 升級成真正的 Lifecycle。
	NewLifecycle func(cfg config.Config) Lifecycle
	// OnActiveChange 於 active 變更後額外通知（例如 Tray 重繪）。
	OnActiveChange func(cfg config.Config)
	// InstallProbe 觀察新增機器（wsl --install）的下載進度（nil → 進度只有經過時間）。
	InstallProbe InstallProbe

	startMu   sync.Mutex
	starting  bool
	lastStart *lifecycle.Status
	engineMu  sync.RWMutex

	installOnce sync.Once
	install     *installTracker
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
	mux.HandleFunc("DELETE /api/containers/{id}", s.handleContainerRemove)
	mux.HandleFunc("GET /api/containers/{id}", s.handleContainerInspect)
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
	mux.HandleFunc("DELETE /api/networks/{id}", s.handleNetworkRemove)
	mux.HandleFunc("POST /api/networks/prune", s.handleNetworksPrune)
	mux.HandleFunc("GET /api/machines", s.handleMachines)
	mux.HandleFunc("GET /api/machines/online", s.handleMachinesOnline)
	mux.HandleFunc("GET /api/machines/{name}/config", s.handleMachineConfig)
	mux.HandleFunc("POST /api/machines/install", s.handleMachineInstall)
	mux.HandleFunc("GET /api/machines/install/progress", s.handleInstallProgress)
	mux.HandleFunc("GET /api/machines/install/defaults", s.handleInstallDefaults)
	mux.HandleFunc("POST /api/machines/{name}/use", s.handleMachineUse)
	mux.HandleFunc("POST /api/machines/{name}/unmanage", s.handleMachineUnmanage)
	mux.HandleFunc("POST /api/machines/{name}/unregister", s.handleMachineUnregister)
	mux.HandleFunc("GET /api/machines/{name}/exec", s.handleMachineExec)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/doctor", s.handleDoctor)
	mux.HandleFunc("POST /api/window/{action}", s.handleWindow)
	mux.HandleFunc("POST /api/distro/{action}", s.handleDistro)
	mux.HandleFunc("POST /api/open-repo", s.handleOpenRepo)
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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
			if ae.Status == http.StatusConflict || ae.Status == http.StatusForbidden {
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

// BindActive 切換 active 後重綁 Engine／Lifecycle（不停舊引擎）。
func (s *Server) BindActive(cfg config.Config) {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	if s.NewEngine != nil {
		s.Engine = s.NewEngine(cfg)
	}
	if _, ok := s.Life.(ConfigMissingLifecycle); ok && s.NewLifecycle != nil {
		s.Life = s.NewLifecycle(cfg)
	}
	if fl, ok := s.Life.(*FakeLifecycle); ok {
		fl.mu.Lock()
		fl.Distro = cfg.Distro
		fl.Endpoint = cfg.APIEndpoint
		fl.mu.Unlock()
		// 占位 profile：API 尚未就緒（對齊真實 NewEngine ping 失敗）
		if am, ok := cfg.ActiveMachine(); ok && !am.Ready() && fl.Engine != nil {
			fl.Engine.mu.Lock()
			fl.Engine.Down = true
			fl.Engine.mu.Unlock()
		}
	}
	if mgr, ok := s.Life.(*lifecycle.Manager); ok {
		mgr.Cfg = cfg
		if p, ok := s.Engine.(lifecycle.Pinger); ok {
			mgr.API = p
		}
	}
	if s.OnActiveChange != nil {
		s.OnActiveChange(cfg)
	}
}

func (s *Server) engine() Engine {
	s.engineMu.RLock()
	defer s.engineMu.RUnlock()
	return s.Engine
}

func (s *Server) life() Lifecycle {
	s.engineMu.RLock()
	defer s.engineMu.RUnlock()
	return s.Life
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
	ActiveReady        bool              `json:"active_ready"`
	AutostartSupported bool              `json:"autostart_supported"`
}

// Settings 是 GUI 可編輯的設定子集。
type Settings struct {
	RefreshIntervalSec int    `json:"refresh_interval_sec"`
	ExecShell          string `json:"exec_shell"`
	TrayAutostart      bool   `json:"tray_autostart"`
	Lang               string `json:"lang,omitempty"`
	Theme              string `json:"theme,omitempty"` // light | dark | system
}

func (s *Server) settingsFrom(c config.Config) Settings {
	st := Settings{
		RefreshIntervalSec: c.RefreshIntervalSec,
		ExecShell:          c.ExecShell,
		TrayAutostart:      c.TrayAutostart,
		Lang:               c.LangOrDefault(),
		Theme:              c.ThemeOrDefault(),
	}
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
	ready := false
	if m, ok := c.ActiveMachine(); ok {
		ready = m.Ready()
	}
	writeJSON(w, http.StatusOK, Meta{
		Version:            s.Version,
		Lang:               string(i18n.Current()),
		Messages:           i18n.All(i18n.Current()),
		Config:             s.settingsFrom(c),
		Distro:             c.Distro,
		APIEndpoint:        c.APIEndpoint,
		ActiveReady:        ready,
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
	st := s.life().Status(r.Context())
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
	st, err := s.bringUpActive(ctx)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, StatusResponse{Status: st, Ready: st.Ready()})
}

// bringUpActive：必要時 Init／補齊 profile，再 Start 引擎並寫回設定。
func (s *Server) bringUpActive(ctx context.Context) (lifecycle.Status, error) {
	if err := s.ensureActiveInitialized(ctx); err != nil {
		return lifecycle.Status{}, err
	}
	st, err := s.life().Start(ctx)
	if err != nil {
		return st, err
	}
	if mgr, ok := s.life().(*lifecycle.Manager); ok {
		if s.SaveConfig != nil {
			if err := s.SaveConfig(mgr.Cfg); err != nil {
				return st, err
			}
		}
		s.BindActive(mgr.Cfg)
	}
	return st, nil
}

// ensureActiveInitialized：補齊 active profile（Init），並在 API 不通時重選可用 endpoint。
func (s *Server) ensureActiveInitialized(ctx context.Context) error {
	cfg := s.cfg()
	am, ok := cfg.ActiveMachine()
	if !ok {
		return fmt.Errorf("%w: %s", lifecycle.ErrNotReady, i18n.T("cli.no_active"))
	}
	needInit := !am.Ready()
	if !needInit && s.engine() != nil {
		// 已有 endpoint 但打不通（埠被佔／連錯引擎）→ 重跑 Init 選可用埠
		if err := s.engine().Ping(ctx); err != nil {
			needInit = true
		}
	}
	if !needInit {
		return nil
	}
	// 無 config 開 GUI 時 Life 可能仍是 ConfigMissingLifecycle；先升級再 Init。
	if _, ok := s.life().(ConfigMissingLifecycle); ok {
		s.BindActive(cfg)
	}
	if fl, ok := s.life().(*FakeLifecycle); ok {
		ep := nextMachineEndpoint(cfg)
		cfg.EnsureActive(config.Machine{Distro: am.Distro, User: "testuser", APIEndpoint: ep})
		if s.SaveConfig == nil {
			return errors.New("config not writable")
		}
		if err := s.SaveConfig(cfg); err != nil {
			return err
		}
		s.BindActive(cfg)
		_ = fl
		return nil
	}
	if mgr, ok := s.life().(*lifecycle.Manager); ok {
		mgr.Cfg = cfg
		newCfg, err := mgr.Init(ctx, lifecycle.InitOptions{Distro: am.Distro})
		if err != nil {
			return err
		}
		if s.SaveConfig == nil {
			return errors.New("config not writable")
		}
		if err := s.SaveConfig(newCfg); err != nil {
			return err
		}
		s.BindActive(newCfg)
		return nil
	}
	return fmt.Errorf("%w: %s", lifecycle.ErrNotReady, i18n.T("cli.active_not_ready", am.Distro, am.Distro))
}

// nextMachineEndpoint 選一個尚未被其他 machine 占用的 loopback 埠（測試／占位補齊用）。
func nextMachineEndpoint(cfg config.Config) string {
	return config.EndpointForPort(cfg.NextAPIPort(""))
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true"
	list, err := s.engine().ListContainers(r.Context(), all)
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
	if err := s.engine().StartContainer(r.Context(), r.PathValue("id")); err != nil {
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
	if err := s.engine().StopContainer(r.Context(), r.PathValue("id"), t); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "stopped"})
}

func (s *Server) handleContainerRemove(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	if err := s.engine().RemoveContainer(r.Context(), r.PathValue("id"), force); err != nil {
		writeEngineErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ContainerInfo 是 Info drawer 用的摘要。
type ContainerInfo struct {
	Name          string            `json:"name"`
	ID            string            `json:"id"`
	Image         string            `json:"image"`
	State         string            `json:"state"`
	Created       string            `json:"created"`
	Started       string            `json:"started"`
	Command       string            `json:"command"`
	Ports         string            `json:"ports"`
	Networks      []NetworkInfo     `json:"networks"`
	Mounts        []MountInfo       `json:"mounts"`
	Labels        map[string]string `json:"labels"`
	RestartPolicy string            `json:"restart_policy"`
}

// NetworkInfo 含網路名與 IP。
type NetworkInfo struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// MountInfo 是掛載摘要。
type MountInfo struct {
	Source string `json:"source"`
	Dest   string `json:"dest"`
	Mode   string `json:"mode"`
}

func (s *Server) handleContainerInspect(w http.ResponseWriter, r *http.Request) {
	d, err := s.engine().Inspect(r.Context(), r.PathValue("id"))
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, containerInfoFromDetail(d))
}

func containerInfoFromDetail(d dockerapi.ContainerDetail) ContainerInfo {
	name := strings.TrimPrefix(d.Name, "/")
	cmd := strings.Join(d.Config.Cmd, " ")
	if cmd == "" && d.Path != "" {
		cmd = strings.TrimSpace(d.Path + " " + strings.Join(d.Args, " "))
	}
	ports := formatInspectPorts(d.NetworkSettings.Ports)
	nets := make([]NetworkInfo, 0, len(d.NetworkSettings.Networks))
	for n, ep := range d.NetworkSettings.Networks {
		nets = append(nets, NetworkInfo{Name: n, IP: ep.IPAddress})
	}
	mounts := make([]MountInfo, 0, len(d.Mounts))
	for _, m := range d.Mounts {
		mounts = append(mounts, MountInfo{Source: m.Source, Dest: m.Destination, Mode: m.Mode})
	}
	rp := d.HostConfig.RestartPolicy.Name
	if rp == "" {
		rp = "no"
	}
	return ContainerInfo{
		Name: name, ID: d.ID, Image: d.Config.Image, State: d.State.Status,
		Created: d.Created, Started: d.State.StartedAt,
		Command: cmd, Ports: ports, Networks: nets, Mounts: mounts,
		Labels: d.Config.Labels, RestartPolicy: rp,
	}
}

func formatInspectPorts(ports map[string][]dockerapi.PortBinding) string {
	if len(ports) == 0 {
		return ""
	}
	var parts []string
	for priv, binds := range ports {
		if len(binds) == 0 {
			parts = append(parts, priv)
			continue
		}
		for _, b := range binds {
			parts = append(parts, b.HostPort+"→"+priv)
		}
	}
	return strings.Join(parts, ", ")
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
	rc, err := s.engine().Logs(r.Context(), r.PathValue("id"), dockerapi.LogsOptions{Tail: tailParam(r)})
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
	rc, err := s.engine().Logs(r.Context(), r.PathValue("id"), dockerapi.LogsOptions{Tail: tailParam(r), Follow: true})
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
	execID, err := s.engine().ExecCreate(r.Context(), id, cmd, true)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	conn, err := s.engine().ExecStart(context.Background(), execID, true)
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
		_ = s.engine().ExecResize(ctx, execID, rows, cols)
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
					_ = s.engine().ExecResize(ctx, execID, ctl.Rows, ctl.Cols)
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

func (s *Server) handleMachineExec(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !wsl.ValidDistroName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid distro name"))
		return
	}
	if s.Shell == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("distro shell unavailable"))
		return
	}
	user := ""
	if m, ok := s.cfg().FindMachine(name); ok {
		user = m.User
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	conn, err := s.Shell.Open(r.Context(), name, user, cols, rows)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
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
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				conn.Close()
				return
			}
			if typ == websocket.MessageText {
				continue // resize 對 pipe shell 無作用
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}()
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
	if in.Theme != "" {
		switch in.Theme {
		case "light", "dark", "system":
			c.Theme = in.Theme
		default:
			writeErr(w, http.StatusBadRequest, "usage", fmt.Errorf("theme must be light, dark, or system"))
			return
		}
	}
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
	rep := doctor.Run(r.Context(), doctor.Deps{WSL: s.WSL, Cfg: s.cfg(), API: s.engine(), Processes: s.Processes})
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

func (s *Server) handleDistro(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	switch action {
	case "open-folder", "open-terminal":
	default:
		writeErr(w, http.StatusBadRequest, "usage", errors.New("unknown distro action"))
		return
	}
	if s.cfg().Distro == "" {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("distro is not configured"))
		return
	}
	if s.OnDistroAction == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("distro actions unavailable"))
		return
	}
	if err := s.OnDistroAction(action); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": action})
}

// RepoURL 是 footer GitHub 連結的固定目標。
const RepoURL = "https://github.com/iridance/wsld"

func (s *Server) handleOpenRepo(w http.ResponseWriter, r *http.Request) {
	if s.OnOpenURL != nil {
		if err := s.OnOpenURL(RepoURL); err != nil {
			writeErr(w, http.StatusInternalServerError, "error", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": RepoURL})
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
