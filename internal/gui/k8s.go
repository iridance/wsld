package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/host"
	"github.com/iridance/wsld/internal/k8s"
)

// K8s 是 GUI 需要的 k3s 操作子集（*k8s.Manager 滿足）。
type K8s interface {
	Status(ctx context.Context) k8s.Status
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Reset(ctx context.Context) error
	Kubeconfig(ctx context.Context) ([]byte, error)
	ListNamespaces(ctx context.Context) ([]string, error)
	ListPods(ctx context.Context, ns string) ([]k8s.Pod, error)
	ListDeployments(ctx context.Context, ns string) ([]k8s.Deployment, error)
	ListServices(ctx context.Context, ns string) ([]k8s.Service, error)
	ListExtras(ctx context.Context, ns string) (k8s.Extras, error)
	DeploymentDetail(ctx context.Context, ns, name string) (k8s.DeploymentDetail, error)
	Delete(ctx context.Context, kind, ns, name string) error
	RestartDeployment(ctx context.Context, ns, name string) error
	ScaleDeployment(ctx context.Context, ns, name string, replicas int) error
	Deploy(ctx context.Context, spec k8s.DeploySpec) (k8s.DeployResult, error)
	Expose(ctx context.Context, spec k8s.ExposeSpec) (k8s.DeployResult, error)
	Apply(ctx context.Context, manifest []byte) (string, error)
	DeployStack(ctx context.Context, spec k8s.StackSpec) (k8s.StackResult, error)
	DeleteStack(ctx context.Context, ns, name string, withPVC bool) error
}

// CommandStream 在 distro 內執行單一指令並回傳雙向連線（kubectl logs／exec）。
type CommandStream interface {
	Open(ctx context.Context, distro string, argv []string, tty bool, cols, rows int) (net.Conn, error)
}

// HostCommandStream 以本機 wsl.exe 執行（Windows）；以 root 身分，因為是對叢集 API 操作而非使用者 shell。
type HostCommandStream struct{}

// Open 實作 CommandStream。
func (HostCommandStream) Open(_ context.Context, distro string, argv []string, tty bool, cols, rows int) (net.Conn, error) {
	return host.StartCommand(distro, "root", argv, tty, cols, rows)
}

// K8sResponse 是 /api/k8s 的回應。
type K8sResponse struct {
	// Machine：目前開啟 k8s 的 machine；沒有人開啟時為「按啟動會使用的」primary machine。
	Machine string `json:"machine"`
	// Enabled：config 中 Machine 已開啟 k8s。
	Enabled bool `json:"enabled"`
	// Op：背景作業進行中（starting|stopping|resetting）；空 = 閒置。
	Op string `json:"op,omitempty"`
	// Step：starting 作業目前所在步驟（k8s.Steps 之一）；Steps 是完整順序，供前端畫步驟清單。
	Step  string   `json:"step,omitempty"`
	Steps []string `json:"steps"`
	// ErrorStep：上一次作業失敗時所在的步驟（與 Error 同時出現）。
	ErrorStep string     `json:"error_step,omitempty"`
	Error     string     `json:"error,omitempty"`   // 上一次背景作業的錯誤
	Warning   string     `json:"warning,omitempty"` // 上一次背景作業成功但有附帶問題（如 kubeconfig 匯出失敗）
	Status    k8s.Status `json:"status"`
	// KubeconfigPath：匯出 kubeconfig 的目標檔。
	KubeconfigPath string `json:"kubeconfig_path"`
}

// ResetK8sState 清除背景作業狀態（測試主機 /__test/reset 用）。
func (s *Server) ResetK8sState() {
	s.k8sMu.Lock()
	s.k8sOp, s.k8sErr, s.k8sWarn, s.k8sStep, s.k8sErrStep = "", "", "", "", ""
	s.k8sMu.Unlock()
}

func (s *Server) k8sMgr(distro string) K8s {
	if s.K8sFor == nil {
		return nil
	}
	return s.K8sFor(distro)
}

func (s *Server) kubeconfigPath() string {
	if s.KubeconfigPath != "" {
		return s.KubeconfigPath
	}
	return k8s.DefaultKubeconfigPath()
}

// k8sTarget 決定頁面所指的 machine：已開啟 k8s 的優先，否則 primary（按啟動會使用的）。
func k8sTarget(cfg config.Config) (target string, enabled bool) {
	if m, ok := cfg.K8sMachine(); ok {
		return m.Distro, true
	}
	return cfg.Active, false
}

