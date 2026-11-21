package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/k8s"
)

// FakeK8sCluster 是記憶體內的 k3s 叢集假實作（Go 測試與 Playwright 測試主機共用）。
// 一個 cluster 對應整個 WSL VM（單一叢集限制）；FakeK8s 是它對某 distro 的視圖。
type FakeK8sCluster struct {
	mu         sync.Mutex
	installed  map[string]bool
	running    string // 目前執行 k3s 的 distro；空 = 沒有
	Delay      time.Duration
	FailStart  bool
	pods       []k8s.Pod
	deploys    []k8s.Deployment
	services   []k8s.Service
	sts        []k8s.StatefulSet
	ingresses  []k8s.Ingress
	configmaps []k8s.ConfigMap
	pvcs       []k8s.PVC
	others     []k8s.Other
	namespaces []string
	Calls      []string // 動作紀錄：start:<d> stop:<d> reset:<d> delete:pod/ns/name …
}

// NewFakeK8sCluster 建立含 kube-system 與一個範例 Deployment 的叢集。
func NewFakeK8sCluster() *FakeK8sCluster {
	c := &FakeK8sCluster{installed: map[string]bool{}}
	c.seed()
	return c
}

func (c *FakeK8sCluster) seed() {
	t0 := time.Now().Add(-2 * time.Hour)
	c.namespaces = []string{"default", "kube-system", "kube-public", "kube-node-lease"}
	c.pods = []k8s.Pod{
		{Name: "coredns-577d995dff-74qgr", Namespace: "kube-system", Status: "Running", Ready: "1/1", Node: "framework", IP: "10.42.0.10", Labels: map[string]string{"k8s-app": "kube-dns"}, CreatedAt: t0},
		{Name: "local-path-provisioner-56c6584955-w6bbr", Namespace: "kube-system", Status: "Running", Ready: "1/1", Node: "framework", IP: "10.42.0.11", CreatedAt: t0},
		{Name: "metrics-server-778c4649b4-q6x2b", Namespace: "kube-system", Status: "Running", Ready: "1/1", Restarts: 1, Node: "framework", IP: "10.42.0.12", CreatedAt: t0},
		{Name: "poc-web-56bcf8dcb9-dp6m9", Namespace: "default", Status: "Running", Ready: "1/1", Node: "framework", IP: "10.42.0.13", Labels: map[string]string{"app": "poc-web"}, ConfigMaps: []string{"poc-web-env"}, PVCs: []string{"poc-data"}, CreatedAt: t0},
	}
	c.deploys = []k8s.Deployment{
		{Name: "poc-web", Namespace: "default", Ready: "1/1", Replicas: 1, UpToDate: 1, Available: 1, Selector: map[string]string{"app": "poc-web"}, Image: "poc/web:1.0", Strategy: "RollingUpdate", MaxSurge: "25%", MaxUnavailable: "25%", CreatedAt: t0},
		{Name: "coredns", Namespace: "kube-system", Ready: "1/1", Replicas: 1, UpToDate: 1, Available: 1, Selector: map[string]string{"k8s-app": "kube-dns"}, Image: "rancher/coredns:1.12", Strategy: "RollingUpdate", MaxSurge: "25%", MaxUnavailable: "25%", CreatedAt: t0},
	}
	c.sts, c.ingresses = nil, []k8s.Ingress{{Name: "poc-web", Namespace: "default", Rules: []k8s.IngressRule{{Host: "poc.local", Path: "/", Service: "poc-web", Port: "8080"}}, CreatedAt: t0}}
	c.configmaps = []k8s.ConfigMap{{Name: "poc-web-env", Namespace: "default", Keys: 2, CreatedAt: t0}}
	c.others = []k8s.Other{
		{Kind: "Lease", Name: "framework", Namespace: "kube-node-lease", CreatedAt: t0},
		{Kind: "Role", Name: "system:controller:bootstrap-signer", Namespace: "kube-public", CreatedAt: t0},
		{Kind: "RoleBinding", Name: "system:controller:bootstrap-signer", Namespace: "kube-public", CreatedAt: t0},
	}
	c.pvcs = []k8s.PVC{{Name: "poc-data", Namespace: "default", Status: "Bound", Size: "1Gi", StorageClass: "local-path", CreatedAt: t0}}
	c.services = []k8s.Service{
		{Name: "kubernetes", Namespace: "default", Type: "ClusterIP", ClusterIP: "10.43.0.1", Ports: []k8s.ServicePort{{Name: "https", Port: 443, Protocol: "TCP"}}, CreatedAt: t0},
		{Name: "poc-web", Namespace: "default", Type: "NodePort", ClusterIP: "10.43.221.80", Ports: []k8s.ServicePort{{Port: 8080, NodePort: 30080, Protocol: "TCP"}}, Selector: map[string]string{"app": "poc-web"}, CreatedAt: t0},
		{Name: "kube-dns", Namespace: "kube-system", Type: "ClusterIP", ClusterIP: "10.43.0.10", Ports: []k8s.ServicePort{{Name: "dns", Port: 53, Protocol: "UDP"}}, Selector: map[string]string{"k8s-app": "kube-dns"}, CreatedAt: t0},
	}
}

