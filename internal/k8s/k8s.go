// Package k8s 管理專用的 k3s distro（k8s-engine）：k3s 跑在那裡，但 runtime 是 engine 的 dockerd
// （cri-dockerd 經 /mnt/wsl/wsld/docker.sock），pod 就是 engine 上的 docker 容器、映像檔共用。
// 跨 distro 的路徑對應見 scripts/lib/k8s-lib.sh。
package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/wsl"
)

const (
	k3sKubeconfig = "/etc/rancher/k3s/k3s.yaml"
	scriptTmp     = "/tmp/wsld-k8s.sh"
	// SharedKubeconfig 是給其他 distro 用的 kubeconfig（k3s 啟動後由 drop-in 複製到共用 tmpfs）。
	SharedKubeconfig = "/mnt/wsl/wsld/k8s/kubeconfig"
	// ContextName 是匯出的 kubeconfig 裡 cluster／user／context 的名稱（k3s 原本都叫 default）。
	ContextName = "wsld"
	// DefaultVHDSizeGB 是新建 k8s-engine 的虛擬磁碟上限（只放 k3s 狀態與 PV；映像檔在 engine）。
	DefaultVHDSizeGB = 60
)

// 在 distro 內以 `bash -c` 執行的腳本。不可含 $ ` \ "：wsl.exe 外層的 shell 會先展開它們。
const (
	// statusScript：9 = 未安裝、8 = 服務未啟動、0 = 印出 nodes JSON；其他 = API 還沒好。
	statusScript = "test -x /usr/local/bin/k3s || exit 9; systemctl is-active --quiet k3s || exit 8; k3s kubectl get nodes -o json --request-timeout=5s"
	// removePodsScript：在 engine 移除 cri-dockerd 建立的 pod 容器（停 k3s 不會停它們，它們還接著 k8s-engine 的曝露路徑）。
	// 轉發容器（forward.go）連的是叢集內的位址，也一併移除。
	removePodsScript = "docker ps -aq --filter label=io.kubernetes.pod.namespace | xargs -r docker rm -f; docker ps -aq --filter label=" + LabelForward + " | xargs -r docker rm -f"
)

// State 是 k8s 的概括狀態。
type State string

const (
	StateAbsent       State = "absent"        // 還沒有 k8s-engine distro
	StateStopped      State = "stopped"       // distro 未執行（不去喚醒它）
	StateNotInstalled State = "not-installed" // distro 在，但 k3s 尚未安裝
	StateOff          State = "off"           // 已安裝、服務未啟動
	StateStarting     State = "starting"      // 服務 active，API／node 尚未 Ready
	StateReady        State = "ready"
	StateUnknown      State = "unknown" // 查詢失敗（wsl.exe 逾時等）：呼叫端應沿用上一次狀態
)

// Status 是 k8s 的狀態。
type Status struct {
	Distro   string `json:"distro"`
	State    State  `json:"state"`
	Version  string `json:"version,omitempty"`
	NodeName string `json:"node_name,omitempty"`
	NodeIP   string `json:"node_ip,omitempty"`
	Endpoint string `json:"endpoint"`
}

// 啟用流程的步驟（GUI 的步驟清單依此順序）。
const (
	StepEngine     = "engine"
	StepDistro     = "distro"
	StepBootstrap  = "bootstrap"
	StepShare      = "share"
	StepStart      = "start"
	StepReady      = "ready"
	StepKubeconfig = "kubeconfig"
	StepStop       = "stop" // Restart／Reset：先停止
	StepWipe       = "wipe" // Reset：清除叢集資料
)

// Steps 是 Enable 的完整步驟順序。
var Steps = []string{StepEngine, StepDistro, StepBootstrap, StepShare, StepStart, StepReady, StepKubeconfig}

// RestartSteps／ResetSteps 是 Restart／Reset 的步驟順序。
var (
	RestartSteps = []string{StepStop, StepEngine, StepShare, StepStart, StepReady, StepKubeconfig}
	ResetSteps   = []string{StepStop, StepWipe, StepEngine, StepShare, StepStart, StepReady, StepKubeconfig}
)

// StepError 標出失敗在哪一步。
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }

