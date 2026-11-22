package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultNamespace 是 Deploy 未指定 namespace 時使用的 namespace。
const DefaultNamespace = "default"

// MaxApplyBytes 是 Apply 接受的 YAML／JSON 大小上限。
const MaxApplyBytes = 1 << 20

// imageRe 接受 `name[:tag][@sha256:…]` 與 registry／路徑（`host:5000/a/b:tag`）。
// 第一個字元必為英數，避免被 kubectl 當成旗標。
var imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)

// 對外方式（DeploySpec.Expose）。
const (
	ExposeNone         = "none"         // 只建 Deployment，不建 Service
	ExposeNodePort     = "nodeport"     // NodePort Service，埠由 k8s 配發（30000–32767）
	ExposeLoadBalancer = "loadbalancer" // LoadBalancer Service，localhost 上用指定的埠
)

// hostPortScan 是指定埠被占用時，往後最多嘗試的埠數。
const hostPortScan = 20

// DeploySpec 描述 GUI「Deploy」要建立的一個 Deployment（＋可選的 NodePort Service）。
type DeploySpec struct {
	Image     string `json:"image"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Port      int    `json:"port,omitempty"` // 容器埠；0 = 不建立 Service
	// Expose：none | nodeport | loadbalancer。空值向下相容：有 Port → nodeport，否則 none。
	Expose string `json:"expose,omitempty"`
	// HostPort：Expose = loadbalancer 時 Windows localhost 上想用的埠（預設與容器埠相同）。
	HostPort int `json:"host_port,omitempty"`
	Replicas int `json:"replicas,omitempty"` // 0 → 1
}

// DeployResult 是 Deploy 的結果。
type DeployResult struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	NodePort  int    `json:"node_port,omitempty"` // Expose = nodeport 時，k8s 配發的 NodePort
	Expose    string `json:"expose"`
	// HostPort 是 Expose = loadbalancer 時實際使用的 localhost 埠；與要求不同（被占用而順延）時 PortChanged = true。
	HostPort    int  `json:"host_port,omitempty"`
	PortChanged bool `json:"port_changed,omitempty"`
}

// Normalize 驗證並補上預設值。
func (s DeploySpec) Normalize() (DeploySpec, error) {
	if s.Namespace == "" {
		s.Namespace = DefaultNamespace
	}
	if s.Replicas == 0 {
		s.Replicas = 1
	}
	if s.Expose == "" {
		s.Expose = ExposeNone
		if s.Port > 0 {
			s.Expose = ExposeNodePort
		}
	}
	switch s.Expose {
	case ExposeNone, ExposeNodePort, ExposeLoadBalancer:
	default:
		return s, fmt.Errorf("invalid expose %q", s.Expose)
	}
	if s.Expose != ExposeNone && s.Port < 1 {
		return s, errors.New("a container port is required to expose the app")
	}
	if s.Expose == ExposeLoadBalancer {
		if s.HostPort == 0 {
			s.HostPort = s.Port
		}
		if s.HostPort < 1 || s.HostPort > 65535 {
			return s, errors.New("host port must be between 1 and 65535")
		}
	}
	if !ValidName(s.Namespace) {
		return s, fmt.Errorf("invalid namespace %q", s.Namespace)
	}
	// Deployment／Service 名稱須為 DNS-1035 標籤（≤ 63 字元）；比一般物件名稱更嚴格。
	if !ValidName(s.Name) || len(s.Name) > 63 || strings.Contains(s.Name, ".") {
		return s, fmt.Errorf("invalid name %q", s.Name)
	}
	if !imageRe.MatchString(s.Image) {
		return s, fmt.Errorf("invalid image %q", s.Image)
	}
	if s.Port < 0 || s.Port > 65535 {
		return s, errors.New("port must be between 1 and 65535")
	}
	if s.Replicas < 1 || s.Replicas > MaxReplicas {
		return s, fmt.Errorf("replicas must be between 1 and %d", MaxReplicas)
	}
	return s, nil
}

// DeployManifest 產生 kubectl apply 用的 List（JSON）。映像用 IfNotPresent：
// `--docker` 的 k3s 與 dockerd 共用映像，本機 build 的映像直接可用，沒有的才會去 registry 拉。
func DeployManifest(s DeploySpec) ([]byte, error) {
	labels := map[string]string{"app": s.Name}
	container := map[string]any{"name": s.Name, "image": s.Image, "imagePullPolicy": "IfNotPresent"}
	if s.Port > 0 { // 容器埠一律宣告（供 Expose 事後帶入）
		container["ports"] = []any{map[string]any{"containerPort": s.Port}}
	}
	items := []any{
		map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": s.Name, "namespace": s.Namespace, "labels": labels},
			"spec": map[string]any{
				"replicas": s.Replicas,
				"selector": map[string]any{"matchLabels": labels},
				"template": map[string]any{
					"metadata": map[string]any{"labels": labels},
					"spec":     map[string]any{"containers": []any{container}},
				},
			},
		},
	}
	switch s.Expose {
	case ExposeNodePort:
		items = append(items, serviceItem(s, "NodePort", s.Port))
	case ExposeLoadBalancer:
		items = append(items, serviceItem(s, "LoadBalancer", s.HostPort))
	}
	return json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
}

// serviceItem 產生對外 Service：port 為 Service 埠（LoadBalancer 即 localhost 上的埠），targetPort 為容器埠。
func serviceItem(s DeploySpec, typ string, port int) map[string]any {
	labels := map[string]string{"app": s.Name}
	return map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": s.Name, "namespace": s.Namespace, "labels": labels},
		"spec": map[string]any{
			"type":     typ,
			"selector": labels,
			"ports":    []any{map[string]any{"port": port, "targetPort": s.Port}},
		},
	}
}

// pickHostPort 從想要的埠起，找第一個沒被占用的 localhost 埠：
// 排除其他 LoadBalancer Service 已綁的埠（k3s ServiceLB 用 hostPort，同埠會 Pending）與 Windows 端已被占用的埠。
// 同名 Service 自己目前用的埠視為可用（重新 Deploy／Expose 不會把自己擠掉）。
func (m *Manager) pickHostPort(ctx context.Context, s DeploySpec) int {
	used, own := map[int]bool{}, map[int]bool{}
	if svcs, err := m.ListServices(ctx, ""); err == nil {
		for _, sv := range svcs {
			if sv.Type != "LoadBalancer" {
				continue
			}
			for _, p := range sv.Ports {
				if sv.Namespace == s.Namespace && sv.Name == s.Name {
					own[p.Port] = true
				} else {
					used[p.Port] = true
				}
			}
		}
	}
	busy := m.PortBusy
	if busy == nil {
		busy = portBusy
	}
	for p := s.HostPort; p < s.HostPort+hostPortScan && p <= 65535; p++ {
		if own[p] || (!used[p] && !busy(p)) {
			return p
		}
	}
	return s.HostPort
}

// Deploy 建立（或更新）一個 Deployment 與可選的 NodePort Service，回傳配發的 NodePort。
// namespace 不存在時先建立（冪等）。
func (m *Manager) Deploy(ctx context.Context, spec DeploySpec) (DeployResult, error) {
	s, err := spec.Normalize()
	if err != nil {
		return DeployResult{}, err
	}
	requested := s.HostPort
	if s.Expose == ExposeLoadBalancer {
		s.HostPort = m.pickHostPort(ctx, s)
	}
	manifest, err := DeployManifest(s)
	if err != nil {
		return DeployResult{}, err
	}
	if s.Namespace != DefaultNamespace {
		// 以 apply 建 namespace：已存在不會報錯。
		ns, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": s.Namespace}})
		if _, err := m.root(ctx, bytes.NewReader(ns), "k3s", "kubectl", "apply", "-f", "-"); err != nil {
			return DeployResult{}, err
		}
	}
	if _, err := m.root(ctx, bytes.NewReader(manifest), "k3s", "kubectl", "apply", "-f", "-"); err != nil {
		return DeployResult{}, err
	}
	res := DeployResult{Namespace: s.Namespace, Name: s.Name, Expose: s.Expose}
	if s.Expose == ExposeLoadBalancer {
		res.HostPort, res.PortChanged = s.HostPort, s.HostPort != requested
	}
	if s.Expose == ExposeNodePort {
		out, err := m.root(ctx, nil, "k3s", "kubectl", "get", "service", s.Name, "-n", s.Namespace, "-o", "json")
		if err != nil {
			return res, err
		}
		// 單一物件不是 List：包成 items 交給既有解析器。
		svcs, err := ParseServices(`{"items":[` + out + `]}`)
		if err == nil && len(svcs) == 1 && len(svcs[0].Ports) > 0 {
			res.NodePort = svcs[0].Ports[0].NodePort
		}
	}
	return res, nil
}

// Apply 以 `kubectl apply -f -` 套用使用者提供的 YAML／JSON，回傳 kubectl 的輸出
// （每行一個 `kind/name created|configured|unchanged`）。
func (m *Manager) Apply(ctx context.Context, manifest []byte) (string, error) {
	if len(bytes.TrimSpace(manifest)) == 0 {
		return "", errors.New("empty manifest")
	}
	if len(manifest) > MaxApplyBytes {
		return "", fmt.Errorf("manifest is larger than %d bytes", MaxApplyBytes)
	}
	return m.root(ctx, bytes.NewReader(manifest), "k3s", "kubectl", "apply", "-f", "-")
}
