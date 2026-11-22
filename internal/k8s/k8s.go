package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/wsl"
)

// k3s 在 distro 內的固定路徑（官方安裝腳本決定）。
const (
	k3sBin       = "/usr/local/bin/k3s"
	k3sKillall   = "/usr/local/bin/k3s-killall.sh"
	k3sKubeconf  = "/etc/rancher/k3s/k3s.yaml"
	k3sScriptTmp = "/tmp/wsld-k3s.sh"
)

// State 是 k3s 的概括狀態（GUI 徽章／CLI status）。
type State string

const (
	StateStopped      State = "stopped"       // distro 未執行（不去喚醒它）
	StateNotInstalled State = "not-installed" // 尚未安裝 k3s
	StateOff          State = "off"           // 已安裝、服務未啟動
	StateStarting     State = "starting"      // 服務 active 但 API／node 尚未 Ready
	StateReady        State = "ready"
	StateError        State = "error"   // systemd unit failed
	StateUnknown      State = "unknown" // 查詢失敗（wsl.exe 逾時等），無法判斷；呼叫端應沿用上一次狀態
)

// Status 是單台 machine 的 k3s 狀態。
type Status struct {
	Distro      string `json:"distro"`
	State       State  `json:"state"`
	Installed   bool   `json:"installed"`
	Active      bool   `json:"active"`
	Ready       bool   `json:"ready"`
	Version     string `json:"version,omitempty"`
	NodeName    string `json:"node_name,omitempty"`
	NodeReady   bool   `json:"node_ready"`
	NodeIP      string `json:"node_ip,omitempty"`
	IPMismatch  bool   `json:"ip_mismatch,omitempty"` // node InternalIP ≠ 目前 eth0：WSL 重啟後 IP 漂移
	MemoryBytes int64  `json:"memory_bytes,omitempty"`
	Endpoint    string `json:"endpoint"`
	Context     string `json:"context"`
}

// Manager 管理單一 distro 內的 k3s。
type Manager struct {
	WSL    wsl.Runner
	Distro string
	Bundle []byte    // scripts.K3sBundle()
	Output io.Writer // 安裝輸出；nil 則丟棄

	APIPort  int                 // 新安裝 k3s 的 API 埠偏好；0 = DefaultAPIPort。已安裝的 k3s 不會改埠
	PortBusy func(port int) bool // Windows 端該埠是否已被占用（預設以 127.0.0.1 撥號探測）

	DisableAPI bool // true = 不走 TCP API，全部經 kubectl（測試用）
	// AsyncProbes：true = 記憶體／IP 過期後背景更新、Status 不等待（GUI 長駐輪詢用）。
	// 預設同步：CLI／doctor 是一次性查詢，要拿到完整結果。
	AsyncProbes bool

	apiMu sync.Mutex
	apiC  *apiConn
	// apiDownUntil：API 失敗後的冷卻期，期間直接走 kubectl，避免每次輪詢都重試（並多讀一次 kubeconfig）。
	apiDownUntil time.Time

	// 輪詢用的快取：少打 wsl.exe（每次都要 3 秒以上，WSL 忙時更久）。
	probeMu     sync.Mutex
	installedAt time.Time // 最近一次確認 k3s 已安裝的時間
	memAt       time.Time
	memCache    string
	memBusy     bool
	ipAt        time.Time
	ipOut       string
	ipBusy      bool

	ReadyAttempts int           // 預設 120
	ReadyInterval time.Duration // 預設 1s
	Sleep         func(time.Duration)
}

// apiPort 回傳偏好的 API 埠。
func (m *Manager) apiPort() int { return APIPort(m.APIPort) }

// APIPort 把設定值正規化：1024–65535 以外（含 0）視為未設定，回 DefaultAPIPort。
func APIPort(configured int) int {
	if configured >= 1024 && configured <= 65535 {
		return configured
	}
	return DefaultAPIPort
}

