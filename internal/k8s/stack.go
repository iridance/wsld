package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MaxStackServices 是一個 stack 最多的服務數。
const MaxStackServices = 12

// StackLabel 標在 stack 內每個物件上，供整組刪除與 GUI 分組。
const StackLabel = "wsld.stack"

var (
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sizeRe    = regexp.MustCompile(`^[1-9][0-9]{0,5}(Mi|Gi)$`)
)

// EnvVar 是容器環境變數（v1 只支援明文）。
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// StackVolume 描述一個掛載到服務的 PVC。
type StackVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	Size      string `json:"size,omitempty"` // 預設 1Gi；HostPath 非空時不用
	// HostPath 非空時改掛 k3s 所在機器上的資料夾（Compose 的 bind mount），不建 PVC。
	HostPath string `json:"host_path,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// StackService 是 stack 內的一個服務。
type StackService struct {
	Name      string        `json:"name"`
	Image     string        `json:"image"`
	Port      int           `json:"port,omitempty"`
	Replicas  int           `json:"replicas,omitempty"`
	Expose    string        `json:"expose,omitempty"` // none | nodeport | loadbalancer
	HostPort  int           `json:"host_port,omitempty"`
	Env       []EnvVar      `json:"env,omitempty"`
	Volumes   []StackVolume `json:"volumes,omitempty"`
	DependsOn []string      `json:"depends_on,omitempty"`
	Stateful  bool          `json:"stateful,omitempty"` // true → StatefulSet
	// Command／Args 覆寫映像的 ENTRYPOINT／CMD（Compose 的 entrypoint／command）。
	Command    []string `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

// StackSpec 描述一組一起部署的服務。
type StackSpec struct {
	Name      string         `json:"name"`
	Namespace string         `json:"namespace,omitempty"` // 空 → Name
	Services  []StackService `json:"services"`
}

// StackResult 是 DeployStack 的結果，Services 依部署順序排列。
type StackResult struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Services  []DeployResult `json:"services"`
}

// Normalize 驗證並補上預設值；回傳的 spec 內服務順序不變（部署順序見 Order）。
func (s StackSpec) Normalize() (StackSpec, error) {
	if !ValidName(s.Name) || len(s.Name) > 63 || strings.Contains(s.Name, ".") {
		return s, fmt.Errorf("invalid stack name %q", s.Name)
	}
	if s.Namespace == "" {
		s.Namespace = s.Name
	}
	if !ValidName(s.Namespace) {
		return s, fmt.Errorf("invalid namespace %q", s.Namespace)
	}
	if n := len(s.Services); n < 1 || n > MaxStackServices {
		return s, fmt.Errorf("a stack needs 1 to %d services", MaxStackServices)
	}
	svcs := make([]StackService, len(s.Services))
	copy(svcs, s.Services)
	names := map[string]bool{}
	for i := range svcs {
		sv := &svcs[i]
		// 重用單一服務的驗證（名稱、映像、埠、副本、expose）。
		d, err := DeploySpec{Image: sv.Image, Name: sv.Name, Namespace: s.Namespace, Port: sv.Port, Expose: sv.Expose, HostPort: sv.HostPort, Replicas: sv.Replicas}.Normalize()
		if err != nil {
			return s, fmt.Errorf("service %q: %w", sv.Name, err)
		}
		if names[sv.Name] {
			return s, fmt.Errorf("duplicate service %q", sv.Name)
		}
		names[sv.Name] = true
		sv.Replicas, sv.Expose, sv.HostPort = d.Replicas, d.Expose, d.HostPort
		if sv.WorkingDir != "" && !strings.HasPrefix(sv.WorkingDir, "/") {
			return s, fmt.Errorf("service %q: working dir %q must be absolute", sv.Name, sv.WorkingDir)
		}
		for _, e := range sv.Env {
			if !envNameRe.MatchString(e.Name) {
				return s, fmt.Errorf("service %q: invalid env name %q", sv.Name, e.Name)
			}
		}
		vols := map[string]bool{}
		for j := range sv.Volumes {
			v := &sv.Volumes[j]
			if !ValidName(v.Name) || len(v.Name) > 63 || strings.Contains(v.Name, ".") {
				return s, fmt.Errorf("service %q: invalid volume name %q", sv.Name, v.Name)
			}
			if vols[v.Name] {
				return s, fmt.Errorf("service %q: duplicate volume %q", sv.Name, v.Name)
			}
			vols[v.Name] = true
			if !strings.HasPrefix(v.MountPath, "/") || strings.ContainsAny(v.MountPath, ":\n") {
				return s, fmt.Errorf("service %q: mount path %q must be absolute", sv.Name, v.MountPath)
			}
			if v.HostPath != "" {
				if !strings.HasPrefix(v.HostPath, "/") || strings.ContainsAny(v.HostPath, "\n\x00") {
					return s, fmt.Errorf("service %q: host path %q must be absolute", sv.Name, v.HostPath)
				}
				continue
			}
			if v.Size == "" {
				v.Size = "1Gi"
			}
			if !sizeRe.MatchString(v.Size) {
				return s, fmt.Errorf("service %q: invalid size %q (use e.g. 500Mi, 2Gi)", sv.Name, v.Size)
			}
		}
	}
	for _, sv := range svcs {
		for _, dep := range sv.DependsOn {
			if dep == sv.Name {
				return s, fmt.Errorf("service %q depends on itself", sv.Name)
			}
			if !names[dep] {
				return s, fmt.Errorf("service %q depends on unknown service %q", sv.Name, dep)
			}
		}
	}
	s.Services = svcs
	if _, err := s.Order(); err != nil {
		return s, err
	}
	return s, nil
}

