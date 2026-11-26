package k8s

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/iridance/wsld/internal/i18n"
)

// kubectl port-forward 在這裡不能用（cri-dockerd 沒有 CNI，拿不到 pod 的 netns）。替代：在 engine 以 host 網路
// 跑一個 socat 容器，在 127.0.0.1:<本機埠> 監聽（WSL 的 localhost 轉發讓 Windows 也連得到），轉到 ClusterIP／Pod IP。
const (
	// ForwardImage 是轉發用的映像檔。
	ForwardImage = "alpine/socat"
	// LabelForward 標記轉發容器，值為 <namespace>/<svc|pod>/<名稱>:<遠端埠>。
	LabelForward      = "wsld.k8s.forward"
	labelForwardLocal = "wsld.k8s.forward.local"
	forwardPrefix     = "wsld-k8s-fwd-"
)

// PortForward 是一個轉發。
type PortForward struct {
	Namespace  string `json:"namespace"`
	Kind       string `json:"kind"` // svc｜pod
	Name       string `json:"name"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Running    bool   `json:"running"`
	URL        string `json:"url"`
}

// ForwardRequest 是新增轉發的參數。RemotePort 0 = 與 LocalPort 相同。
type ForwardRequest struct {
	Namespace  string `json:"namespace"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
}

// ParseForwardTarget 解析 CLI 的 <namespace>/<svc|pod>/<名稱>（namespace 可省略 = default）。
func ParseForwardTarget(s string) (ns, kind, name string, err error) {
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 2:
		ns, kind, name = "default", parts[0], parts[1]
	case 3:
		ns, kind, name = parts[0], parts[1], parts[2]
	default:
		return "", "", "", errors.New(i18n.T("k8s.bad_target", s))
	}
	switch kind {
	case "svc", "service", "services":
		kind = "svc"
	case "pod", "pods", "po":
		kind = "pod"
	default:
		return "", "", "", errors.New(i18n.T("k8s.bad_target", s))
	}
	return ns, kind, name, nil
}

