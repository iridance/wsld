package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Pod 是 GUI 列表用的精簡 Pod。
type Pod struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Ready     string `json:"ready"`
	Restarts  int    `json:"restarts"`
	Node      string `json:"node,omitempty"`
	IP        string `json:"ip,omitempty"`
	// Labels 供架構圖以 selector 把 Pod 歸到 Service／Deployment。
	Labels map[string]string `json:"labels,omitempty"`
	// ConfigMaps／PVCs：此 Pod 掛載或引用的 ConfigMap、PersistentVolumeClaim 名稱（架構圖的掛載連線）。
	ConfigMaps []string  `json:"config_maps,omitempty"`
	PVCs       []string  `json:"pvcs,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Deployment 是 GUI 列表用的精簡 Deployment。
type Deployment struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Ready     string `json:"ready"`
	Replicas  int    `json:"replicas"`
	UpToDate  int    `json:"up_to_date"`
	Available int    `json:"available"`
	// Selector 是 spec.selector.matchLabels；Image 是第一個容器的映像。
	Selector map[string]string `json:"selector,omitempty"`
	Image    string            `json:"image,omitempty"`
	// Strategy／MaxSurge／MaxUnavailable：更新策略（RollingUpdate 才有後兩者）。
	Strategy       string    `json:"strategy,omitempty"`
	MaxSurge       string    `json:"max_surge,omitempty"`
	MaxUnavailable string    `json:"max_unavailable,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ServicePort 是 Service 的單一埠。
type ServicePort struct {
	Name     string `json:"name,omitempty"`
	Port     int    `json:"port"`
	NodePort int    `json:"node_port,omitempty"`
	Protocol string `json:"protocol"`
}

// Service 是 GUI 列表用的精簡 Service。
type Service struct {
	Name      string        `json:"name"`
	Namespace string        `json:"namespace"`
	Type      string        `json:"type"`
	ClusterIP string        `json:"cluster_ip"`
	Ports     []ServicePort `json:"ports"`
	// Selector 是 spec.selector（無 selector 的 Service 為空）。
	Selector  map[string]string `json:"selector,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type objMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp time.Time         `json:"creationTimestamp"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
	Labels            map[string]string `json:"labels"`
}

// intOrString 是 k8s 的 IntOrString（maxSurge 可為 1 或 "25%"）。
type intOrString string

func (v *intOrString) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		return nil
	}
	if len(s) > 1 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*v = intOrString(str)
		return nil
	}
	*v = intOrString(s)
	return nil
}

// podRefs 收集 Pod 引用的 ConfigMap 與 PVC 名稱（去重、保持出現順序）。
func podRefs(volumes []struct {
	ConfigMap *struct{ Name string } `json:"configMap"`
	PVC       *struct {
		ClaimName string `json:"claimName"`
	} `json:"persistentVolumeClaim"`
}, containers []struct {
	EnvFrom []struct {
		ConfigMapRef *struct{ Name string } `json:"configMapRef"`
	} `json:"envFrom"`
	Env []struct {
		ValueFrom *struct {
			ConfigMapKeyRef *struct{ Name string } `json:"configMapKeyRef"`
		} `json:"valueFrom"`
	} `json:"env"`
}) (cms, pvcs []string) {
	seen := map[string]bool{}
	add := func(dst *[]string, kind, n string) {
		if n != "" && !seen[kind+n] {
			seen[kind+n] = true
			*dst = append(*dst, n)
		}
	}
	for _, v := range volumes {
		if v.ConfigMap != nil {
			add(&cms, "cm", v.ConfigMap.Name)
		}
		if v.PVC != nil {
			add(&pvcs, "pvc", v.PVC.ClaimName)
		}
	}
	for _, c := range containers {
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil {
				add(&cms, "cm", e.ConfigMapRef.Name)
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
				add(&cms, "cm", e.ValueFrom.ConfigMapKeyRef.Name)
			}
		}
	}
	return cms, pvcs
}

