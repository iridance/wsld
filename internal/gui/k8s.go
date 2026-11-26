package gui

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/k8s"
	"github.com/iridance/wsld/internal/wsl"
)

// K8sOps 是 GUI 需要的 k8s 操作（*k8s.Manager 滿足）。
type K8sOps interface {
	Status(ctx context.Context) k8s.Status
	Enable(ctx context.Context) ([]byte, error)
	Start(ctx context.Context) ([]byte, error)
	Restart(ctx context.Context) ([]byte, error)
	Reset(ctx context.Context) ([]byte, error)
	Disable(ctx context.Context) error
	ListPods(ctx context.Context) ([]k8s.Pod, error)
	DeletePod(ctx context.Context, ns, name string) error
	ListServices(ctx context.Context) ([]k8s.Service, error)
	ListPodStats(ctx context.Context) ([]k8s.PodStats, error)
	Forward(ctx context.Context, req k8s.ForwardRequest) (k8s.PortForward, error)
	ListForwards(ctx context.Context) ([]k8s.PortForward, error)
	StopForward(ctx context.Context, localPort int) error
	Apply(ctx context.Context, manifest []byte) (string, error)
	DeployStack(ctx context.Context, spec k8s.StackSpec) (k8s.StackResult, error)
	DeleteStack(ctx context.Context, ns, name string, withPVC bool) error
}

// K8sOptions 是建立 K8sOps 時由 Server 提供的回呼。
type K8sOptions struct {
	Progress func(step string)
	// Install 建立 distro（GUI 包上下載進度追蹤）；nil = 直接 wsl --install。
	Install func(ctx context.Context, base, name string, vhdSizeGB int) error
}

// k8s 背景作業。
const (
	k8sOpEnabling   = "enabling"
	k8sOpDisabling  = "disabling"
	k8sOpStarting   = "starting" // 已開啟的 k8s 自動帶起（程式啟動、WSL 重啟後）
	k8sOpRestarting = "restarting"
	k8sOpResetting  = "resetting"
)

// k8sStepsFor 回傳背景作業的步驟清單（GUI 依此畫進度）。
func k8sStepsFor(op string) []string {
	switch op {
	case k8sOpRestarting:
		return k8s.RestartSteps
	case k8sOpResetting:
		return k8s.ResetSteps
	}
	return k8s.Steps
}

// k8sSuperviseEvery：Supervise 每次輪詢都會經過，k8s 的檢查最多每 30 秒一次（每次要打兩個 wsl.exe）。
const k8sSuperviseEvery = 30 * time.Second

// K8sResponse 是 /api/k8s 的回應。
type K8sResponse struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"` // config 的 k8s（使用者開啟了 Kubernetes）
	Distro    string `json:"distro"`
	// Op：背景作業（enabling|disabling|starting）；空 = 閒置。
	Op        string     `json:"op,omitempty"`
	Step      string     `json:"step,omitempty"`
	Steps     []string   `json:"steps"`
	Error     string     `json:"error,omitempty"`
	ErrorStep string     `json:"error_step,omitempty"`
	Status    k8s.Status `json:"status"`
	// Install：建立 distro 那一步的下載進度（沿用新增機器的追蹤）。
	Install *InstallProgress `json:"install,omitempty"`
	// KubeconfigPath：Windows 端匯出的 kubeconfig；SharedKubeconfig：給其他 distro 用的。
	KubeconfigPath   string `json:"kubeconfig_path"`
	SharedKubeconfig string `json:"shared_kubeconfig"`
	// DiskBytes／DiskLimitBytes：k8s distro 的 ext4.vhdx 大小與上限（同 Machines 的 engine 卡；不喚醒 distro）。
	DiskBytes      int64 `json:"disk_bytes,omitempty"`
	DiskLimitBytes int64 `json:"disk_limit_bytes,omitempty"`
	// UserKubeconfig：使用者的 ~/.kube/config；CurrentContext：它目前的 current-context；
	// MergeKubeconfig：config 的 k8s_merge_kubeconfig（wsld 會持續更新其中的 wsld 項目）。
	UserKubeconfig  string `json:"user_kubeconfig,omitempty"`
	CurrentContext  string `json:"current_context,omitempty"`
	MergeKubeconfig bool   `json:"merge_kubeconfig"`
}