// ParsePortPair 解析 <本機>[:<遠端>]。
func ParsePortPair(s string) (local, remote int, err error) {
	l, r, has := strings.Cut(s, ":")
	if local, err = strconv.Atoi(l); err != nil || !validPort(local) {
		return 0, 0, errors.New(i18n.T("k8s.bad_port", s))
	}
	remote = local
	if has {
		if remote, err = strconv.Atoi(r); err != nil || !validPort(remote) {
			return 0, 0, errors.New(i18n.T("k8s.bad_port", s))
		}
	}
	return local, remote, nil
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

// Forward 建立轉發（同一本機埠已有轉發時先移除）。
func (m *Manager) Forward(ctx context.Context, req ForwardRequest) (PortForward, error) {
	if req.RemotePort == 0 {
		req.RemotePort = req.LocalPort
	}
	if req.Kind != "svc" && req.Kind != "pod" {
		return PortForward{}, errors.New(i18n.T("k8s.bad_target", req.Kind))
	}
	if !ValidName(req.Namespace) || !ValidName(req.Name) {
		return PortForward{}, errors.New(i18n.T("k8s.bad_name"))
	}
	if !validPort(req.LocalPort) || !validPort(req.RemotePort) {
		return PortForward{}, errors.New(i18n.T("k8s.bad_port", strconv.Itoa(req.LocalPort)))
	}
	if m.EngineDistro == "" {
		return PortForward{}, errors.New(i18n.T("k8s.no_engine"))
	}
	ip, err := m.resolveTarget(ctx, req.Namespace, req.Kind, req.Name)
	if err != nil {
		return PortForward{}, err
	}
	local := strconv.Itoa(req.LocalPort)
	_, _ = m.in(ctx, m.EngineDistro, nil, "docker", "rm", "-f", forwardPrefix+local)
	label := req.Namespace + "/" + req.Kind + "/" + req.Name + ":" + strconv.Itoa(req.RemotePort)
	if _, err := m.in(ctx, m.EngineDistro, nil, "docker", "run", "-d", "--name", forwardPrefix+local,
		"--network", "host", "--label", LabelForward+"="+label, "--label", labelForwardLocal+"="+local,
		ForwardImage, "TCP-LISTEN:"+local+",bind=127.0.0.1,fork,reuseaddr", "TCP:"+ip+":"+strconv.Itoa(req.RemotePort)); err != nil {
		return PortForward{}, fmt.Errorf("%s: %w", i18n.T("k8s.forward_failed", label), err)
	}
	return PortForward{
		Namespace: req.Namespace, Kind: req.Kind, Name: req.Name, LocalPort: req.LocalPort, RemotePort: req.RemotePort,
		Running: true, URL: LocalURL(req.LocalPort),
	}, nil
}

// resolveTarget 取 service 的 ClusterIP 或 pod 的 IP。
func (m *Manager) resolveTarget(ctx context.Context, ns, kind, name string) (string, error) {
	res := "service"
	if kind == "pod" {
		res = "pod"
	}
	out, err := m.kubectl(ctx, "get", res, "-n", ns, name, "-o", "json", "--request-timeout=10s")
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("k8s.target_not_found", ns+"/"+kind+"/"+name), err)
	}
	var v struct {
		Spec struct {
			ClusterIP string `json:"clusterIP"`
		} `json:"spec"`
		Status struct {
			PodIP string `json:"podIP"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", err
	}
	ip := v.Status.PodIP
	if kind == "svc" {
		ip = v.Spec.ClusterIP
	}
	if ip == "" || ip == "None" {
		return "", errors.New(i18n.T("k8s.target_no_ip", ns+"/"+kind+"/"+name))
	}
	return ip, nil
}

// ListForwards 列出轉發容器。
func (m *Manager) ListForwards(ctx context.Context) ([]PortForward, error) {
	if m.EngineDistro == "" {
		return []PortForward{}, nil
	}
	out, err := m.in(ctx, m.EngineDistro, nil, "docker", "ps", "-a", "--filter", "label="+LabelForward, "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("k8s.forward_list_failed"), err)
	}
	return ParseForwards(out), nil
}

// ParseForwards 解析 `docker ps --format json`（每行一個 JSON；Labels 是 a=b,c=d 字串）。
func ParseForwards(out string) []PortForward {
	res := []PortForward{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var row struct {
			Labels string `json:"Labels"`
			State  string `json:"State"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(sc.Text())), &row) != nil {
			continue
		}
		labels := map[string]string{}
		for _, kv := range strings.Split(row.Labels, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				labels[k] = v
			}
		}
		target, remote, _ := strings.Cut(labels[LabelForward], ":")
		parts := strings.Split(target, "/")
		local, _ := strconv.Atoi(labels[labelForwardLocal])
		rp, _ := strconv.Atoi(remote)
		if len(parts) != 3 || local == 0 {
			continue
		}
		res = append(res, PortForward{
			Namespace: parts[0], Kind: parts[1], Name: parts[2], LocalPort: local, RemotePort: rp,
			Running: row.State == "running", URL: LocalURL(local),
		})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].LocalPort < res[j].LocalPort })
	return res
}

// StopForward 移除本機埠上的轉發。
func (m *Manager) StopForward(ctx context.Context, localPort int) error {
	if !validPort(localPort) {
		return errors.New(i18n.T("k8s.bad_port", strconv.Itoa(localPort)))
	}
	if m.EngineDistro == "" {
		return errors.New(i18n.T("k8s.no_engine"))
	}
	if _, err := m.in(ctx, m.EngineDistro, nil, "docker", "rm", "-f", forwardPrefix+strconv.Itoa(localPort)); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.forward_stop_failed", strconv.Itoa(localPort)), err)
	}
	return nil
}
