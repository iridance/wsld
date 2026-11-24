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
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/doctor"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/k8s"
	"github.com/iridance/wsld/internal/lan"
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
	// Files 提供 Import 對話框瀏覽的檔案來源（nil＝預設：Windows 本機與以 WSL 讀取的 distro）。
	Files FileHost
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
	// LAN 提供網卡、0.9.0 mirrored 遺留設定清理與 WSL 重啟時的埠等待；與 Forwarder 皆非 nil 才啟用 /api/lan。
	LAN *lan.Manager
	// Forwarder 在內網 IP 上轉發容器 published 埠（區網存取）。
	Forwarder *lan.Forwarder
	// DistroDisk 回傳 distro 虛擬磁碟（ext4.vhdx）大小（Windows：wsl.VHDXSize；nil＝不顯示）。
	DistroDisk func(name string) (int64, error)
	// DistroDiskLimit 回傳 distro 虛擬磁碟的容量上限（Windows：wsl.VHDXLimit；nil＝不顯示）。
	DistroDiskLimit func(name string) (int64, error)
	// SuperviseBackoff 是自動帶起失敗後的重試間隔（0 = 30 秒；測試縮短）。
	SuperviseBackoff time.Duration
	// KeepAlive 替主要機器掛著常駐 wsl session，避免 WSL 在閒置約 20 秒後回收它（nil＝不保活）。
	KeepAlive *wsl.KeepAlive
	// SelfInWSL：wsld-gui 的執行檔位於 WSL 檔案系統（\\wsl.localhost\…）；此時拒絕 wsl --shutdown。
	SelfInWSL bool
	// K8sFor 取得指定 distro 的 k3s 管理器（nil → /api/k8s 回 501）。
	K8sFor func(distro string) K8s
	// K8sStream 開啟 kubectl logs／exec 串流（nil → 501）。
	K8sStream CommandStream
	// KubeconfigPath 是匯出 kubeconfig 的目標（空 = %USERPROFILE%\.kube\config；測試用）。
	KubeconfigPath string
	// InstallProbe 觀察新增機器（wsl --install）的下載進度（nil → 進度只有經過時間）。
	InstallProbe InstallProbe

	diskBusy atomic.Bool // 調整／壓縮磁碟中：/config 不在 distro 內執行命令（避免把 WSL 叫醒）

	exposeMu     sync.Mutex      // 保護下列曝露狀態
	lastExposeGC time.Time       // 上次曝露 GC 的時間（節流）
	exposed      map[string]bool // 目前曝露中的 distro 快取（Machines 頁用；由 GC 與釋放更新）

	startMu   sync.Mutex
	starting  bool
	startErr  string    // 最近一次自動帶起失敗的原因
	nextTry   time.Time // 失敗後的退避：此時間之前不再自動重試
	lastStart *lifecycle.Status
	engineMu  sync.RWMutex

	lanMu sync.Mutex // 序列化區網轉發同步（背景迴圈與 API 共用）

	k8sMu      sync.Mutex // 保護下列背景作業狀態（同一時間只跑一個 k3s 作業）
	k8sOp      string
	k8sErr     string
	k8sWarn    string
	k8sStep    string                // 啟動作業目前所在步驟（k8s.Step*）
	k8sErrStep string                // 上一次失敗發生在哪個步驟
	k8sLast    map[string]k8s.Status // 各機器最近一次確定的狀態（查詢逾時時沿用）

	fwMu      sync.Mutex // 同一時間只跑一個防火牆 UAC
	fwPending atomic.Bool

	fwCacheMu sync.Mutex // 防火牆狀態快取（背景同步每 3 秒讀一次，規則查詢較慢）
	fwCache   lan.FirewallInfo
	fwCacheAt time.Time

	installOnce sync.Once
	install     *installTracker

	// DiskCacheTTL 是 /api/disk 快取時間（0＝預設 15 秒；負值＝不快取，測試主機用）。
	DiskCacheTTL time.Duration
	diskMu       sync.Mutex
	diskVal      DiskResponse
	diskAt       time.Time
	diskFor      Engine
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
	mux.HandleFunc("GET /api/disk", s.handleDisk)
	mux.HandleFunc("GET /api/machines", s.handleMachines)
	mux.HandleFunc("GET /api/machines/online", s.handleMachinesOnline)
	mux.HandleFunc("GET /api/machines/{name}/config", s.handleMachineConfig)
	mux.HandleFunc("POST /api/machines/install", s.handleMachineInstall)
	mux.HandleFunc("GET /api/machines/install/progress", s.handleInstallProgress)
	mux.HandleFunc("GET /api/machines/install/defaults", s.handleInstallDefaults)
	mux.HandleFunc("GET /api/expose", s.handleExposeReport)
	mux.HandleFunc("POST /api/machines/{name}/expose/release", s.handleExposeRelease)
	mux.HandleFunc("POST /api/machines/{name}/role", s.handleMachineRole)
	mux.HandleFunc("POST /api/machines/{name}/start", s.handleMachineStart)
	mux.HandleFunc("POST /api/machines/{name}/stop", s.handleMachineStop)
	mux.HandleFunc("POST /api/machines/{name}/unmanage", s.handleMachineUnmanage)
	mux.HandleFunc("POST /api/machines/{name}/unregister", s.handleMachineUnregister)
	mux.HandleFunc("POST /api/machines/{name}/resize", s.handleMachineResize)
	mux.HandleFunc("GET /api/machines/{name}/resize-info", s.handleMachineResizeInfo)
	mux.HandleFunc("POST /api/machines/{name}/compact", s.handleMachineCompact)
	mux.HandleFunc("GET /api/machines/{name}/exec", s.handleMachineExec)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/lan", s.handleGetLAN)
	mux.HandleFunc("PUT /api/lan", s.handlePutLAN)
	mux.HandleFunc("POST /api/lan/restore-nat", s.handleRestoreNAT)
	mux.HandleFunc("POST /api/lan/firewall", s.handleAllowFirewall)
	mux.HandleFunc("GET /api/lan/forwards", s.handleLANForwards)
	mux.HandleFunc("POST /api/lan/open", s.handleLANOpen)
	mux.HandleFunc("GET /api/doctor", s.handleDoctor)
	mux.HandleFunc("POST /api/window/{action}", s.handleWindow)
	mux.HandleFunc("POST /api/distro/{action}", s.handleDistro)
	mux.HandleFunc("POST /api/open-repo", s.handleOpenRepo)
	mux.HandleFunc("POST /api/open-local", s.handleOpenLocal)
	mux.HandleFunc("GET /api/k8s", s.handleK8sGet)
	mux.HandleFunc("POST /api/k8s/on", s.handleK8sOn)
	mux.HandleFunc("POST /api/k8s/off", s.handleK8sOff)
	mux.HandleFunc("POST /api/k8s/reset", s.handleK8sReset)
	mux.HandleFunc("POST /api/k8s/kubeconfig", s.handleK8sKubeconfig)
	mux.HandleFunc("POST /api/k8s/open", s.handleK8sOpen)
	mux.HandleFunc("POST /api/k8s/deploy", s.handleK8sDeploy)
	mux.HandleFunc("POST /api/k8s/expose", s.handleK8sExpose)
	mux.HandleFunc("POST /api/k8s/apply", s.handleK8sApply)
	mux.HandleFunc("POST /api/k8s/stack", s.handleK8sStack)
	mux.HandleFunc("POST /api/k8s/compose/preview", s.handleComposePreview)
	mux.HandleFunc("GET /api/fs/sources", s.handleFSSources)
	mux.HandleFunc("GET /api/fs/list", s.handleFSList)
	mux.HandleFunc("DELETE /api/k8s/stack/{ns}/{name}", s.handleK8sStackDelete)
	mux.HandleFunc("GET /api/k8s/namespaces", s.handleK8sNamespaces)
	mux.HandleFunc("GET /api/k8s/pods", s.handleK8sPods)
	mux.HandleFunc("GET /api/k8s/deployments", s.handleK8sDeployments)
	mux.HandleFunc("GET /api/k8s/services", s.handleK8sServices)
	mux.HandleFunc("GET /api/k8s/extras", s.handleK8sExtras)
	mux.HandleFunc("GET /api/k8s/deployments/{ns}/{name}/detail", s.handleK8sDeploymentDetail)
	mux.HandleFunc("GET /api/k8s/pods/{ns}/{name}/logs/stream", s.handleK8sLogsStream)
	mux.HandleFunc("GET /api/k8s/pods/{ns}/{name}/exec", s.handleK8sExec)
	mux.HandleFunc("POST /api/k8s/deployments/{ns}/{name}/restart", s.handleK8sRestart)
	mux.HandleFunc("POST /api/k8s/deployments/{ns}/{name}/scale", s.handleK8sScale)
	mux.HandleFunc("DELETE /api/k8s/{kind}/{ns}/{name}", s.handleK8sDelete)
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
	writeErr(w, engineErrStatus(err), engineErrCode(err), err)
}