// ParsePods 解析 `kubectl get pods -o json`。
func ParsePods(js string) ([]Pod, error) {
	var v struct {
		Items []struct {
			Metadata objMeta `json:"metadata"`
			Spec     struct {
				NodeName string `json:"nodeName"`
				Volumes  []struct {
					ConfigMap *struct{ Name string } `json:"configMap"`
					PVC       *struct {
						ClaimName string `json:"claimName"`
					} `json:"persistentVolumeClaim"`
				} `json:"volumes"`
				Containers []struct {
					EnvFrom []struct {
						ConfigMapRef *struct{ Name string } `json:"configMapRef"`
					} `json:"envFrom"`
					Env []struct {
						ValueFrom *struct {
							ConfigMapKeyRef *struct{ Name string } `json:"configMapKeyRef"`
						} `json:"valueFrom"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				PodIP             string `json:"podIP"`
				Reason            string `json:"reason"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
					State        struct {
						Waiting    *struct{ Reason string } `json:"waiting"`
						Terminated *struct{ Reason string } `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]Pod, 0, len(v.Items))
	for _, it := range v.Items {
		ready, restarts := 0, 0
		status := it.Status.Phase
		for _, c := range it.Status.ContainerStatuses {
			if c.Ready {
				ready++
			}
			restarts += c.RestartCount
			// 與 kubectl 一致：等待／終止原因比 phase 更有資訊（CrashLoopBackOff、Error…）
			if w := c.State.Waiting; w != nil && w.Reason != "" {
				status = w.Reason
			} else if tm := c.State.Terminated; tm != nil && tm.Reason != "" && status != "Succeeded" {
				status = tm.Reason
			}
		}
		if it.Metadata.DeletionTimestamp != nil {
			status = "Terminating"
		} else if status == "" && it.Status.Reason != "" {
			status = it.Status.Reason
		}
		cms, pvcs := podRefs(it.Spec.Volumes, it.Spec.Containers)
		out = append(out, Pod{
			ConfigMaps: cms,
			PVCs:       pvcs,
			Name:       it.Metadata.Name,
			Namespace:  it.Metadata.Namespace,
			Status:     status,
			Ready:      fmt.Sprintf("%d/%d", ready, len(it.Status.ContainerStatuses)),
			Restarts:   restarts,
			Node:       it.Spec.NodeName,
			IP:         it.Status.PodIP,
			Labels:     it.Metadata.Labels,
			CreatedAt:  it.Metadata.CreationTimestamp,
		})
	}
	return out, nil
}

// ParseDeployments 解析 `kubectl get deployments -o json`。
func ParseDeployments(js string) ([]Deployment, error) {
	var v struct {
		Items []struct {
			Metadata objMeta `json:"metadata"`
			Spec     struct {
				Replicas int `json:"replicas"`
				Selector struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"selector"`
				Strategy struct {
					Type          string `json:"type"`
					RollingUpdate struct {
						MaxSurge       intOrString `json:"maxSurge"`
						MaxUnavailable intOrString `json:"maxUnavailable"`
					} `json:"rollingUpdate"`
				} `json:"strategy"`
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas     int `json:"readyReplicas"`
				UpdatedReplicas   int `json:"updatedReplicas"`
				AvailableReplicas int `json:"availableReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]Deployment, 0, len(v.Items))
	for _, it := range v.Items {
		image := ""
		if cs := it.Spec.Template.Spec.Containers; len(cs) > 0 {
			image = cs[0].Image
		}
		out = append(out, Deployment{
			Name:           it.Metadata.Name,
			Namespace:      it.Metadata.Namespace,
			Ready:          fmt.Sprintf("%d/%d", it.Status.ReadyReplicas, it.Spec.Replicas),
			Replicas:       it.Spec.Replicas,
			UpToDate:       it.Status.UpdatedReplicas,
			Available:      it.Status.AvailableReplicas,
			Selector:       it.Spec.Selector.MatchLabels,
			Image:          image,
			Strategy:       it.Spec.Strategy.Type,
			MaxSurge:       string(it.Spec.Strategy.RollingUpdate.MaxSurge),
			MaxUnavailable: string(it.Spec.Strategy.RollingUpdate.MaxUnavailable),
			CreatedAt:      it.Metadata.CreationTimestamp,
		})
	}
	return out, nil
}

// ParseServices 解析 `kubectl get services -o json`。
func ParseServices(js string) ([]Service, error) {
	var v struct {
		Items []struct {
			Metadata objMeta `json:"metadata"`
			Spec     struct {
				Type      string            `json:"type"`
				ClusterIP string            `json:"clusterIP"`
				Selector  map[string]string `json:"selector"`
				Ports     []struct {
					Name     string `json:"name"`
					Port     int    `json:"port"`
					NodePort int    `json:"nodePort"`
					Protocol string `json:"protocol"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(v.Items))
	for _, it := range v.Items {
		s := Service{
			Name:      it.Metadata.Name,
			Namespace: it.Metadata.Namespace,
			Type:      it.Spec.Type,
			ClusterIP: it.Spec.ClusterIP,
			Selector:  it.Spec.Selector,
			CreatedAt: it.Metadata.CreationTimestamp,
			Ports:     []ServicePort{},
		}
		for _, p := range it.Spec.Ports {
			s.Ports = append(s.Ports, ServicePort{Name: p.Name, Port: p.Port, NodePort: p.NodePort, Protocol: p.Protocol})
		}
		out = append(out, s)
	}
	return out, nil
}

// ParseNamespaces 解析 `kubectl get namespaces -o json`。
func ParseNamespaces(js string) ([]string, error) {
	var v struct {
		Items []struct {
			Metadata objMeta `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Items))
	for _, it := range v.Items {
		out = append(out, it.Metadata.Name)
	}
	return out, nil
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// ValidName 判斷是否為合法的 k8s 物件／namespace 名稱。使用者輸入進 kubectl 引數前一律驗證，
// 避免以 `-`／`--` 開頭的值被當成旗標。
func ValidName(s string) bool { return len(s) > 0 && len(s) <= 253 && nameRe.MatchString(s) }

func nsArgs(ns string) ([]string, error) {
	if ns == "" {
		return []string{"-A"}, nil
	}
	if !ValidName(ns) {
		return nil, fmt.Errorf("invalid namespace %q", ns)
	}
	return []string{"-n", ns}, nil
}

func (m *Manager) list(ctx context.Context, kind, ns string) (string, error) {
	scope, err := nsArgs(ns)
	if err != nil {
		return "", err
	}
	args := append([]string{"k3s", "kubectl", "get", kind}, scope...)
	args = append(args, "-o", "json")
	return m.root(ctx, nil, args...)
}

// ListPods 列出 Pod；ns 為空 = 所有 namespace。
func (m *Manager) ListPods(ctx context.Context, ns string) ([]Pod, error) {
	out, err := m.list(ctx, "pods", ns)
	if err != nil {
		return nil, err
	}
	return ParsePods(out)
}

// ListDeployments 列出 Deployment；ns 為空 = 所有 namespace。
func (m *Manager) ListDeployments(ctx context.Context, ns string) ([]Deployment, error) {
	out, err := m.list(ctx, "deployments", ns)
	if err != nil {
		return nil, err
	}
	return ParseDeployments(out)
}

// ListServices 列出 Service；ns 為空 = 所有 namespace。
func (m *Manager) ListServices(ctx context.Context, ns string) ([]Service, error) {
	out, err := m.list(ctx, "services", ns)
	if err != nil {
		return nil, err
	}
	return ParseServices(out)
}

// ListNamespaces 列出 namespace 名稱。
func (m *Manager) ListNamespaces(ctx context.Context) ([]string, error) {
	out, err := m.root(ctx, nil, "k3s", "kubectl", "get", "namespaces", "-o", "json")
	if err != nil {
		return nil, err
	}
	return ParseNamespaces(out)
}

// 可刪除的種類（GUI 只提供這三種）。
var deletable = map[string]bool{"pod": true, "deployment": true, "service": true}

func checkRef(ns, name string) error {
	if !ValidName(ns) {
		return fmt.Errorf("invalid namespace %q", ns)
	}
	if !ValidName(name) {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}

// Delete 刪除 pod|deployment|service。
func (m *Manager) Delete(ctx context.Context, kind, ns, name string) error {
	if !deletable[kind] {
		return fmt.Errorf("cannot delete kind %q", kind)
	}
	if err := checkRef(ns, name); err != nil {
		return err
	}
	_, err := m.root(ctx, nil, "k3s", "kubectl", "delete", kind, name, "-n", ns)
	return err
}

// RestartDeployment = kubectl rollout restart。
func (m *Manager) RestartDeployment(ctx context.Context, ns, name string) error {
	if err := checkRef(ns, name); err != nil {
		return err
	}
	_, err := m.root(ctx, nil, "k3s", "kubectl", "rollout", "restart", "deployment/"+name, "-n", ns)
	return err
}

// MaxReplicas 是 GUI 調整副本數的上限（防止誤輸入把單節點壓垮）。
const MaxReplicas = 100

// ScaleDeployment 調整副本數（0..MaxReplicas）。
func (m *Manager) ScaleDeployment(ctx context.Context, ns, name string, replicas int) error {
	if err := checkRef(ns, name); err != nil {
		return err
	}
	if replicas < 0 || replicas > MaxReplicas {
		return errors.New("replicas must be between 0 and " + strconv.Itoa(MaxReplicas))
	}
	_, err := m.root(ctx, nil, "k3s", "kubectl", "scale", "deployment/"+name, "--replicas="+strconv.Itoa(replicas), "-n", ns)
	return err
}

// LogsArgs 回傳在 distro 內執行的 `k3s kubectl logs ...` 指令；輸入不合法回 nil。
func LogsArgs(ns, pod, container string, tail int, follow bool) []string {
	if !ValidName(ns) || !ValidName(pod) || (container != "" && !ValidName(container)) {
		return nil
	}
	if tail < 0 {
		tail = 0
	}
	a := []string{"k3s", "kubectl", "logs", pod, "-n", ns, "--tail=" + strconv.Itoa(tail), "--timestamps"}
	if container != "" {
		a = append(a, "-c", container)
	}
	if follow {
		a = append(a, "-f")
	}
	return a
}

// ExecArgs 回傳互動式 `kubectl exec -it`（先試 bash，不行改 sh）；輸入不合法回 nil。
func ExecArgs(ns, pod, container string) []string {
	if !ValidName(ns) || !ValidName(pod) || (container != "" && !ValidName(container)) {
		return nil
	}
	a := []string{"k3s", "kubectl", "exec", "-it", pod, "-n", ns}
	if container != "" {
		a = append(a, "-c", container)
	}
	return append(a, "--", "sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh")
}