// SeedShop 加入較完整的 "shop" namespace：Ingress → web／api Service → Deployment → Pods，
// 加上 postgres StatefulSet、ConfigMap 與 PVC（測試主機 /__test/k8s?shop=1，Go 測試直接呼叫）。
func (c *FakeK8sCluster) SeedShop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	t0 := time.Now().Add(-5 * time.Hour)
	c.namespaces = append(c.namespaces, "shop")
	pod := func(name string, labels map[string]string, cms, pvcs []string) k8s.Pod {
		return k8s.Pod{Name: name, Namespace: "shop", Status: "Running", Ready: "1/1", Node: "framework", IP: "10.42.1.5", Labels: labels, ConfigMaps: cms, PVCs: pvcs, CreatedAt: t0}
	}
	web, api, pg := map[string]string{"app": "web"}, map[string]string{"app": "api"}, map[string]string{"app": "postgres"}
	c.pods = append(c.pods,
		pod("web-6d9f7c-x2k4", web, nil, nil), pod("web-6d9f7c-p8m1", web, nil, nil),
		pod("api-5c4b8d-j7qz", api, []string{"api-env"}, nil), pod("api-5c4b8d-m2vd", api, []string{"api-env"}, nil), pod("api-5c4b8d-t9ln", api, []string{"api-env"}, nil),
		pod("postgres-0", pg, nil, []string{"postgres-data"}),
	)
	roll := func(name, image string, n int, sel map[string]string) k8s.Deployment {
		return k8s.Deployment{Name: name, Namespace: "shop", Ready: fmt.Sprintf("%d/%d", n, n), Replicas: n, UpToDate: n, Available: n, Selector: sel, Image: image, Strategy: "RollingUpdate", MaxSurge: "25%", MaxUnavailable: "25%", CreatedAt: t0}
	}
	c.deploys = append(c.deploys, roll("web", "shop/web:1.8.2", 2, web), roll("api", "shop/api:2.4.0", 3, api))
	c.services = append(c.services,
		k8s.Service{Name: "web-svc", Namespace: "shop", Type: "ClusterIP", ClusterIP: "10.43.12.8", Ports: []k8s.ServicePort{{Port: 80, Protocol: "TCP"}}, Selector: web, CreatedAt: t0},
		k8s.Service{Name: "api-svc", Namespace: "shop", Type: "ClusterIP", ClusterIP: "10.43.77.21", Ports: []k8s.ServicePort{{Port: 8080, Protocol: "TCP"}}, Selector: api, CreatedAt: t0},
	)
	c.sts = append(c.sts, k8s.StatefulSet{Name: "postgres", Namespace: "shop", Ready: "1/1", Replicas: 1, Selector: pg, Image: "postgres:16.4", CreatedAt: t0})
	c.ingresses = append(c.ingresses, k8s.Ingress{Name: "shop", Namespace: "shop", Rules: []k8s.IngressRule{{Host: "shop.local", Path: "/", Service: "web-svc", Port: "80"}, {Host: "shop.local", Path: "/api", Service: "api-svc", Port: "8080"}}, CreatedAt: t0})
	c.configmaps = append(c.configmaps, k8s.ConfigMap{Name: "api-env", Namespace: "shop", Keys: 6, CreatedAt: t0})
	c.pvcs = append(c.pvcs, k8s.PVC{Name: "postgres-data", Namespace: "shop", Status: "Bound", Size: "20Gi", StorageClass: "local-path", CreatedAt: t0})
}