// Manager 管理 k8s-engine。
type Manager struct {
	WSL          wsl.Runner
	Distro       string   // k8s-engine
	EngineDistro string   // 跑 dockerd 的 distro
	Base         string   // 新建 distro 用的發行版（`wsl --install -d`）；"" = Debian
	VHDSizeGB    int      // 新建 distro 的磁碟上限；0 = DefaultVHDSizeGB
	APIPort      int      // k3s API 埠（呼叫端已正規化）
	TLSSAN       []string // 額外的 API server 憑證 SAN（config 的 k8s_tls_san）
	KubeletArgs  []string // 額外的 kubelet 參數（config 的 k8s_kubelet_args）
	Bundle       []byte   // scripts.K8sBundle()
	Output       io.Writer

	// Progress 在每個步驟開始時呼叫（可為 nil）。
	Progress func(step string)
	// Install 建立 distro（nil = wsl.Install；GUI 換成會回報下載進度的版本）。
	Install func(ctx context.Context, base, name string, vhdSizeGB int) error

	ReadyAttempts int           // 預設 180
	ReadyInterval time.Duration // 預設 1s
	Sleep         func(time.Duration)
	// PortBusy：Windows 端該 localhost 埠是否已被占用（nil = 以 127.0.0.1 撥號探測；Deploy 挑 LoadBalancer 埠用）。
	PortBusy func(port int) bool
}

// Endpoint 是 Windows 與各 distro 連 API 的位址（WSL2 共用 network namespace，localhost 轉發到 Windows）。
func Endpoint(port int) string { return "https://127.0.0.1:" + strconv.Itoa(port) }

// configArgs 是 03-k8s.sh install／configure 的參數（值已由 config 驗證過）。
func (m *Manager) configArgs() []string {
	args := []string{m.Distro, strconv.Itoa(m.APIPort)}
	for _, v := range m.TLSSAN {
		args = append(args, "--tls-san="+v)
	}
	for _, v := range m.KubeletArgs {
		args = append(args, "--kubelet-arg="+v)
	}
	return args
}

func (m *Manager) progress(step string) {
	if m.Progress != nil {
		m.Progress(step)
	}
}

func (m *Manager) print(s string) {
	if m.Output != nil {
		fmt.Fprintln(m.Output, s)
	}
}