func (s *Server) k8sUnavailable(w http.ResponseWriter) bool {
	if s.K8sFor == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("kubernetes support unavailable"))
		return true
	}
	return false
}

func (s *Server) handleK8sGet(w http.ResponseWriter, r *http.Request) {
	if s.k8sUnavailable(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.k8sSnapshot(r.Context()))
}

func (s *Server) k8sSnapshot(ctx context.Context) K8sResponse {
	// 先讀背景作業狀態、再讀設定：作業結束（Op 為空）前一定已存好旗標，
	// 反過來讀會在作業剛好完成時回出「Op 已空、旗標還是舊的」的錯誤組合。
	s.k8sMu.Lock()
	op, opErr, opWarn, opStep, opErrStep := s.k8sOp, s.k8sErr, s.k8sWarn, s.k8sStep, s.k8sErrStep
	s.k8sMu.Unlock()
	cfg := s.cfg()
	target, enabled := k8sTarget(cfg)
	resp := K8sResponse{Machine: target, Enabled: enabled, KubeconfigPath: s.kubeconfigPath(), Steps: k8s.Steps}
	resp.Op, resp.Error, resp.Warning = op, opErr, opWarn
	if op == "starting" {
		resp.Step = opStep
	}
	if opErr != "" {
		resp.ErrorStep = opErrStep
	}
	if target == "" {
		return resp
	}
	if mgr := s.k8sMgr(target); mgr != nil {
		st := mgr.Status(ctx)
		s.k8sMu.Lock()
		if st.State == k8s.StateUnknown {
			// 查詢偶爾逾時：沿用同一台機器上一次的確定狀態，避免徽章在啟動中／未啟動間閃動。
			if last, ok := s.k8sLast[target]; ok {
				st = last
			}
		} else {
			if s.k8sLast == nil {
				s.k8sLast = map[string]k8s.Status{}
			}
			s.k8sLast[target] = st
		}
		s.k8sMu.Unlock()
		resp.Status = st
	}
	return resp
}

// startK8sOp 啟動單一背景作業；已有作業進行中回 false。
// 傳給 fn 的 ctx 帶有進度回報（k8s.ReportStep），目前步驟會出現在 /api/k8s 的 step。
func (s *Server) startK8sOp(op string, fn func(ctx context.Context) (warning string, err error)) bool {
	s.k8sMu.Lock()
	if s.k8sOp != "" {
		s.k8sMu.Unlock()
		return false
	}
	s.k8sOp, s.k8sErr, s.k8sWarn, s.k8sStep, s.k8sErrStep = op, "", "", "", ""
	s.k8sMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		ctx = k8s.WithProgress(ctx, func(step string) {
			s.k8sMu.Lock()
			s.k8sStep = step
			s.k8sMu.Unlock()
		})
		warn, err := fn(ctx)
		s.k8sMu.Lock()
		s.k8sOp = ""
		if err != nil {
			s.k8sErr = err.Error()
			s.k8sErrStep = s.k8sStep
		}
		s.k8sStep = ""
		s.k8sWarn = warn
		s.k8sMu.Unlock()
	}()
	return true
}

// resumeK8s：引擎啟動後，若 primary machine 設定為 k8s = true，背景啟動 k3s（對應 wsld start）。
func (s *Server) resumeK8s() {
	if s.K8sFor == nil {
		return
	}
	cfg := s.cfg()
	m, ok := cfg.K8sMachine()
	if !ok || !strings.EqualFold(m.Distro, cfg.Active) {
		return
	}
	s.startK8sOp("starting", func(ctx context.Context) (string, error) {
		return "", s.k8sMgr(m.Distro).Start(ctx)
	})
}

// resumeK8sIfDown：engine 已在跑時，只有 k3s 沒起來才恢復（避免無謂的「啟動中」狀態）。
func (s *Server) resumeK8sIfDown() {
	if s.K8sFor == nil {
		return
	}
	cfg := s.cfg()
	m, ok := cfg.K8sMachine()
	if !ok || !strings.EqualFold(m.Distro, cfg.Active) {
		return
	}
	if s.k8sMgr(m.Distro).Status(context.Background()).State == k8s.StateReady {
		return
	}
	s.resumeK8s()
}

func (s *Server) k8sBusy(w http.ResponseWriter) {
	writeErr(w, http.StatusConflict, "conflict", errors.New("another Kubernetes operation is in progress"))
}