// Reset 回到初始狀態（測試主機 /__test/reset）。
func (c *FakeK8sCluster) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.installed = map[string]bool{}
	c.running, c.Delay, c.FailStart, c.Calls = "", 0, false, nil
	c.seed()
}

// Configure 調整行為：delay＝每個動作的延遲；failStart＝Start 失敗（測試主機 /__test/k8s）。
func (c *FakeK8sCluster) Configure(delay time.Duration, failStart bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Delay, c.FailStart = delay, failStart
}

// SetInstalled 預先標記某 distro 已安裝 k3s。
func (c *FakeK8sCluster) SetInstalled(distro string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.installed[distro] = true
}

// For 回傳某 distro 的視圖。
func (c *FakeK8sCluster) For(distro string) *FakeK8s { return &FakeK8s{c: c, distro: distro} }

// Running 回傳目前執行 k3s 的 distro。
func (c *FakeK8sCluster) Running() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Called 回傳動作紀錄的副本。
func (c *FakeK8sCluster) Called() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.Calls...)
}

// FakeK8s 實作 K8s。
type FakeK8s struct {
	c      *FakeK8sCluster
	distro string
}

func (f *FakeK8s) sleep() {
	f.c.mu.Lock()
	d := f.c.Delay
	f.c.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

// Status 實作 K8s。
func (f *FakeK8s) Status(context.Context) k8s.Status {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	st := k8s.Status{Distro: f.distro, State: k8s.StateNotInstalled, Endpoint: k8s.APIServer, Context: k8s.ContextName(f.distro)}
	if !f.c.installed[f.distro] {
		return st
	}
	st.Installed = true
	st.State = k8s.StateOff
	if strings.EqualFold(f.c.running, f.distro) {
		st.State, st.Active, st.Ready, st.NodeReady = k8s.StateReady, true, true, true
		st.Version, st.NodeName, st.NodeIP, st.MemoryBytes = "v1.37.2+k3s1", "framework", "172.20.3.68", 439762944
	}
	return st
}

// Start 實作 K8s（未安裝時等同安裝＋啟動）。
func (f *FakeK8s) Start(context.Context) error {
	f.sleep()
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	f.c.Calls = append(f.c.Calls, "start:"+f.distro)
	if f.c.FailStart {
		return errors.New("k3s did not become ready in time")
	}
	if f.c.running != "" && !strings.EqualFold(f.c.running, f.distro) {
		return fmt.Errorf("k3s is already running on %s", f.c.running)
	}
	f.c.installed[f.distro] = true
	f.c.running = f.distro
	return nil
}

// Stop 實作 K8s。
func (f *FakeK8s) Stop(context.Context) error {
	f.sleep()
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	f.c.Calls = append(f.c.Calls, "stop:"+f.distro)
	if strings.EqualFold(f.c.running, f.distro) {
		f.c.running = ""
	}
	return nil
}

// Reset 實作 K8s：回到只有系統元件的乾淨叢集。
func (f *FakeK8s) Reset(ctx context.Context) error {
	f.sleep()
	f.c.mu.Lock()
	f.c.Calls = append(f.c.Calls, "reset:"+f.distro)
	f.c.seed()
	f.c.deploys = f.c.deploys[1:]
	f.c.services = append(f.c.services[:1:1], f.c.services[2:]...)
	var keep []k8s.Pod
	for _, p := range f.c.pods {
		if p.Namespace == "kube-system" {
			keep = append(keep, p)
		}
	}
	f.c.pods = keep
	f.c.ingresses, f.c.configmaps, f.c.pvcs, f.c.sts = nil, nil, nil, nil
	f.c.namespaces = []string{"default", "kube-system", "kube-public", "kube-node-lease"}
	f.c.running = f.distro
	f.c.mu.Unlock()
	return nil
}

// Kubeconfig 實作 K8s。
func (f *FakeK8s) Kubeconfig(context.Context) ([]byte, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if !f.c.installed[f.distro] {
		return nil, errors.New("k3s is not installed")
	}
	return []byte(`apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Q0E=
    server: https://127.0.0.1:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
kind: Config
users:
- name: default
  user:
    client-certificate-data: Q0VSVA==
    client-key-data: S0VZ
`), nil
}

func (f *FakeK8s) requireRunning() error {
	if !strings.EqualFold(f.c.running, f.distro) {
		return errors.New("The connection to the server was refused: k3s is not running")
	}
	return nil
}

// ListNamespaces 實作 K8s。
func (f *FakeK8s) ListNamespaces(context.Context) ([]string, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return nil, err
	}
	return append([]string(nil), f.c.namespaces...), nil
}