func (m *Manager) sleep(d time.Duration) {
	if m.Sleep != nil {
		m.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (m *Manager) base() string {
	if m.Base != "" {
		return m.Base
	}
	return "Debian"
}

func (m *Manager) vhdSize() int {
	if m.VHDSizeGB > 0 {
		return m.VHDSizeGB
	}
	return DefaultVHDSizeGB
}

// in 在指定 distro 內以 root 執行；錯誤附上 stderr。
func (m *Manager) in(ctx context.Context, distro string, stdin io.Reader, cmd ...string) (string, error) {
	out, stderr, err := wsl.RunIn(ctx, m.WSL, distro, "root", stdin, cmd...)
	if err != nil {
		if msg := strings.TrimSpace(wsl.DecodeOutput(stderr)); msg != "" {
			return string(out), fmt.Errorf("%w: %s", err, msg)
		}
	}
	return string(out), err
}

// runScript 把 bundle 寫入 distro 暫存再執行（避免 bash -s 的 stdin 被 systemctl 等吞掉）。
func (m *Manager) runScript(ctx context.Context, distro string, args ...string) error {
	if len(m.Bundle) == 0 {
		return errors.New("k8s bundle missing")
	}
	cmd := "cat > " + scriptTmp + " && bash " + scriptTmp + " " + strings.Join(args, " ")
	out, err := m.in(ctx, distro, bytes.NewReader(m.Bundle), "bash", "-c", cmd)
	if m.Output != nil {
		_, _ = io.WriteString(m.Output, out)
	}
	return err
}

// distroState 回傳 distro 的狀態（"" = 不存在）。
func (m *Manager) distroState(ctx context.Context, name string) (string, error) {
	list, err := wsl.ListDistros(ctx, m.WSL)
	if err != nil {
		return "", err
	}
	d, ok := wsl.FindDistro(list, name)
	if !ok {
		return "", nil
	}
	return d.State, nil
}

// Status 盡力收集狀態。distro 未執行時不喚醒它（GUI 會輪詢）。
func (m *Manager) Status(ctx context.Context) Status {
	st := Status{Distro: m.Distro, State: StateUnknown, Endpoint: Endpoint(m.APIPort)}
	state, err := m.distroState(ctx, m.Distro)
	switch {
	case err != nil:
		return st
	case state == "":
		st.State = StateAbsent
		return st
	case !strings.EqualFold(state, "Running"):
		st.State = StateStopped
		return st
	}
	out, err := m.in(ctx, m.Distro, nil, "bash", "-c", statusScript)
	switch wsl.ExitCode(err) {
	case 0:
	case 9:
		st.State = StateNotInstalled
		return st
	case 8:
		st.State = StateOff
		return st
	case -1:
		return st // wsl.exe 本身失敗：不知道
	default:
		st.State = StateStarting
		return st
	}
	n, err := ParseNode(out)
	if err != nil {
		st.State = StateStarting
		return st
	}
	st.NodeName, st.NodeIP, st.Version = n.Name, n.IP, n.Version
	st.State = StateStarting
	if n.Ready {
		st.State = StateReady
	}
	return st
}

// Enable 從零建立到 Ready：確認 engine → 建立 distro → 安裝 → 分享 → 啟動 → 等待 → 讀 kubeconfig。
// 每一步都冪等，失敗後重跑會從頭檢查。回傳 k3s 產生的 kubeconfig（已改名為 ContextName）。
func (m *Manager) Enable(ctx context.Context) ([]byte, error) {
	m.progress(StepEngine)
	if err := m.checkEngine(ctx); err != nil {
		return nil, &StepError{StepEngine, err}
	}
	m.progress(StepDistro)
	if err := m.ensureDistro(ctx); err != nil {
		return nil, &StepError{StepDistro, err}
	}
	m.progress(StepBootstrap)
	if err := m.bootstrap(ctx); err != nil {
		return nil, &StepError{StepBootstrap, err}
	}
	return m.start(ctx)
}

// Start 啟動已安裝的 k3s（分享 → 啟動 → 等待 → 讀 kubeconfig）；尚未安裝時走完整的 Enable。
func (m *Manager) Start(ctx context.Context) ([]byte, error) {
	switch m.Status(ctx).State {
	case StateAbsent, StateNotInstalled:
		return m.Enable(ctx)
	}
	m.progress(StepEngine)
	if err := m.checkEngine(ctx); err != nil {
		return nil, &StepError{StepEngine, err}
	}
	return m.start(ctx)
}

func (m *Manager) start(ctx context.Context) ([]byte, error) {
	m.progress(StepShare)
	if err := m.runScript(ctx, m.EngineDistro, "share-engine"); err != nil {
		return nil, &StepError{StepShare, fmt.Errorf("%s: %w", i18n.T("k8s.share_failed", m.EngineDistro), err)}
	}
	m.progress(StepStart)
	// 每次啟動前重寫 k3s 設定：config.toml 的 tls-san／kubelet-arg 變更在下次啟動生效。
	if err := m.runScript(ctx, m.Distro, append([]string{"configure"}, m.configArgs()...)...); err != nil {
		return nil, &StepError{StepStart, fmt.Errorf("%s: %w", i18n.T("k8s.start_failed", m.Distro), err)}
	}
	// 之前連續失敗會觸發 start limit，start 會直接被拒；先 reset-failed。
	_, _ = m.in(ctx, m.Distro, nil, "systemctl", "reset-failed", "k3s", "wsld-cri-dockerd", "wsld-k8s-mounts")
	if _, err := m.in(ctx, m.Distro, nil, "systemctl", "start", "k3s"); err != nil {
		return nil, &StepError{StepStart, fmt.Errorf("%s: %w", i18n.T("k8s.start_failed", m.Distro), err)}
	}
	m.progress(StepReady)
	if err := m.waitReady(ctx); err != nil {
		return nil, &StepError{StepReady, err}
	}
	m.progress(StepKubeconfig)
	kc, err := m.Kubeconfig(ctx)
	if err != nil {
		return nil, &StepError{StepKubeconfig, err}
	}
	return kc, nil
}

// checkEngine 確認 engine 的 dockerd 正在執行（k8s 的 runtime；engine 由 wsld 另外帶起）。
func (m *Manager) checkEngine(ctx context.Context) error {
	if m.EngineDistro == "" {
		return errors.New(i18n.T("k8s.no_engine"))
	}
	out, _ := m.in(ctx, m.EngineDistro, nil, "systemctl", "is-active", "docker")
	if strings.TrimSpace(out) != "active" {
		return errors.New(i18n.T("k8s.engine_down", m.EngineDistro))
	}
	return nil
}

func (m *Manager) ensureDistro(ctx context.Context) error {
	state, err := m.distroState(ctx, m.Distro)
	if err != nil {
		return err
	}
	if state != "" {
		return nil
	}
	m.print(i18n.T("k8s.installing_distro", m.Distro))
	install := m.Install
	if install == nil {
		install = func(ctx context.Context, base, name string, size int) error {
			return wsl.Install(ctx, m.WSL, base, name, size)
		}
	}
	if err := install(ctx, m.base(), m.Distro, m.vhdSize()); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.install_distro_failed", m.Distro), err)
	}
	return nil
}

// bootstrap 在 k8s-engine 跑安裝腳本；結束碼 3 = systemd 剛啟用，重開 distro 再跑一次。
func (m *Manager) bootstrap(ctx context.Context) error {
	args := append([]string{"install"}, m.configArgs()...)
	err := m.runScript(ctx, m.Distro, args...)
	if wsl.ExitCode(err) == 3 {
		if terr := wsl.Terminate(ctx, m.WSL, m.Distro); terr != nil {
			return terr
		}
		err = m.runScript(ctx, m.Distro, args...)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.bootstrap_failed", m.Distro), err)
	}
	return nil
}

func (m *Manager) waitReady(ctx context.Context) error {
	attempts := m.ReadyAttempts
	if attempts <= 0 {
		attempts = 180
	}
	interval := m.ReadyInterval
	if interval <= 0 {
		interval = time.Second
	}
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if m.Status(ctx).State == StateReady {
			return nil
		}
		m.sleep(interval)
	}
	return errors.New(i18n.T("k8s.not_ready", m.Distro))
}