// Order 以拓撲排序回傳部署順序（相依者在前）；同層依名稱排序以求穩定。有環時回傳錯誤。
func (s StackSpec) Order() ([]StackService, error) {
	by := map[string]StackService{}
	indeg := map[string]int{}
	rev := map[string][]string{}
	for _, sv := range s.Services {
		by[sv.Name] = sv
		indeg[sv.Name] += 0
		for _, d := range sv.DependsOn {
			indeg[sv.Name]++
			rev[d] = append(rev[d], sv.Name)
		}
	}
	var ready []string
	for n, d := range indeg {
		if d == 0 {
			ready = append(ready, n)
		}
	}
	sort.Strings(ready)
	var out []StackService
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		out = append(out, by[n])
		var next []string
		for _, m := range rev[n] {
			if indeg[m]--; indeg[m] == 0 {
				next = append(next, m)
			}
		}
		sort.Strings(next)
		ready = append(ready, next...)
	}
	if len(out) != len(s.Services) {
		return nil, errors.New("services have a circular dependency")
	}
	return out, nil
}

// serviceEnv 合併使用者 env 與相依注入的 <DEP>_HOST／<DEP>_PORT（使用者同名優先）。
func serviceEnv(s StackSpec, sv StackService) []any {
	have := map[string]bool{}
	var env []any
	for _, e := range sv.Env {
		have[e.Name] = true
		env = append(env, map[string]any{"name": e.Name, "value": e.Value})
	}
	by := map[string]StackService{}
	for _, x := range s.Services {
		by[x.Name] = x
	}
	for _, d := range sv.DependsOn {
		key := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(d))
		add := func(n, v string) {
			if !have[n] {
				have[n] = true
				env = append(env, map[string]any{"name": n, "value": v})
			}
		}
		add(key+"_HOST", d)
		if p := by[d].Port; p > 0 {
			add(key+"_PORT", fmt.Sprint(p))
		}
	}
	return env
}

