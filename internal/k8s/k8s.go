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

// k3s 在 distro 內的固定路徑（官方安裝腳本決定）。
const (
	k3sBin       = "/usr/local/bin/k3s"
	k3sKillall   = "/usr/local/bin/k3s-killall.sh"
	k3sKubeconf  = "/etc/rancher/k3s/k3s.yaml"
	k3sScriptTmp = "/tmp/wsld-k3s.sh"
)

// MachineKubeconfigPath 是機器內 k3s 產生的 kubeconfig（正本）；Windows 那份只在使用者手動匯出時才有。
const MachineKubeconfigPath = k3sKubeconf

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

	ReadyAttempts int           // 預設 120
	ReadyInterval time.Duration // 預設 1s
	Sleep         func(time.Duration)
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
	st := Status{Distro: m.Distro, State: StateStopped, Endpoint: APIServer, Context: ContextName(m.Distro)}
	list, err := wsl.ListDistros(ctx, m.WSL)
	if err != nil {
		st.State = StateUnknown
		return st
	}
	d, ok := wsl.FindDistro(list, m.Distro)
	if !ok || !strings.EqualFold(d.State, "Running") {
		return st
	}
	if _, err := m.root(ctx, nil, "test", "-x", k3sBin); err != nil {
		// 只有 test 本身以 1 結束才代表檔案不存在；wsl.exe 逾時等其他錯誤是「不知道」。
		if wsl.ExitCode(err) == 1 {
			st.State = StateNotInstalled
		} else {
			st.State = StateUnknown
		}
		return st
	}
	st.Installed = true
	// is-active 未啟動時以非 0 結束但仍會印出狀態字，所以只看輸出；沒有輸出＝查詢失敗。
	out, _ := m.root(ctx, nil, "systemctl", "is-active", "k3s")
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
	if out, err := m.root(ctx, nil, "k3s", "kubectl", "get", "--raw", "/readyz"); err != nil || strings.TrimSpace(out) != "ok" {
		return st
	}
	st.Ready = true
	if out, err := m.root(ctx, nil, "k3s", "kubectl", "get", "nodes", "-o", "json"); err == nil {
		if n, err := ParseNode(out); err == nil {
			st.NodeName, st.NodeReady, st.Version, st.NodeIP = n.Name, n.Ready, n.Version, n.IP
		}
	}
	if out, err := m.root(ctx, nil, "systemctl", "show", "k3s", "-p", "MemoryCurrent", "--value"); err == nil {
		st.MemoryBytes = parseMemory(out)
	}
	if out, err := m.root(ctx, nil, "hostname", "-I"); err == nil {
		if eth := strings.Fields(out); len(eth) > 0 && st.NodeIP != "" && eth[0] != st.NodeIP {
			st.IPMismatch = true
		}
	}
	if st.NodeReady {
		st.State = StateReady
	}
	return st
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
	if err := m.runScript(ctx, "install"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.install_failed", m.Distro), err)
	}
	return nil
}

// Uninstall 完整移除 k3s（k3s-uninstall.sh 與 drop-in）。
func (m *Manager) Uninstall(ctx context.Context) error {
	if err := m.runScript(ctx, "uninstall"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.uninstall_failed", m.Distro), err)
	}
	return nil
}

// Start 啟動 k3s：未安裝則先安裝 → 確認 dockerd → systemctl start → 等 node Ready。
func (m *Manager) Start(ctx context.Context) error {
	if _, err := m.root(ctx, nil, "test", "-x", k3sBin); err != nil {
		m.print(i18n.T("k8s.installing", m.Distro))
		if err := m.Install(ctx); err != nil {
			return err
		}
	}
	if out, _ := m.root(ctx, nil, "systemctl", "is-active", "docker"); strings.TrimSpace(out) != "active" {
		_, _ = m.root(ctx, nil, "systemctl", "reset-failed", "docker")
		if _, err := m.root(ctx, nil, "systemctl", "start", "docker"); err != nil {
			return fmt.Errorf("%s: %w", i18n.T("k8s.docker_start_failed", m.Distro), err)
		}
	}
	// 之前連續失敗會觸發 start limit，start 會直接被拒；先 reset-failed。
	_, _ = m.root(ctx, nil, "systemctl", "reset-failed", "k3s")
	if _, err := m.root(ctx, nil, "systemctl", "start", "k3s"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.start_failed", m.Distro), err)
	}
	return m.waitReady(ctx)
}

func (m *Manager) waitReady(ctx context.Context) error {
	for i := 0; i < m.readyAttempts(); i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if out, err := m.root(ctx, nil, "k3s", "kubectl", "get", "--raw", "/readyz"); err == nil && strings.TrimSpace(out) == "ok" {
			if js, err := m.root(ctx, nil, "k3s", "kubectl", "get", "nodes", "-o", "json"); err == nil {
				if n, err := ParseNode(js); err == nil && n.Ready {
					return nil
				}
			}
		}
		m.sleep(m.readyInterval())
	}
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
	if _, err := m.root(ctx, nil, k3sKillall); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.stop_failed", m.Distro), err)
	}
	_, _ = m.root(ctx, nil, "bash", "-c", RemoveContainersScript)
	return nil
}

// Reset 還原為乾淨叢集：停止 → 清除 k3s／kubelet 狀態 → 重新啟動。
func (m *Manager) Reset(ctx context.Context) error {
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