func (s *Server) userKubeconfigPath() string {
	if s.UserKubeconfigPath != "" {
		return s.UserKubeconfigPath
	}
	return k8s.UserKubeconfigPath()
}

func (s *Server) kubeconfigPath() string {
	if s.KubeconfigPath != "" {
		return s.KubeconfigPath
	}
	return filepath.Join(filepath.Dir(config.DefaultPath()), "kubeconfig.yaml")
}

// k8sOps 依目前設定建立 k8s 操作；沒有注入或沒有 engine 時回 nil。
func (s *Server) k8sOps(opts K8sOptions) K8sOps {
	if s.K8sFor == nil {
		return nil
	}
	return s.K8sFor(s.cfg(), opts)
}

func (s *Server) handleK8sGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.k8sSnapshot(r.Context()))
}

func (s *Server) k8sSnapshot(ctx context.Context) K8sResponse {
	// 先讀背景作業狀態、再讀設定：作業結束前一定已存好設定（反過來讀會出現「作業已結束、設定還是舊的」）。
	s.k8sMu.Lock()
	op, step, opErr, errStep, last, steps := s.k8sOp, s.k8sStep, s.k8sErr, s.k8sErrStep, s.k8sLast, s.k8sSteps
	s.k8sMu.Unlock()
	if steps == nil {
		steps = k8s.Steps
	}
	cfg := s.cfg()
	resp := K8sResponse{
		Available: s.K8sFor != nil, Enabled: cfg.K8s, Distro: cfg.K8sDistro(), Op: op, Step: step, Steps: steps,
		MergeKubeconfig: cfg.K8sMergeKubeconfig,
		Error:           opErr, ErrorStep: errStep, KubeconfigPath: s.kubeconfigPath(), SharedKubeconfig: k8s.SharedKubeconfig,
		Status: k8s.Status{Distro: cfg.K8sDistro(), State: k8s.StateOff, Endpoint: k8s.Endpoint(cfg.K8sAPIPortOrDefault())},
	}
	if op != "" && step == k8s.StepDistro {
		if p := s.installs().snapshot(); p.Active {
			resp.Install = &p
		}
	}
	// 關閉時不查狀態：不必為了畫 hero 去打 wsl.exe。
	if !cfg.K8s && op == "" {
		return resp
	}
	ops := s.k8sOps(K8sOptions{})
	if ops == nil {
		return resp
	}
	resp.DiskBytes, resp.DiskLimitBytes = s.distroDisk(resp.Distro), s.distroDiskLimit(resp.Distro)
	if up := s.userKubeconfigPath(); up != "" {
		resp.UserKubeconfig = up
		if b, err := os.ReadFile(up); err == nil {
			resp.CurrentContext = k8s.CurrentContext(b)
		}
	}
	st := ops.Status(ctx)
	if st.State == k8s.StateUnknown && last != nil {
		st = *last // 查詢偶爾逾時：沿用上一次的確定狀態，避免徽章閃動
	} else if st.State != k8s.StateUnknown {
		s.k8sMu.Lock()
		s.k8sLast = &st
		s.k8sMu.Unlock()
	}
	resp.Status = st
	return resp
}

// beginK8sOp 標記背景作業開始；已有作業進行中回 false。
func (s *Server) beginK8sOp(op string) bool {
	s.k8sMu.Lock()
	defer s.k8sMu.Unlock()
	if s.k8sOp != "" {
		return false
	}
	s.k8sOp, s.k8sStep, s.k8sErr, s.k8sErrStep, s.k8sSteps = op, "", "", "", k8sStepsFor(op)
	return true
}

func (s *Server) setK8sStep(step string) {
	s.k8sMu.Lock()
	s.k8sStep = step
	s.k8sMu.Unlock()
}