// StackManifest 產生 kubectl apply 用的 List（JSON）。hostPorts 是 LoadBalancer 服務已挑好的 localhost 埠（以服務名稱為鍵）。
// 每個有埠的服務都有叢集內可解析的 ClusterIP Service（名稱＝服務名稱）；要對外時改為 NodePort／LoadBalancer。
func StackManifest(s StackSpec, hostPorts map[string]int) ([]byte, error) {
	order, err := s.Order()
	if err != nil {
		return nil, err
	}
	var items []any
	for _, sv := range order {
		labels := map[string]string{"app": sv.Name, StackLabel: s.Name}
		selector := map[string]string{"app": sv.Name}
		container := map[string]any{"name": sv.Name, "image": sv.Image, "imagePullPolicy": "IfNotPresent"}
		if sv.Port > 0 {
			container["ports"] = []any{map[string]any{"containerPort": sv.Port}}
		}
		if env := serviceEnv(s, sv); len(env) > 0 {
			container["env"] = env
		}
		if len(sv.Command) > 0 {
			container["command"] = sv.Command
		}
		if len(sv.Args) > 0 {
			container["args"] = sv.Args
		}
		if sv.WorkingDir != "" {
			container["workingDir"] = sv.WorkingDir
		}
		var mounts, volumes []any
		for _, v := range sv.Volumes {
			mount := map[string]any{"name": v.Name, "mountPath": v.MountPath}
			if v.ReadOnly {
				mount["readOnly"] = true
			}
			mounts = append(mounts, mount)
			if v.HostPath != "" {
				volumes = append(volumes, map[string]any{"name": v.Name, "hostPath": map[string]any{"path": v.HostPath}})
				continue
			}
			claim := sv.Name + "-" + v.Name
			items = append(items, map[string]any{
				"apiVersion": "v1", "kind": "PersistentVolumeClaim",
				"metadata": map[string]any{"name": claim, "namespace": s.Namespace, "labels": labels},
				"spec": map[string]any{
					"accessModes": []any{"ReadWriteOnce"},
					"resources":   map[string]any{"requests": map[string]any{"storage": v.Size}},
				},
			})
			volumes = append(volumes, map[string]any{"name": v.Name, "persistentVolumeClaim": map[string]any{"claimName": claim}})
		}
		if len(mounts) > 0 {
			container["volumeMounts"] = mounts
		}
		podSpec := map[string]any{"containers": []any{container}}
		if len(volumes) > 0 {
			podSpec["volumes"] = volumes
		}
		workload := map[string]any{
			"replicas": sv.Replicas,
			"selector": map[string]any{"matchLabels": selector},
			"template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": podSpec},
		}
		kind := "Deployment"
		if sv.Stateful {
			kind = "StatefulSet"
			workload["serviceName"] = sv.Name
		}
		items = append(items, map[string]any{
			"apiVersion": "apps/v1", "kind": kind,
			"metadata": map[string]any{"name": sv.Name, "namespace": s.Namespace, "labels": labels},
			"spec":     workload,
		})
		if sv.Port > 0 {
			typ, port := "ClusterIP", sv.Port
			switch sv.Expose {
			case ExposeNodePort:
				typ = "NodePort"
			case ExposeLoadBalancer:
				typ, port = "LoadBalancer", sv.HostPort
				if p := hostPorts[sv.Name]; p > 0 {
					port = p
				}
			}
			svc := serviceObject(s.Namespace, sv.Name, typ, port, sv.Port, selector)
			svc["metadata"] = map[string]any{"name": sv.Name, "namespace": s.Namespace, "labels": labels}
			items = append(items, svc)
		}
	}
	return json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
}

// pickStackHostPorts 為每個 LoadBalancer 服務挑 localhost 埠，stack 內彼此也互相避開。
func (m *Manager) pickStackHostPorts(ctx context.Context, s StackSpec) map[string]int {
	out := map[string]int{}
	taken := map[int]bool{}
	for _, sv := range s.Services {
		if sv.Expose != ExposeLoadBalancer {
			continue
		}
		spec := DeploySpec{Name: sv.Name, Namespace: s.Namespace, HostPort: sv.HostPort}
		p := m.pickHostPort(ctx, spec)
		for taken[p] && p < 65535 {
			spec.HostPort = p + 1
			p = m.pickHostPort(ctx, spec)
		}
		taken[p] = true
		out[sv.Name] = p
	}
	return out
}