func engineErrStatus(err error) int {
	switch {
	case errors.Is(err, lifecycle.ErrNotReady):
		return http.StatusServiceUnavailable
	case errors.Is(err, lifecycle.ErrAPI), errors.Is(err, dockerapi.ErrUnreachable):
		return http.StatusBadGateway
	case dockerapi.IsNotFound(err):
		return http.StatusNotFound
	}
	var ae *dockerapi.APIError
	if errors.As(err, &ae) {
		if ae.Status == http.StatusConflict || ae.Status == http.StatusForbidden {
			return http.StatusConflict
		}
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func engineErrCode(err error) string {
	switch {
	case errors.Is(err, lifecycle.ErrNotReady):
		return "not_ready"
	case errors.Is(err, lifecycle.ErrAPI), errors.Is(err, dockerapi.ErrUnreachable):
		return "api"
	case dockerapi.IsNotFound(err):
		return "not_found"
	}
	var ae *dockerapi.APIError
	if errors.As(err, &ae) && (ae.Status == http.StatusConflict || ae.Status == http.StatusForbidden) {
		return "conflict"
	}
	return "error"
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
	LANSupported       bool              `json:"lan_supported"`
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
		LANSupported:       s.LAN != nil && s.Forwarder != nil,
	})
}

// StatusResponse 包含 lifecycle.Status 與 starting 旗標。
type StatusResponse struct {
	lifecycle.Status
	Ready    bool `json:"ready"`
	Starting bool `json:"starting"`
	// StartError 是最近一次自動帶起 engine 失敗的原因（成功後清空）；畫面據此顯示錯誤，使用者不需要手動啟動。
	StartError string `json:"start_error,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.life().Status(r.Context())
	s.startMu.Lock()
	starting, startErr := s.starting, s.startErr
	s.startMu.Unlock()
	writeJSON(w, http.StatusOK, StatusResponse{Status: st, Ready: st.Ready(), Starting: starting, StartError: startErr})
}

// engineStartOpt 帶起 engine，成功後背景恢復 k3s。已有啟動進行中回 busy。
// reuse 為 true 時，若 engine 已在跑（關閉 app 不會停掉它，只放掉保活 session）
// 就只補掛保活並在背景確認 k3s，不走完整啟動流程。檢查在 starting 旗標內進行，畫面不會空窗。
func (s *Server) engineStartOpt(reuse bool) (st lifecycle.Status, err error, busy bool) {
	s.startMu.Lock()
	if s.starting {
		s.startMu.Unlock()
		return st, nil, true
	}
	s.starting = true
	s.startMu.Unlock()
	defer func() {
		s.startMu.Lock()
		s.starting = false
		if err != nil {
			s.startErr = err.Error()
		} else {
			s.startErr = ""
		}
		s.startMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if reuse {
		if cur := s.life().Status(ctx); cur.Ready() {
			s.holdPrimary()
			go s.resumeK8sIfDown()
			return cur, nil, false
		}
	}
	st, err = s.bringUpActive(ctx)
	if err != nil {
		return st, err, false
	}
	s.resumeK8s()
	return st, nil, false
}

// AutoStart：程式啟動（開機自啟 --tray 或手動開啟）時自動帶起 engine；全新安裝（還沒有 engine）則先建立 wsld-engine。
// 沒有「啟動引擎」按鈕：啟動 wsld 就是啟動引擎。同步執行直到完成；失敗記在 start_error 並由 Supervise 退避重試。
func (s *Server) AutoStart() error {
	_, err, _ := s.engineStartOpt(true)
	return err
}

const defaultSuperviseBackoff = 30 * time.Second

// ResetStart 清掉自動帶起的錯誤與退避（testhost 的 /__test/reset 用）。
func (s *Server) ResetStart() {
	s.startMu.Lock()
	s.startErr, s.nextTry = "", time.Time{}
	s.startMu.Unlock()
}

// Supervise 定期檢查 engine，沒起來就自動帶起（WSL 被關掉、engine 當掉、剛登入等）。
// 使用者在 Machines 頁明確停止的 engine 不會被拉起；失敗後退避 30 秒再試。
func (s *Server) Supervise(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.superviseOnce(now)
		}
	}
}

func (s *Server) superviseOnce(now time.Time) {
	s.startMu.Lock()
	busy, wait := s.starting, now.Before(s.nextTry)
	s.startMu.Unlock()
	if busy || wait {
		return
	}
	for _, name := range keepAliveNames(s.cfg()) {
		if s.KeepAlive.Paused(name) {
			return
		}
	}
	if s.life().Status(context.Background()).Ready() {
		s.gcExposure(now)
		return
	}
	if _, err, _ := s.engineStartOpt(true); err != nil {
		s.startMu.Lock()
		backoff := s.SuperviseBackoff
		if backoff <= 0 {
			backoff = defaultSuperviseBackoff
		}
		s.nextTry = now.Add(backoff)
		s.startMu.Unlock()
	}
}

// bringUpActive：必要時 Init／補齊 profile，再 Start 引擎並寫回設定。
func (s *Server) bringUpActive(ctx context.Context) (lifecycle.Status, error) {
	if err := s.ensureActiveInitialized(ctx, false); err != nil {
		return lifecycle.Status{}, err
	}
	st, err := s.life().Start(ctx)
	if errors.Is(err, lifecycle.ErrAPI) {
		// docker 已經起來卻一直連不上（埠被佔／endpoint 錯）：重跑 Init 選可用埠再試一次
		if rerr := s.ensureActiveInitialized(ctx, true); rerr != nil {
			return st, rerr
		}
		st, err = s.life().Start(ctx)
	}
	if err != nil {
		return st, err
	}
	s.holdPrimary()
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
// force 為 true 時無論如何重跑 Init（Start 之後 API 仍打不通：重選可用埠）；否則只在 profile 尚未 init 時做。
// 引擎只是停著（冷啟動）時不重跑：Init 會重啟 dockerd，Start 喚醒並等 API 就夠了。
func (s *Server) ensureActiveInitialized(ctx context.Context, force bool) error {
	cfg := s.cfg()
	// 沒有 engine，或遷移舊設定留下的尚未 init 的 engine 占位（真實環境要先建立 distro）：走 EnsureEngine。
	eng, has := cfg.EngineMachine()
	_, isManager := s.life().(*lifecycle.Manager)
	if !has || (!eng.Ready() && isManager) {
		if err := s.createEngine(ctx, &cfg); err != nil {
			return err
		}
	}
	am, ok := cfg.ActiveMachine()
	if !ok {
		return fmt.Errorf("%w: %s", lifecycle.ErrNotReady, i18n.T("cli.no_active"))
	}
	needInit := !am.Ready() || force
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
		newCfg, err := mgr.Init(ctx, lifecycle.InitOptions{Distro: am.Distro, Role: am.Role})
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

// createEngine 首次使用：建立並 init 專用 engine（wsld-engine），寫回設定。
func (s *Server) createEngine(ctx context.Context, cfg *config.Config) error {
	if s.SaveConfig == nil {
		return errors.New("config not writable")
	}
	if _, ok := s.life().(ConfigMissingLifecycle); ok {
		s.BindActive(*cfg)
	}
	switch life := s.life().(type) {
	case *FakeLifecycle:
		name := life.Distro
		if name == "" {
			name = lifecycle.DefaultEngineDistro
		}
		cfg.UpsertMachine(config.Machine{Distro: name, User: "testuser", APIEndpoint: nextMachineEndpoint(*cfg)})
		if err := cfg.SetEngine(name); err != nil {
			return err
		}
		_ = cfg.SetActive(name)
	case *lifecycle.Manager:
		life.Cfg = *cfg
		got, err := life.EnsureEngine(ctx, lifecycle.InitOptions{})
		if err != nil {
			return err
		}
		*cfg = got
	default:
		return fmt.Errorf("%w: %s", lifecycle.ErrNotReady, i18n.T("cli.no_active"))
	}
	if err := s.SaveConfig(*cfg); err != nil {
		return err
	}
	s.BindActive(*cfg)
	return nil
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
	if s.ensureExposedForStart(w, r, r.PathValue("id")) {
		return
	}
	if err := s.engine().StartContainer(r.Context(), r.PathValue("id")); err != nil {
		writeEngineErr(w, err)
		return
	}
	s.invalidateDisk()
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
	s.invalidateDisk()
	writeJSON(w, http.StatusOK, map[string]string{"result": "stopped"})
}

func (s *Server) handleContainerRemove(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	if err := s.engine().RemoveContainer(r.Context(), r.PathValue("id"), force); err != nil {
		writeEngineErr(w, err)
		return
	}
	s.invalidateDisk()
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
	deps := doctor.Deps{WSL: s.WSL, Cfg: s.cfg(), API: s.engine(), Processes: s.Processes}
	if s.LAN != nil {
		deps.LAN = s.LAN.Legacy
	}
	rep := doctor.Run(r.Context(), deps)
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

// LocalURL 是容器 published 埠在本機的網址（WSL localhost 轉發）。
func LocalURL(port int) string { return "http://localhost:" + strconv.Itoa(port) }

// handleOpenLocal 以系統瀏覽器開啟 http://localhost:<埠>；只接受執行中容器已發佈的 TCP 埠。
// 區網存取關閉（或該容器沒有區網轉發）時，容器列的瀏覽器按鈕走這裡。
func (s *Server) handleOpenLocal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Port int `json:"port"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	list, err := s.engine().ListContainers(r.Context(), false)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	allowed := false
	for _, c := range list {
		if c.State != "running" {
			continue
		}
		for _, p := range c.Ports {
			if in.Port > 0 && p.PublicPort == in.Port && (p.Type == "" || p.Type == "tcp") {
				allowed = true
			}
		}
	}
	if !allowed {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("not a published container port"))
		return
	}
	url := LocalURL(in.Port)
	if s.OnOpenURL != nil {
		if err := s.OnOpenURL(url); err != nil {
			writeErr(w, http.StatusInternalServerError, "error", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
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

// keepAliveNames 是需要常駐保活的 distro：專用 engine（client 要靠它，不能被 WSL 閒置回收）。
// 尚未有 engine 的過渡設定（舊檔未遷移、測試）退回 active。
func keepAliveNames(cfg config.Config) []string {
	if e, ok := cfg.EngineMachine(); ok {
		return []string{e.Distro}
	}
	if cfg.Active != "" {
		return []string{cfg.Active}
	}
	return nil
}

// shouldKeepAlive 表示 name 屬於需保活的 distro。
func shouldKeepAlive(cfg config.Config, name string) bool {
	for _, n := range keepAliveNames(cfg) {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// holdPrimary 讓主要機器與 engine 保持執行：換手（放掉其他機器）並掛上保活 session。best-effort。
func (s *Server) holdPrimary() {
	if s.KeepAlive == nil {
		return
	}
	_ = s.KeepAlive.HoldOnly(keepAliveNames(s.cfg())...)
}

// ensurePrimaryHeld 在主要機器／engine 已執行但沒被保活時補掛（GUI 啟動、WSL 重啟後使用者開了終端等）。
// 使用者明確停止過的機器不會自動重掛。
func (s *Server) ensurePrimaryHeld(list []wsl.Distro) {
	if s.KeepAlive == nil {
		return
	}
	for _, name := range keepAliveNames(s.cfg()) {
		if d, ok := wsl.FindDistro(list, name); ok && strings.EqualFold(d.State, "Running") {
			_ = s.KeepAlive.Ensure(d.Name)
		}
	}
}

// ReleaseKeepAlive 放掉全部保活 session（wsld-gui 結束時呼叫）。
func (s *Server) ReleaseKeepAlive() { s.KeepAlive.ReleaseAll() }