func (s *Server) endK8sOp(err error) {
	s.k8sMu.Lock()
	defer s.k8sMu.Unlock()
	if err != nil {
		s.k8sErr = err.Error()
		var se *k8s.StepError
		if errors.As(err, &se) {
			s.k8sErrStep = se.Step
		} else {
			s.k8sErrStep = s.k8sStep
		}
	}
	s.k8sOp, s.k8sStep, s.k8sLast = "", "", nil
}

func (s *Server) k8sBusy(w http.ResponseWriter) bool {
	s.k8sMu.Lock()
	op := s.k8sOp
	s.k8sMu.Unlock()
	if op == "" {
		return false
	}
	writeErr(w, http.StatusConflict, "conflict", errors.New(i18n.T("k8s.busy", i18n.T("gui.k8s.op."+op))))
	return true
}

// handleK8sEnable POST /api/k8s/enable：開啟 Kubernetes（背景走完整的 bootstrap；以 GET /api/k8s 追蹤步驟）。
func (s *Server) handleK8sEnable(w http.ResponseWriter, _ *http.Request) {
	if s.K8sFor == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("kubernetes support unavailable"))
		return
	}
	if s.SaveConfig == nil {
		writeErr(w, http.StatusInternalServerError, "error", errors.New("config not writable"))
		return
	}
	if _, ok := s.cfg().EngineMachine(); !ok {
		writeErr(w, http.StatusConflict, "not_ready", errors.New(i18n.T("k8s.no_engine")))
		return
	}
	if !s.beginK8sOp(k8sOpEnabling) {
		s.k8sBusy(w)
		return
	}
	go s.runK8sUp(k8sOpEnabling)
	writeJSON(w, http.StatusAccepted, map[string]string{"op": k8sOpEnabling})
}

// handleK8sRestart POST /api/k8s/restart、handleK8sReset POST /api/k8s/reset：背景執行，以 GET /api/k8s 追蹤步驟。
func (s *Server) handleK8sRestart(w http.ResponseWriter, _ *http.Request) {
	s.startK8sOp(w, k8sOpRestarting)
}
func (s *Server) handleK8sReset(w http.ResponseWriter, _ *http.Request) {
	s.startK8sOp(w, k8sOpResetting)
}

func (s *Server) startK8sOp(w http.ResponseWriter, op string) {
	if s.K8sFor == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("kubernetes support unavailable"))
		return
	}
	if s.SaveConfig == nil {
		writeErr(w, http.StatusInternalServerError, "error", errors.New("config not writable"))
		return
	}
	if !s.cfg().K8s {
		writeErr(w, http.StatusConflict, "not_ready", errors.New(i18n.T("k8s.not_running")))
		return
	}
	if !s.beginK8sOp(op) {
		s.k8sBusy(w)
		return
	}
	go s.runK8sUp(op)
	writeJSON(w, http.StatusAccepted, map[string]string{"op": op})
}

// runK8sUp 背景開啟（enabling）、帶起（starting）、重新啟動或重置 k8s；呼叫端已 beginK8sOp。
func (s *Server) runK8sUp(op string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	begin := time.Now()
	err := s.k8sUp(ctx, op)
	if err != nil {
		log.Printf("k8s %s: FAILED after %.1fs: %v", op, time.Since(begin).Seconds(), err)
	} else {
		log.Printf("k8s %s: ready in %.1fs", op, time.Since(begin).Seconds())
	}
	s.endK8sOp(err)
}