// ListPods 實作 K8s。
func (f *FakeK8s) ListPods(_ context.Context, ns string) ([]k8s.Pod, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return nil, err
	}
	out := []k8s.Pod{}
	for _, p := range f.c.pods {
		if ns == "" || p.Namespace == ns {
			out = append(out, p)
		}
	}
	return out, nil
}

// ListDeployments 實作 K8s。
func (f *FakeK8s) ListDeployments(_ context.Context, ns string) ([]k8s.Deployment, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return nil, err
	}
	out := []k8s.Deployment{}
	for _, d := range f.c.deploys {
		if ns == "" || d.Namespace == ns {
			out = append(out, d)
		}
	}
	return out, nil
}

// ListServices 實作 K8s。
func (f *FakeK8s) ListServices(_ context.Context, ns string) ([]k8s.Service, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return nil, err
	}
	out := []k8s.Service{}
	for _, s := range f.c.services {
		if ns == "" || s.Namespace == ns {
			out = append(out, s)
		}
	}
	return out, nil
}

// ListExtras 實作 K8s。
func (f *FakeK8s) ListExtras(_ context.Context, ns string) (k8s.Extras, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return k8s.Extras{}, err
	}
	x := k8s.Extras{StatefulSets: []k8s.StatefulSet{}, Ingresses: []k8s.Ingress{}, ConfigMaps: []k8s.ConfigMap{}, PVCs: []k8s.PVC{}, Others: []k8s.Other{}}
	for _, v := range f.c.sts {
		if ns == "" || v.Namespace == ns {
			x.StatefulSets = append(x.StatefulSets, v)
		}
	}
	for _, v := range f.c.ingresses {
		if ns == "" || v.Namespace == ns {
			x.Ingresses = append(x.Ingresses, v)
		}
	}
	for _, v := range f.c.configmaps {
		if ns == "" || v.Namespace == ns {
			x.ConfigMaps = append(x.ConfigMaps, v)
		}
	}
	for _, v := range f.c.pvcs {
		if ns == "" || v.Namespace == ns {
			x.PVCs = append(x.PVCs, v)
		}
	}
	for _, v := range f.c.others {
		if ns == "" || v.Namespace == ns {
			x.Others = append(x.Others, v)
		}
	}
	return x, nil
}

// DeploymentDetail 實作 K8s：兩個 rollout 版本（目前＋前一版）與一筆擴縮事件。
func (f *FakeK8s) DeploymentDetail(_ context.Context, ns, name string) (k8s.DeploymentDetail, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return k8s.DeploymentDetail{}, err
	}
	for _, d := range f.c.deploys {
		if d.Namespace == ns && d.Name == name {
			return k8s.DeploymentDetail{
				Revisions: []k8s.Revision{
					{Revision: 2, Image: d.Image, CreatedAt: d.CreatedAt, Current: true},
					{Revision: 1, Image: d.Image + "-prev", CreatedAt: d.CreatedAt.Add(-24 * time.Hour)},
				},
				Events: []k8s.Event{{Type: "Normal", Reason: "ScalingReplicaSet", Message: fmt.Sprintf("Scaled up replica set %s-5c4b8d to %d", name, d.Replicas), Count: 1, LastSeen: d.CreatedAt}},
			}, nil
		}
	}
	return k8s.DeploymentDetail{}, fmt.Errorf("deployments %q not found", name)
}