// Stop 停止 k3s：停服務 → 在 engine 移除 pod 容器 → 停 cri-dockerd 與掛載（卸載曝露）。
// distro 沒在執行就什麼都不做（殘留的曝露由 expose GC 清掉）。
func (m *Manager) Stop(ctx context.Context) error {
	state, err := m.distroState(ctx, m.Distro)
	if err != nil {
		return err
	}
	if !strings.EqualFold(state, "Running") {
		return nil
	}
	if _, err := m.in(ctx, m.Distro, nil, "systemctl", "stop", "k3s"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.stop_failed", m.Distro), err)
	}
	if m.EngineDistro != "" {
		if es, _ := m.distroState(ctx, m.EngineDistro); strings.EqualFold(es, "Running") {
			_, _ = m.in(ctx, m.EngineDistro, nil, "bash", "-c", removePodsScript)
		}
	}
	if _, err := m.in(ctx, m.Distro, nil, "systemctl", "stop", "wsld-cri-dockerd", "wsld-k8s-mounts"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.stop_failed", m.Distro), err)
	}
	return nil
}

// Restart 停止再啟動 k3s（pod 容器會由 kubelet 重建）。
func (m *Manager) Restart(ctx context.Context) ([]byte, error) {
	m.progress(StepStop)
	if err := m.Stop(ctx); err != nil {
		return nil, &StepError{StepStop, err}
	}
	return m.Start(ctx)
}

// Reset 清除叢集（所有資源與 PV）後重新啟動；k8s-engine、映像檔與憑證保留。
func (m *Manager) Reset(ctx context.Context) ([]byte, error) {
	m.progress(StepStop)
	if err := m.Stop(ctx); err != nil {
		return nil, &StepError{StepStop, err}
	}
	if state, err := m.distroState(ctx, m.Distro); err != nil {
		return nil, &StepError{StepWipe, err}
	} else if state == "" {
		return m.Enable(ctx) // 還沒有 distro：等同全新開啟
	}
	m.progress(StepWipe)
	if err := m.runScript(ctx, m.Distro, "reset"); err != nil {
		return nil, &StepError{StepWipe, fmt.Errorf("%s: %w", i18n.T("k8s.reset_failed", m.Distro), err)}
	}
	return m.Start(ctx)
}

// Disable 停止 k3s，並讓 engine 不再分享容器目錄。k8s-engine 與叢集資料保留（再次開啟會沿用）。
func (m *Manager) Disable(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	if m.EngineDistro == "" {
		return nil
	}
	if es, _ := m.distroState(ctx, m.EngineDistro); !strings.EqualFold(es, "Running") {
		return nil
	}
	if err := m.runScript(ctx, m.EngineDistro, "unshare-engine"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.unshare_failed", m.EngineDistro), err)
	}
	return nil
}

// Kubeconfig 讀出 k3s 的 kubeconfig，並把 default 改名為 ContextName（避免和使用者既有的 default 撞名）。
func (m *Manager) Kubeconfig(ctx context.Context) ([]byte, error) {
	out, err := m.in(ctx, m.Distro, nil, "cat", k3sKubeconfig)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("k8s.kubeconfig_failed", m.Distro), err)
	}
	return RenameKubeconfig([]byte(out), ContextName), nil
}

// RenameKubeconfig 把 k3s kubeconfig 中名為 default 的 cluster／user／context 改成 name。
func RenameKubeconfig(kc []byte, name string) []byte {
	lines := strings.Split(string(kc), "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		for _, key := range []string{"name:", "cluster:", "user:", "current-context:"} {
			k := strings.TrimPrefix(t, "- ")
			if k == key+" default" {
				lines[i] = strings.Replace(l, key+" default", key+" "+name, 1)
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// Node 是 node 的摘要。
type Node struct {
	Name    string
	Ready   bool
	Version string
	IP      string
}

// ParseNode 取 `kubectl get nodes -o json` 的第一個 node。
func ParseNode(js string) (Node, error) {
	var v struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return Node{}, err
	}
	if len(v.Items) == 0 {
		return Node{}, errors.New("no nodes")
	}
	it := v.Items[0]
	n := Node{Name: it.Metadata.Name, Version: it.Status.NodeInfo.KubeletVersion}
	for _, a := range it.Status.Addresses {
		if a.Type == "InternalIP" {
			n.IP = a.Address
		}
	}
	for _, c := range it.Status.Conditions {
		if c.Type == "Ready" {
			n.Ready = c.Status == "True"
		}
	}
	return n, nil
}