func (s *Server) k8sUp(ctx context.Context, op string) error {
	// 先登記 k8s distro（Machines 頁會把它歸到 engine 旁、保護它），建立失敗也沒關係：重試會沿用。
	cfg := s.cfg()
	if err := cfg.SetK8sMachine(cfg.K8sDistro()); err != nil {
		return err
	}
	if err := s.SaveConfig(cfg); err != nil {
		return err
	}
	s.BindActive(cfg)
	ops := s.k8sOps(K8sOptions{Progress: s.setK8sStep, Install: s.k8sInstall})
	var kc []byte
	var err error
	switch op {
	case k8sOpEnabling:
		kc, err = ops.Enable(ctx)
	case k8sOpRestarting:
		kc, err = ops.Restart(ctx)
	case k8sOpResetting:
		kc, err = ops.Reset(ctx)
	default:
		kc, err = ops.Start(ctx)
	}
	if err != nil {
		return err
	}
	cfg = s.cfg()
	cfg.K8s = true
	if err := s.SaveConfig(cfg); err != nil {
		return err
	}
	s.BindActive(cfg)
	s.holdPrimary() // k8s-engine 也要保活
	if err := s.writeKubeconfig(kc); err != nil {
		return &k8s.StepError{Step: k8s.StepKubeconfig, Err: err}
	}
	if cfg.K8sMergeKubeconfig {
		if err := s.mergeUserKubeconfig(kc, false); err != nil {
			return &k8s.StepError{Step: k8s.StepKubeconfig, Err: err}
		}
	}
	s.invalidateNodePorts()
	return nil
}

func (s *Server) mergeUserKubeconfig(kc []byte, setCurrent bool) error {
	up := s.userKubeconfigPath()
	if up == "" {
		return errors.New("no home directory")
	}
	if err := k8s.MergeKubeconfigFile(up, kc, setCurrent); err != nil {
		return errors.New(i18n.T("k8s.merge_failed", up) + ": " + err.Error())
	}
	return nil
}

// handleK8sMerge POST /api/k8s/kubeconfig/merge：把 wsld 的項目併進 ~/.kube/config、設為 current-context，
// 並記住設定（之後每次啟動都更新）。
func (s *Server) handleK8sMerge(w http.ResponseWriter, _ *http.Request) {
	if s.SaveConfig == nil {
		writeErr(w, http.StatusInternalServerError, "error", errors.New("config not writable"))
		return
	}
	kc, err := os.ReadFile(s.kubeconfigPath())
	if err != nil {
		writeErr(w, http.StatusConflict, "not_ready", errors.New(i18n.T("k8s.not_running")))
		return
	}
	if err := s.mergeUserKubeconfig(kc, true); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	cfg := s.cfg()
	cfg.K8sMergeKubeconfig = true
	if err := s.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	s.BindActive(cfg)
	writeJSON(w, http.StatusOK, s.k8sSnapshot(context.Background()))
}

// k8sInstall 建立 k8s-engine，並沿用新增機器的下載進度追蹤。
func (s *Server) k8sInstall(ctx context.Context, base, name string, size int) error {
	if done, ok := s.trackInstall(ctx, base, name); ok {
		defer done()
	}
	if s.InstallK8sDistro != nil {
		return s.InstallK8sDistro(ctx, base, name, size)
	}
	return errors.New("wsl unavailable")
}

func (s *Server) writeKubeconfig(kc []byte) error {
	if len(kc) == 0 {
		return nil
	}
	p := s.kubeconfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, kc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// handleK8sDisable POST /api/k8s/disable：關閉 Kubernetes（停 k3s、移除 pod 容器、卸載對應；distro 與資料保留）。
func (s *Server) handleK8sDisable(w http.ResponseWriter, _ *http.Request) {
	if s.K8sFor == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("kubernetes support unavailable"))
		return
	}
	if s.SaveConfig == nil {
		writeErr(w, http.StatusInternalServerError, "error", errors.New("config not writable"))
		return
	}
	if !s.beginK8sOp(k8sOpDisabling) {
		s.k8sBusy(w)
		return
	}
	// 先寫設定：關閉過程中 Supervise 不會再把它拉起來。
	cfg := s.cfg()
	cfg.K8s = false
	if err := s.SaveConfig(cfg); err != nil {
		s.endK8sOp(err)
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	s.BindActive(cfg)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := s.k8sOps(K8sOptions{}).Disable(ctx)
		s.holdPrimary() // 不再保活 k8s-engine
		if err != nil {
			log.Printf("k8s disable: %v", err)
		}
		s.endK8sOp(err)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"op": k8sOpDisabling})
}