// DeployStack 建 namespace、套用整份 manifest，再依相依順序等待各服務 rollout。
func (m *Manager) DeployStack(ctx context.Context, spec StackSpec) (StackResult, error) {
	s, err := spec.Normalize()
	if err != nil {
		return StackResult{}, err
	}
	ports := m.pickStackHostPorts(ctx, s)
	manifest, err := StackManifest(s, ports)
	if err != nil {
		return StackResult{}, err
	}
	if s.Namespace != DefaultNamespace {
		ns, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": s.Namespace}})
		if _, err := m.root(ctx, bytes.NewReader(ns), "k3s", "kubectl", "apply", "-f", "-"); err != nil {
			return StackResult{}, err
		}
	}
	if _, err := m.root(ctx, bytes.NewReader(manifest), "k3s", "kubectl", "apply", "-f", "-"); err != nil {
		return StackResult{}, err
	}
	order, _ := s.Order()
	res := StackResult{Namespace: s.Namespace, Name: s.Name}
	for _, sv := range order {
		kind := "deployment"
		if sv.Stateful {
			kind = "statefulset"
		}
		if _, err := m.root(ctx, nil, "k3s", "kubectl", "rollout", "status", kind+"/"+sv.Name, "-n", s.Namespace, "--timeout=120s"); err != nil {
			msg := fmt.Sprintf("service %q did not become ready: %v", sv.Name, err)
			if tail := m.podLogTail(ctx, s.Namespace, sv.Name); tail != "" {
				msg += "\n" + tail + "\nThe image may need configuration (env vars): check its documentation."
			}
			return res, errors.New(msg)
		}
		r := DeployResult{Namespace: s.Namespace, Name: sv.Name, Expose: sv.Expose}
		switch sv.Expose {
		case ExposeLoadBalancer:
			r.HostPort, r.PortChanged = ports[sv.Name], ports[sv.Name] != sv.HostPort
		case ExposeNodePort:
			if out, err := m.root(ctx, nil, "k3s", "kubectl", "get", "service", sv.Name, "-n", s.Namespace, "-o", "json"); err == nil {
				if svcs, err := ParseServices(`{"items":[` + out + `]}`); err == nil && len(svcs) == 1 && len(svcs[0].Ports) > 0 {
					r.NodePort = svcs[0].Ports[0].NodePort
				}
			}
		}
		res.Services = append(res.Services, r)
	}
	return res, nil
}

// DeleteStack 刪除帶有 stack label 的工作負載與 Service；withPVC 時一併刪 PVC（資料會消失）。
func (m *Manager) DeleteStack(ctx context.Context, ns, name string, withPVC bool) error {
	if err := checkRef(ns, name); err != nil {
		return err
	}
	kinds := "deployments,statefulsets,services"
	if withPVC {
		kinds += ",persistentvolumeclaims"
	}
	_, err := m.root(ctx, nil, "k3s", "kubectl", "delete", kinds, "-n", ns, "-l", StackLabel+"="+name)
	return err
}

// podLogTail 取服務 Pod 最後幾行 log（先看目前的容器，空的話看上一次已結束的，CrashLoop 時才有內容）。
func (m *Manager) podLogTail(ctx context.Context, ns, app string) string {
	for _, prev := range []bool{false, true} {
		args := []string{"k3s", "kubectl", "logs", "-n", ns, "-l", "app=" + app, "--tail=10", "--all-containers", "--max-log-requests=5"}
		if prev {
			args = append(args, "--previous")
		}
		if out, err := m.root(ctx, nil, args...); err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out)
		}
	}
	return ""
}