// Delete 實作 K8s。刪除 pod 後由所屬 Deployment 自動補一個新的（模擬 ReplicaSet）。
func (f *FakeK8s) Delete(_ context.Context, kind, ns, name string) error {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return err
	}
	f.c.Calls = append(f.c.Calls, fmt.Sprintf("delete:%s/%s/%s", kind, ns, name))
	switch kind {
	case "pod":
		for i, p := range f.c.pods {
			if p.Namespace == ns && p.Name == name {
				f.c.pods = append(f.c.pods[:i], f.c.pods[i+1:]...)
				if dep := ownerDeployment(name); dep != "" {
					f.c.pods = append(f.c.pods, k8s.Pod{Name: dep + "-" + randSuffix(len(f.c.pods)), Namespace: ns, Status: "Running", Ready: "1/1", Node: "framework", IP: "10.42.0.99", Labels: map[string]string{"app": dep}, CreatedAt: time.Now()})
				}
				return nil
			}
		}
	case "deployment":
		for i, d := range f.c.deploys {
			if d.Namespace == ns && d.Name == name {
				f.c.deploys = append(f.c.deploys[:i], f.c.deploys[i+1:]...)
				var keep []k8s.Pod
				for _, p := range f.c.pods {
					if !(p.Namespace == ns && ownerDeployment(p.Name) == name) {
						keep = append(keep, p)
					}
				}
				f.c.pods = keep
				return nil
			}
		}
	case "service":
		for i, s := range f.c.services {
			if s.Namespace == ns && s.Name == name {
				f.c.services = append(f.c.services[:i], f.c.services[i+1:]...)
				return nil
			}
		}
	}
	return fmt.Errorf("Error from server (NotFound): %s %q not found", kind, name)
}

// ownerDeployment 由 pod 名稱（<deploy>-<rs>-<id> 或 <deploy>-<id>）推出 Deployment 名稱；系統 pod 回空。
func ownerDeployment(pod string) string {
	for _, d := range []string{"poc-web"} {
		if strings.HasPrefix(pod, d+"-") {
			return d
		}
	}
	return ""
}

func randSuffix(n int) string { return fmt.Sprintf("x%04d", n) }

// RestartDeployment 實作 K8s。
func (f *FakeK8s) RestartDeployment(_ context.Context, ns, name string) error {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return err
	}
	f.c.Calls = append(f.c.Calls, fmt.Sprintf("restart:%s/%s", ns, name))
	for i, p := range f.c.pods {
		if p.Namespace == ns && ownerDeployment(p.Name) == name {
			f.c.pods[i].Name = name + "-" + randSuffix(i+50)
			f.c.pods[i].CreatedAt = time.Now()
		}
	}
	return nil
}

// ScaleDeployment 實作 K8s：同步增減 pod。
func (f *FakeK8s) ScaleDeployment(_ context.Context, ns, name string, replicas int) error {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if err := f.requireRunning(); err != nil {
		return err
	}
	f.c.Calls = append(f.c.Calls, fmt.Sprintf("scale:%s/%s=%d", ns, name, replicas))
	for i, d := range f.c.deploys {
		if d.Namespace != ns || d.Name != name {
			continue
		}
		f.c.deploys[i].Replicas = replicas
		f.c.deploys[i].Ready = fmt.Sprintf("%d/%d", replicas, replicas)
		f.c.deploys[i].UpToDate, f.c.deploys[i].Available = replicas, replicas
		var mine, others []k8s.Pod
		for _, p := range f.c.pods {
			if p.Namespace == ns && ownerDeployment(p.Name) == name {
				mine = append(mine, p)
			} else {
				others = append(others, p)
			}
		}
		for len(mine) < replicas {
			mine = append(mine, k8s.Pod{Name: name + "-" + randSuffix(len(mine)+10), Namespace: ns, Status: "Running", Ready: "1/1", Node: "framework", IP: "10.42.0.50", Labels: map[string]string{"app": name}, CreatedAt: time.Now()})
		}
		if len(mine) > replicas {
			mine = mine[:replicas]
		}
		f.c.pods = append(others, mine...)
		sort.SliceStable(f.c.pods, func(a, b int) bool { return f.c.pods[a].Namespace < f.c.pods[b].Namespace })
		return nil
	}
	return fmt.Errorf("deployments %q not found", name)
}

// FakeCommandStream 是 CommandStream 的測試實作：logs 回幾行後保持開啟，exec 為回顯 shell。
type FakeCommandStream struct {
	mu     sync.Mutex
	Opened [][]string
}

// Open 實作 CommandStream。
func (f *FakeCommandStream) Open(_ context.Context, distro string, argv []string, tty bool, _, _ int) (net.Conn, error) {
	f.mu.Lock()
	f.Opened = append(f.Opened, append([]string{distro}, argv...))
	f.mu.Unlock()
	if tty {
		return OpenFakeShell(true), nil
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.WriteString(server, "2026-11-21T06:00:00Z listening on :8080\n2026-11-21T06:00:01Z GET / 200\n")
		buf := make([]byte, 64)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()
	return client, nil
}

// OpenedCommands 回傳已開啟的指令（第一個元素是 distro）。
func (f *FakeCommandStream) OpenedCommands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.Opened...)
}