// handleK8sPods GET /api/k8s/pods。
func (s *Server) handleK8sPods(w http.ResponseWriter, r *http.Request) {
	ops := s.k8sOps(K8sOptions{})
	if ops == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("kubernetes support unavailable"))
		return
	}
	if !s.cfg().K8s {
		writeJSON(w, http.StatusOK, []k8s.Pod{})
		return
	}
	pods, err := ops.ListPods(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	if pods == nil {
		pods = []k8s.Pod{}
	}
	writeJSON(w, http.StatusOK, pods)
}

// superviseK8s：已開啟的 k8s 沒在跑（程式剛啟動、WSL 被關掉）就背景帶起。engine 必須已就緒（呼叫端確認）。
func (s *Server) superviseK8s(now time.Time) {
	cfg := s.cfg()
	if !cfg.K8s || s.K8sFor == nil {
		return
	}
	s.k8sMu.Lock()
	if s.k8sOp != "" || (!s.k8sCheckedAt.IsZero() && now.Sub(s.k8sCheckedAt) < k8sSuperviseEvery) || now.Before(s.k8sNextTry) {
		s.k8sMu.Unlock()
		return
	}
	s.k8sCheckedAt = now
	s.k8sMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	st := s.k8sOps(K8sOptions{}).Status(ctx)
	cancel()
	switch st.State {
	case k8s.StateReady, k8s.StateStarting, k8s.StateUnknown:
		return
	}
	if !s.beginK8sOp(k8sOpStarting) {
		return
	}
	go func() {
		s.runK8sUp(k8sOpStarting)
		s.k8sMu.Lock()
		if s.k8sErr != "" {
			backoff := s.SuperviseBackoff
			if backoff <= 0 {
				backoff = defaultSuperviseBackoff
			}
			s.k8sNextTry = time.Now().Add(backoff)
		}
		s.k8sMu.Unlock()
	}()
}

// refuseK8s 對開啟中的 k8s distro 回 409：停止、取消管理或刪除前要先關閉 Kubernetes。
func (s *Server) refuseK8s(w http.ResponseWriter, name string) bool {
	cfg := s.cfg()
	if m, ok := cfg.FindMachine(name); ok && m.IsK8s() && (cfg.K8s || s.k8sOpActive()) {
		writeErr(w, http.StatusConflict, "conflict", errors.New(i18n.T("k8s.protected", m.Distro)))
		return true
	}
	return false
}

func (s *Server) k8sOpActive() bool {
	s.k8sMu.Lock()
	defer s.k8sMu.Unlock()
	return s.k8sOp != ""
}

// ResetK8sState 清除背景作業狀態（測試主機 /__test/reset 用）。
func (s *Server) ResetK8sState() {
	s.k8sMu.Lock()
	s.k8sOp, s.k8sStep, s.k8sErr, s.k8sErrStep, s.k8sLast, s.k8sSteps = "", "", "", "", nil, nil
	s.k8sNodePorts, s.k8sNodePortsAt = nil, time.Time{}
	s.k8sCheckedAt, s.k8sNextTry = time.Time{}, time.Time{}
	s.k8sMu.Unlock()
}

// NewK8sFor 回傳以真正的 wsl.exe 操作 k8s-engine 的 Server.K8sFor。log 寫進 crash.log 所在的 log 輸出。
func NewK8sFor(r wsl.Runner, bundle []byte) func(cfg config.Config, opts K8sOptions) K8sOps {
	return func(cfg config.Config, opts K8sOptions) K8sOps {
		m := &k8s.Manager{
			WSL: r, Distro: cfg.K8sDistro(), Base: cfg.EngineBaseOrDefault(), APIPort: cfg.K8sAPIPortOrDefault(),
			TLSSAN: cfg.K8sTLSSAN, KubeletArgs: cfg.K8sKubeletArgs,
			Bundle: bundle, Output: log.Writer(), Progress: opts.Progress, Install: opts.Install,
		}
		if e, ok := cfg.EngineMachine(); ok {
			m.EngineDistro = e.Distro
		}
		return m
	}
}