// pickAPIPort 從偏好埠起找第一個 Windows 端沒人占用的埠（最多往後試 20 個）。
func (m *Manager) pickAPIPort() int {
	busy := m.PortBusy
	if busy == nil {
		busy = portBusy
	}
	base := m.apiPort()
	for p := base; p < base+20 && p <= 65535; p++ {
		if !busy(p) {
			return p
		}
	}
	return base
}

func portBusy(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (m *Manager) sleep(d time.Duration) {
	if m.Sleep != nil {
		m.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (m *Manager) readyAttempts() int {
	if m.ReadyAttempts > 0 {
		return m.ReadyAttempts
	}
	return 120
}

func (m *Manager) readyInterval() time.Duration {
	if m.ReadyInterval > 0 {
		return m.ReadyInterval
	}
	return time.Second
}

// root 在 distro 內以 root 執行（k3s／systemctl 都需要）。
func (m *Manager) root(ctx context.Context, stdin io.Reader, cmd ...string) (string, error) {
	out, stderr, err := wsl.RunIn(ctx, m.WSL, m.Distro, "root", stdin, cmd...)
	if err != nil {
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return string(out), fmt.Errorf("%w: %s", err, msg)
		}
	}
	return string(out), err
}

func (m *Manager) print(s string) {
	if m.Output != nil {
		fmt.Fprintln(m.Output, s)
	}
}

// Status 盡力收集狀態。distro 未執行時不喚醒它（GUI 會輪詢）。
func (m *Manager) Status(ctx context.Context) Status {
	st := Status{Distro: m.Distro, State: StateStopped, Endpoint: APIServer(m.apiPort()), Context: ContextName(m.Distro)}
	// 快速路徑：憑證已在記憶體、API 的 /readyz 直接回 ok，就代表 distro 在跑、k3s 已安裝且 active，
	// 一個 wsl.exe 都不必打。任何失敗都落回下面的完整探測。
	fast := false
	if m.hasConn() {
		if out, ok := m.apiGet(ctx, "/readyz"); ok && strings.TrimSpace(out) == "ok" {
			fast = true
		}
	}
	if fast {
		st.Installed, st.Active, st.State = true, true, StateStarting
		m.markInstalled()
	} else {
		list, err := wsl.ListDistros(ctx, m.WSL)
		if err != nil {
			st.State = StateUnknown
			return st
		}
		d, ok := wsl.FindDistro(list, m.Distro)
		if !ok || !strings.EqualFold(d.State, "Running") {
			return st
		}
		if !m.installedRecently() {
			if _, err := m.root(ctx, nil, "test", "-x", k3sBin); err != nil {
				// 只有 test 本身以 1 結束才代表檔案不存在；wsl.exe 逾時等其他錯誤是「不知道」。
				if wsl.ExitCode(err) == 1 {
					st.State = StateNotInstalled
				} else {
					st.State = StateUnknown
				}
				return st
			}
			m.markInstalled()
		}
		st.Installed = true
	}
	// is-active 未啟動時以非 0 結束但仍會印出狀態字，所以只看輸出；沒有輸出＝查詢失敗。
	var out string
	if fast {
		out = "active"
	} else {
		out, _ = m.root(ctx, nil, "systemctl", "is-active", "k3s")
	}
	switch strings.TrimSpace(out) {
	case "active":
		st.Active = true
	case "activating", "reloading":
		// k3s 是 Type=notify：啟動期間為 activating，仍屬啟動中。
	case "failed":
		st.State = StateError
		return st
	case "inactive", "deactivating":
		st.State = StateOff
		return st
	default:
		st.State = StateUnknown
		return st
	}
	st.State = StateStarting
	if !fast {
		if out, err := m.get(ctx, "/readyz", "get", "--raw", "/readyz"); err != nil || strings.TrimSpace(out) != "ok" {
			return st
		}
	}
	st.Ready = true
	// 三個查詢互不相依，每個都是一次 wsl.exe：並行以縮短輪詢耗時。
	var wg sync.WaitGroup
	var nodeOut, memOut, ipOut string
	var nodeErr, memErr, ipErr error
	wg.Add(3)
	go func() {
		defer wg.Done()
		nodeOut, nodeErr = m.get(ctx, "/api/v1/nodes", "get", "nodes", "-o", "json")
	}()
	// 記憶體與 IP 變動慢：各自快取一段時間，不必每次輪詢都開 wsl.exe。
	go func() {
		defer wg.Done()
		memOut, memErr = m.cachedProbe(&m.memAt, &m.memCache, &m.memBusy, memTTL, func(c context.Context) (string, error) {
			return m.root(c, nil, "systemctl", "show", "k3s", "-p", "MemoryCurrent", "--value")
		})
	}()
	go func() {
		defer wg.Done()
		ipOut, ipErr = m.cachedProbe(&m.ipAt, &m.ipOut, &m.ipBusy, ipTTL, func(c context.Context) (string, error) { return m.root(c, nil, "hostname", "-I") })
	}()
	wg.Wait()
	if nodeErr == nil {
		if n, err := ParseNode(nodeOut); err == nil {
			st.NodeName, st.NodeReady, st.Version, st.NodeIP = n.Name, n.Ready, n.Version, n.IP
		}
	}
	if memErr == nil {
		st.MemoryBytes = parseMemory(memOut)
	}
	if ipErr == nil {
		if eth := strings.Fields(ipOut); len(eth) > 0 && st.NodeIP != "" && eth[0] != st.NodeIP {
			st.IPMismatch = true
		}
	}
	if st.NodeReady {
		st.State = StateReady
	}
	return st
}

const (
	memTTL       = 30 * time.Second
	ipTTL        = 60 * time.Second
	installedTTL = 60 * time.Second
)

// cachedProbe 是「過期就背景更新」的快取：Status 絕不等待它。
//   - ttl 內：直接回傳快取值。
//   - 過期：先回傳舊值，並在背景重查（同一時間只有一個重查在跑）。
//   - 從未成功過：回傳錯誤（呼叫端略過該欄位），同樣在背景重查。
//
// 記憶體與 IP 只是顯示用；WSL 偶爾讓單次 wsl.exe 卡 30 秒（0xFFFFFFFF 重試），
// 不能讓整個 Status（也就是整個畫面更新）等它們。
func (m *Manager) cachedProbe(at *time.Time, val *string, busy *bool, ttl time.Duration, fn func(context.Context) (string, error)) (string, error) {
	m.probeMu.Lock()
	have := !at.IsZero()
	v := *val
	if have && time.Since(*at) < ttl {
		m.probeMu.Unlock()
		return v, nil
	}
	if !m.AsyncProbes {
		m.probeMu.Unlock()
		out, err := fn(context.Background())
		if err == nil {
			m.probeMu.Lock()
			*at, *val = time.Now(), out
			m.probeMu.Unlock()
		}
		return out, err
	}
	if !*busy {
		*busy = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			out, err := fn(ctx)
			m.probeMu.Lock()
			defer m.probeMu.Unlock()
			*busy = false
			if err == nil {
				*at, *val = time.Now(), out
			}
		}()
	}
	m.probeMu.Unlock()
	if have {
		return v, nil
	}
	return "", errProbePending
}

var errProbePending = errors.New("probe pending")

func (m *Manager) installedRecently() bool {
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	return !m.installedAt.IsZero() && time.Since(m.installedAt) < installedTTL
}

func (m *Manager) markInstalled() {
	m.probeMu.Lock()
	m.installedAt = time.Now()
	m.probeMu.Unlock()
}

// resetProbes 清掉輪詢快取（安裝／移除／reset／啟停後狀態會變）。
func (m *Manager) resetProbes() {
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	m.installedAt, m.memAt, m.ipAt = time.Time{}, time.Time{}, time.Time{}
}

// Node 是 `kubectl get nodes -o json` 的精簡結果（單節點）。
type Node struct {
	Name    string
	Ready   bool
	Version string
	IP      string
}

// ParseNode 取第一個 node。
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

// parseMemory 解析 `systemctl show -p MemoryCurrent --value`（"[not set]" → 0）。
func parseMemory(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// runScript 把 bundle 寫入 distro 暫存再執行（避免 bash -s 的 stdin 被 systemctl 等吞掉）。
func (m *Manager) runScript(ctx context.Context, mode string) error {
	if len(m.Bundle) == 0 {
		return errors.New(i18n.T("k8s.no_bundle"))
	}
	out, err := m.root(ctx, bytes.NewReader(m.Bundle), "bash", "-c", "cat > "+k3sScriptTmp+" && bash "+k3sScriptTmp+" "+mode)
	if m.Output != nil {
		_, _ = io.WriteString(m.Output, out)
	}
	return err
}

// Install 安裝 k3s（unit 預設不啟用；冪等）。
func (m *Manager) Install(ctx context.Context) error {
	m.ResetAPI() // 憑證會隨安裝／reset 改變
	if err := m.runScript(ctx, "install "+strconv.Itoa(m.pickAPIPort())); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.install_failed", m.Distro), err)
	}
	return nil
}

// Uninstall 完整移除 k3s（k3s-uninstall.sh 與 drop-in）。
func (m *Manager) Uninstall(ctx context.Context) error {
	m.ResetAPI() // 憑證會隨安裝／reset 改變
	if err := m.runScript(ctx, "uninstall"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.uninstall_failed", m.Distro), err)
	}
	return nil
}

// Start 啟動 k3s：確認 dockerd → 未安裝則先安裝 → systemctl start → 等 node Ready。
// ctx 帶 WithProgress 時，會依序回報 StepEngine／StepInstall／StepNode。
func (m *Manager) Start(ctx context.Context) (err error) {
	begin := time.Now()
	defer func() {
		if err != nil {
			log.Printf("k8s start %s: FAILED after %.1fs: %v", m.Distro, time.Since(begin).Seconds(), err)
		} else {
			log.Printf("k8s start %s: ready in %.1fs", m.Distro, time.Since(begin).Seconds())
		}
	}()
	// 每個步驟結束時記下耗時，找出「重開很慢」卡在哪一步（寫入 GUI 的 crash.log）。
	step := time.Now()
	lap := func(name string) {
		log.Printf("k8s start %s: %s done in %.1fs", m.Distro, name, time.Since(step).Seconds())
		step = time.Now()
	}
	m.ResetAPI() // 憑證會隨安裝／reset 改變
	ReportStep(ctx, StepEngine)
	if out, _ := m.root(ctx, nil, "systemctl", "is-active", "docker"); strings.TrimSpace(out) != "active" {
		_, _ = m.root(ctx, nil, "systemctl", "reset-failed", "docker")
		if _, err := m.root(ctx, nil, "systemctl", "start", "docker"); err != nil {
			return fmt.Errorf("%s: %w", i18n.T("k8s.docker_start_failed", m.Distro), err)
		}
	}
	lap("engine")
	ReportStep(ctx, StepInstall)
	if _, err := m.root(ctx, nil, "test", "-x", k3sBin); err != nil {
		m.print(i18n.T("k8s.installing", m.Distro))
		if err := m.Install(ctx); err != nil {
			return err
		}
	}
	lap("install check")
	ReportStep(ctx, StepNode)
	// 之前連續失敗會觸發 start limit，start 會直接被拒；先 reset-failed。
	_, _ = m.root(ctx, nil, "systemctl", "reset-failed", "k3s")
	if _, err := m.root(ctx, nil, "systemctl", "start", "k3s"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.start_failed", m.Distro), err)
	}
	lap("systemctl start k3s")
	return m.waitReady(ctx)
}

func (m *Manager) waitReady(ctx context.Context) error {
	begin := time.Now()
	apiReady := time.Duration(0)
	for i := 0; i < m.readyAttempts(); i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if out, err := m.get(ctx, "/readyz", "get", "--raw", "/readyz"); err == nil && strings.TrimSpace(out) == "ok" {
			if apiReady == 0 {
				apiReady = time.Since(begin)
				log.Printf("k8s start %s: API /readyz ok after %.1fs (%d polls)", m.Distro, apiReady.Seconds(), i+1)
			}
			if js, err := m.get(ctx, "/api/v1/nodes", "get", "nodes", "-o", "json"); err == nil {
				if n, err := ParseNode(js); err == nil && n.Ready {
					log.Printf("k8s start %s: node Ready after %.1fs (%d polls)", m.Distro, time.Since(begin).Seconds(), i+1)
					return nil
				}
			}
		}
		m.sleep(m.readyInterval())
	}
	log.Printf("k8s start %s: gave up waiting after %.1fs (API ready: %v)", m.Distro, time.Since(begin).Seconds(), apiReady > 0)
	return errors.New(i18n.T("k8s.not_ready", m.Distro))
}