// exportKubeconfig 把 k3s kubeconfig 合併進使用者的 kubeconfig。
func (s *Server) exportKubeconfig(ctx context.Context, mgr K8s, distro string) error {
	raw, err := mgr.Kubeconfig(ctx)
	if err != nil {
		return err
	}
	return k8s.ExportKubeconfig(s.kubeconfigPath(), raw, distro)
}

func (s *Server) saveK8sFlag(distro string, on bool) error {
	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	if err := cfg.SetK8s(distro, on); err != nil {
		return err
	}
	return s.SaveConfig(cfg)
}

func (s *Server) handleK8sOn(w http.ResponseWriter, r *http.Request) {
	if s.k8sUnavailable(w) {
		return
	}
	cfg := s.cfg()
	target := r.URL.Query().Get("machine")
	if target == "" {
		target = cfg.Active
		if t, ok := cfg.K8sTarget(); ok {
			target = t.Distro // 有 engine 時 k3s 一律裝在 engine
		}
	}
	m, ok := cfg.FindMachine(target)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", fmt.Errorf("machine %q not found", target))
		return
	}
	if !m.Ready() {
		writeErr(w, http.StatusServiceUnavailable, "not_ready", fmt.Errorf("machine %q is not initialised yet", m.Distro))
		return
	}
	if eng, has := cfg.EngineMachine(); has && !strings.EqualFold(eng.Distro, m.Distro) {
		writeErr(w, http.StatusConflict, "conflict",
			fmt.Errorf("k8s must run on the engine %q (k3s --docker has to sit beside dockerd)", eng.Distro))
		return
	}
	owner, hasOwner := cfg.K8sMachine()
	if hasOwner && !strings.EqualFold(owner.Distro, m.Distro) {
		writeErr(w, http.StatusConflict, "conflict",
			fmt.Errorf("k8s is already enabled on %q (all WSL2 distros share one network namespace); turn it off there first", owner.Distro))
		return
	}
	addContext := r.URL.Query().Get("add_context") == "1"
	if !s.startK8sOp("starting", func(ctx context.Context) (string, error) {
		mgr := s.k8sMgr(m.Distro)
		if err := mgr.Start(ctx); err != nil {
			return "", err
		}
		if err := s.saveK8sFlag(m.Distro, true); err != nil {
			return "", err
		}
		// 預設不寫 Windows 的 ~/.kube/config（kubeconfig 以機器內的 k3s.yaml 為準）；
		// GUI 的「Add context to ~/.kube/config」勾選時以 add_context=1 要求合併。
		// 合併失敗只算警告：叢集本身已經可用。
		if addContext {
			k8s.ReportStep(ctx, k8s.StepContext)
			if err := s.exportKubeconfig(ctx, mgr, m.Distro); err != nil {
				return "kubeconfig: " + err.Error(), nil
			}
		}
		return "", nil
	}) {
		s.k8sBusy(w)
		return
	}
	writeJSON(w, http.StatusAccepted, s.k8sSnapshot(r.Context()))
}

func (s *Server) handleK8sOff(w http.ResponseWriter, r *http.Request) {
	if s.k8sUnavailable(w) {
		return
	}
	owner, ok := s.cfg().K8sMachine()
	if !ok {
		writeJSON(w, http.StatusOK, s.k8sSnapshot(r.Context()))
		return
	}
	if !s.startK8sOp("stopping", func(ctx context.Context) (string, error) {
		if err := s.k8sMgr(owner.Distro).Stop(ctx); err != nil {
			return "", err
		}
		return "", s.saveK8sFlag(owner.Distro, false)
	}) {
		s.k8sBusy(w)
		return
	}
	writeJSON(w, http.StatusAccepted, s.k8sSnapshot(r.Context()))
}

func (s *Server) handleK8sReset(w http.ResponseWriter, r *http.Request) {
	if s.k8sUnavailable(w) {
		return
	}
	owner, ok := s.cfg().K8sMachine()
	if !ok {
		writeErr(w, http.StatusConflict, "conflict", errors.New("k8s is not enabled on any machine"))
		return
	}
	if !s.startK8sOp("resetting", func(ctx context.Context) (string, error) {
		mgr := s.k8sMgr(owner.Distro)
		if err := mgr.Reset(ctx); err != nil {
			return "", err
		}
		// 重置後憑證已換新：只有使用者曾匯出過才同步更新，不主動建立。
		if k8s.HasExportedContext(s.kubeconfigPath(), owner.Distro) {
			if err := s.exportKubeconfig(ctx, mgr, owner.Distro); err != nil {
				return "kubeconfig: " + err.Error(), nil
			}
		}
		return "", nil
	}) {
		s.k8sBusy(w)
		return
	}
	writeJSON(w, http.StatusAccepted, s.k8sSnapshot(r.Context()))
}