// 在 distro 內以 `bash -c` 執行的清理腳本。
const (
	// RemoveContainersScript 移除 kubelet（cri-dockerd）建立的 k8s_* 容器。
	// k3s-killall.sh 不處理 --docker 的 pod 容器（實機驗證：off 之後 k8s_* 仍在執行），須另外移除。
	// 注意：腳本內不可有 $ ` \ "——wsl.exe 的外層 shell 會先展開它們（見 TestBashCScriptsAvoidOuterShellMetacharacters）。
	RemoveContainersScript = `docker ps -aq --filter name=^k8s_ | xargs -r docker rm -f`
	// WipeStateScript 清除叢集狀態。實機：/var/lib/kubelet 是獨立掛載點（ext4），
	// `rm -rf` 該目錄會 "Device or resource busy"，所以只清空內容、不刪目錄本身。
	WipeStateScript = `rm -rf /var/lib/rancher/k3s /etc/rancher/k3s; if [ -d /var/lib/kubelet ]; then find /var/lib/kubelet -mindepth 1 -delete; fi`
)

// Stop 停止 k3s：k3s-killall.sh（停 service、清 netns／iptables／掛載）→ 移除 k8s_* 容器。
// 容器清理盡力而為（dockerd 可能已停）；killall 失敗才算失敗。
func (m *Manager) Stop(ctx context.Context) error {
	m.ResetAPI() // 憑證會隨安裝／reset 改變
	if _, err := m.root(ctx, nil, k3sKillall); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.stop_failed", m.Distro), err)
	}
	_, _ = m.root(ctx, nil, "bash", "-c", RemoveContainersScript)
	return nil
}