func (s *Server) handleK8sKubeconfig(w http.ResponseWriter, r *http.Request) {
	if s.k8sUnavailable(w) {
		return
	}
	// 匯出只是讀機器內的 k3s.yaml，與 k3s 目前是否啟動／就緒無關：k8s 關閉時改用主要機器，
	// 檔案不存在（沒裝過）才由讀取失敗回錯誤。
	target, _ := k8sTarget(s.cfg())
	mgr := s.k8sMgr(target)
	if target == "" || mgr == nil {
		writeErr(w, http.StatusConflict, "conflict", errors.New("no machine to export a kubeconfig from"))
		return
	}
	if err := s.exportKubeconfig(r.Context(), mgr, target); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": s.kubeconfigPath(), "context": k8s.ContextName(target)})
}

// k8sOwner 解析資源類請求要操作的 machine；k8s 未開啟回 409。
func (s *Server) k8sOwner(w http.ResponseWriter) (K8s, string, bool) {
	if s.k8sUnavailable(w) {
		return nil, "", false
	}
	owner, ok := s.cfg().K8sMachine()
	if !ok {
		writeErr(w, http.StatusConflict, "conflict", errors.New("k8s is not enabled on any machine"))
		return nil, "", false
	}
	return s.k8sMgr(owner.Distro), owner.Distro, true
}

func nsQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	ns := r.URL.Query().Get("ns")
	if ns != "" && !k8s.ValidName(ns) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid namespace"))
		return "", false
	}
	return ns, true
}

func k8sErr(w http.ResponseWriter, err error) {
	writeErr(w, http.StatusBadGateway, "error", err)
}