// Reset 還原為乾淨叢集：停止 → 清除 k3s／kubelet 狀態 → 重新啟動。
func (m *Manager) Reset(ctx context.Context) error {
	m.ResetAPI() // 憑證會隨安裝／reset 改變
	if err := m.Stop(ctx); err != nil {
		return err
	}
	if _, err := m.root(ctx, nil, "bash", "-c", WipeStateScript); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.reset_failed", m.Distro), err)
	}
	return m.Start(ctx)
}

// Kubeconfig 讀出 k3s 產生的 kubeconfig 原文（尚未合併）。
func (m *Manager) Kubeconfig(ctx context.Context) ([]byte, error) {
	out, err := m.root(ctx, nil, "cat", k3sKubeconf)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("k8s.kubeconfig_failed", m.Distro), err)
	}
	return []byte(out), nil
}

// ParseMeminfo 從 /proc/meminfo 取 MemTotal 與 MemAvailable（bytes）。
func ParseMeminfo(s string) (total, avail int64, err error) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, perr := strconv.ParseInt(f[1], 10, 64)
		if perr != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	if total == 0 {
		return 0, 0, errors.New("MemTotal not found")
	}
	return total, avail, nil
}

// Memory 回傳 WSL VM 的總記憶體與可用記憶體（所有 distro 共用同一個 VM）。
func (m *Manager) Memory(ctx context.Context) (total, avail int64, err error) {
	out, _, err := wsl.RunIn(ctx, m.WSL, m.Distro, "", nil, "cat", "/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	return ParseMeminfo(string(out))
}