func (s *Server) handleK8sNamespaces(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, err := mgr.ListNamespaces(r.Context())
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

func (s *Server) handleK8sPods(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, ok := nsQuery(w, r)
	if !ok {
		return
	}
	v, err := mgr.ListPods(r.Context(), ns)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleK8sDeployments(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, ok := nsQuery(w, r)
	if !ok {
		return
	}
	v, err := mgr.ListDeployments(r.Context(), ns)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleK8sServices(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, ok := nsQuery(w, r)
	if !ok {
		return
	}
	v, err := mgr.ListServices(r.Context(), ns)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleK8sExtras(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, ok := nsQuery(w, r)
	if !ok {
		return
	}
	v, err := mgr.ListExtras(r.Context(), ns)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleK8sDeploymentDetail(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !k8s.ValidName(ns) || !k8s.ValidName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid namespace or name"))
		return
	}
	v, err := mgr.DeploymentDetail(r.Context(), ns, name)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// 路徑種類（複數）→ kubectl 種類（單數）。
var k8sKinds = map[string]string{"pods": "pod", "deployments": "deployment", "services": "service"}

func (s *Server) handleK8sDelete(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	kind, known := k8sKinds[r.PathValue("kind")]
	if !known {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("unknown kind"))
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !k8s.ValidName(ns) || !k8s.ValidName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid name"))
		return
	}
	if err := mgr.Delete(r.Context(), kind, ns, name); err != nil {
		k8sErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleK8sRestart(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !k8s.ValidName(ns) || !k8s.ValidName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid name"))
		return
	}
	if err := mgr.RestartDeployment(r.Context(), ns, name); err != nil {
		k8sErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleK8sScale(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !k8s.ValidName(ns) || !k8s.ValidName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid name"))
		return
	}
	var in struct {
		Replicas int `json:"replicas"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	if in.Replicas < 0 || in.Replicas > k8s.MaxReplicas {
		writeErr(w, http.StatusBadRequest, "usage", fmt.Errorf("replicas must be between 0 and %d", k8s.MaxReplicas))
		return
	}
	if err := mgr.ScaleDeployment(r.Context(), ns, name, in.Replicas); err != nil {
		k8sErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleK8sOpen 以系統瀏覽器開啟 Service 的 NodePort（http://localhost:<nodePort>）；
// 只接受叢集裡某個 Service 實際宣告的 NodePort。
func (s *Server) handleK8sOpen(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	var in struct {
		Port int `json:"port"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	svcs, err := mgr.ListServices(r.Context(), "")
	if err != nil {
		k8sErr(w, err)
		return
	}
	allowed := false
	for _, sv := range svcs {
		for _, p := range sv.Ports {
			tcp := p.Protocol == "" || p.Protocol == "TCP"
			// NodePort，或 LoadBalancer 的 Service 埠（k3s ServiceLB 把它綁在 localhost 上）
			if in.Port > 0 && tcp && (p.NodePort == in.Port || (sv.Type == "LoadBalancer" && p.Port == in.Port)) {
				allowed = true
			}
		}
	}
	if !allowed {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("not a NodePort or LoadBalancer port of any service"))
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

// podStream 驗證參數並開啟 kubectl 指令串流；失敗時已寫出回應。
func (s *Server) podStream(w http.ResponseWriter, r *http.Request, tty bool) (net.Conn, bool) {
	if s.k8sUnavailable(w) {
		return nil, false
	}
	if s.K8sStream == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("kubernetes streaming unavailable"))
		return nil, false
	}
	owner, ok := s.cfg().K8sMachine()
	if !ok {
		writeErr(w, http.StatusConflict, "conflict", errors.New("k8s is not enabled on any machine"))
		return nil, false
	}
	ns, pod, ctr := r.PathValue("ns"), r.PathValue("name"), r.URL.Query().Get("container")
	var argv []string
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if tty {
		argv = k8s.ExecArgs(ns, pod, ctr)
	} else {
		argv = k8s.LogsArgs(ns, pod, ctr, tailParam(r), true)
	}
	if argv == nil {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid pod, namespace or container name"))
		return nil, false
	}
	conn, err := s.K8sStream.Open(r.Context(), owner.Distro, argv, tty, cols, rows)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return nil, false
	}
	return conn, true
}

func (s *Server) handleK8sLogsStream(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.podStream(w, r, false)
	if !ok {
		return
	}
	c, err := s.acceptWS(w, r)
	if err != nil {
		conn.Close()
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				conn.Close()
				return
			}
		}
	}()
	defer conn.Close()
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
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

func (s *Server) handleK8sExec(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.podStream(w, r, true)
	if !ok {
		return
	}
	c, err := s.acceptWS(w, r)
	if err != nil {
		conn.Close()
		return
	}
	defer c.CloseNow()
	defer conn.Close()
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
				continue // resize 對 pipe 無作用（PTY 大小在啟動時決定）
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

// deployRequest 是 POST /api/k8s/deploy 的請求本文。
type deployRequest = k8s.DeploySpec

func (s *Server) handleK8sDeploy(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	var req deployRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid JSON body"))
		return
	}
	if _, err := req.Normalize(); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	res, err := mgr.Deploy(r.Context(), req)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleK8sExpose 替既有 Deployment 建立（或更新）同名 Service。
func (s *Server) handleK8sExpose(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	var req k8s.ExposeSpec
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid JSON body"))
		return
	}
	if !k8s.ValidName(req.Namespace) || !k8s.ValidName(req.Name) || (req.Expose != k8s.ExposeNodePort && req.Expose != k8s.ExposeLoadBalancer) ||
		req.Port < 0 || req.Port > 65535 || req.HostPort < 0 || req.HostPort > 65535 {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid expose request"))
		return
	}
	res, err := mgr.Expose(r.Context(), req)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleK8sApply(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, k8s.MaxApplyBytes+1))
	if err != nil || len(body) > k8s.MaxApplyBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "usage", errors.New("manifest is too large"))
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("empty manifest"))
		return
	}
	out, err := mgr.Apply(r.Context(), body)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(out)})
}

// handleK8sStack 一次部署一組服務（POST /api/k8s/stack）。
func (s *Server) handleK8sStack(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	var req k8s.StackSpec
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid JSON body"))
		return
	}
	if _, err := req.Normalize(); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	res, err := mgr.DeployStack(r.Context(), req)
	if err != nil {
		k8sErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleK8sStackDelete 刪除整個 stack；?pvc=1 時連 PVC 一起刪。
func (s *Server) handleK8sStackDelete(w http.ResponseWriter, r *http.Request) {
	mgr, _, ok := s.k8sOwner(w)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !k8s.ValidName(ns) || !k8s.ValidName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid name"))
		return
	}
	if err := mgr.DeleteStack(r.Context(), ns, name, r.URL.Query().Get("pvc") == "1"); err != nil {
		k8sErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
