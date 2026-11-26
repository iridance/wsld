package gui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/k8s"
)

// FakeK8s 是測試主機與單元測試用的假 Kubernetes：步驟照真的順序回報，可指定失敗步驟與每步延遲。
type FakeK8s struct {
	mu        sync.Mutex
	installed bool // k8s-engine 已建立並安裝 k3s
	running   bool
	failStep  string
	delay     time.Duration
	pods      []k8s.Pod
	services  []k8s.Service
	stats     []k8s.PodStats
	forwards  []k8s.PortForward
	enables   int
	starts    int
	disables  int
	restarts  int
	resets    int
	deleted   []string
	applied   []string // Apply 收到的 manifest
}

// NewFakeK8s 建立尚未安裝的假 k8s。
func NewFakeK8s() *FakeK8s { return &FakeK8s{} }

// Set 調整場景（測試主機 /__test/k8s）。
func (f *FakeK8s) Set(fn func(f *FakeK8s)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// SetInstalled／SetRunning／SetFailStep／SetDelay／SetPods 供 Set 內使用（已持鎖）。
func (f *FakeK8s) SetInstalled(v bool)         { f.installed = v }
func (f *FakeK8s) SetRunning(v bool)           { f.running = v }
func (f *FakeK8s) SetFailStep(s string)        { f.failStep = s }
func (f *FakeK8s) SetDelay(d time.Duration)    { f.delay = d }
func (f *FakeK8s) SetPods(p []k8s.Pod)         { f.pods = p }
func (f *FakeK8s) SetServices(v []k8s.Service) { f.services = v }
func (f *FakeK8s) SetStats(v []k8s.PodStats)   { f.stats = v }

// Applied 回傳 Apply 收到的 manifest。
func (f *FakeK8s) Applied() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.applied...)
}

// Ops 回傳 restart／reset 次數與被刪除的 pod（ns/name）。
func (f *FakeK8s) Ops() (restarts, resets int, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts, f.resets, append([]string(nil), f.deleted...)
}
func (f *FakeK8s) Counts() (enables, starts, disables int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enables, f.starts, f.disables
}

// Reset 回到尚未安裝。
func (f *FakeK8s) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed, f.running, f.failStep, f.delay, f.pods = false, false, "", 0, nil
	f.services, f.stats, f.forwards, f.deleted, f.applied = nil, nil, nil, nil, nil
	f.enables, f.starts, f.disables, f.restarts, f.resets = 0, 0, 0, 0, 0
}

// For 依設定與回呼建立操作（Server.K8sFor）。
func (f *FakeK8s) For(cfg config.Config, opts K8sOptions) K8sOps {
	return &fakeK8sOps{f: f, cfg: cfg, opts: opts}
}

type fakeK8sOps struct {
	f    *FakeK8s
	cfg  config.Config
	opts K8sOptions
}

func (o *fakeK8sOps) Status(context.Context) k8s.Status {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	st := k8s.Status{Distro: o.cfg.K8sDistro(), State: k8s.StateAbsent, Endpoint: k8s.Endpoint(o.cfg.K8sAPIPortOrDefault())}
	switch {
	case o.f.running:
		st.State, st.Version, st.NodeName, st.NodeIP = k8s.StateReady, "v1.37.2+k3s1", o.cfg.K8sDistro(), "172.20.3.68"
	case o.f.installed:
		st.State = k8s.StateOff
	}
	return st
}

func (o *fakeK8sOps) step(ctx context.Context, s string) error {
	if o.opts.Progress != nil {
		o.opts.Progress(s)
	}
	o.f.mu.Lock()
	d, fail := o.f.delay, o.f.failStep
	o.f.mu.Unlock()
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail == s {
		return &k8s.StepError{Step: s, Err: errors.New("fake failure at " + s)}
	}
	return nil
}

func (o *fakeK8sOps) run(ctx context.Context, steps []string) ([]byte, error) {
	for _, s := range steps {
		if s == k8s.StepDistro && o.opts.Install != nil {
			o.f.mu.Lock()
			installed := o.f.installed
			o.f.mu.Unlock()
			if o.opts.Progress != nil {
				o.opts.Progress(s)
			}
			if !installed {
				if err := o.opts.Install(ctx, "Debian", o.cfg.K8sDistro(), k8s.DefaultVHDSizeGB); err != nil {
					return nil, &k8s.StepError{Step: s, Err: err}
				}
			}
		}
		if err := o.step(ctx, s); err != nil {
			return nil, err
		}
		if s == k8s.StepBootstrap {
			o.f.mu.Lock()
			o.f.installed = true
			o.f.mu.Unlock()
		}
	}
	o.f.mu.Lock()
	o.f.running = true
	o.f.mu.Unlock()
	return []byte(FakeKubeconfig), nil
}

func (o *fakeK8sOps) Enable(ctx context.Context) ([]byte, error) {
	o.f.mu.Lock()
	o.f.enables++
	o.f.mu.Unlock()
	return o.run(ctx, k8s.Steps)
}

func (o *fakeK8sOps) Start(ctx context.Context) ([]byte, error) {
	o.f.mu.Lock()
	o.f.starts++
	installed := o.f.installed
	o.f.mu.Unlock()
	if !installed {
		return o.run(ctx, k8s.Steps)
	}
	return o.run(ctx, []string{k8s.StepEngine, k8s.StepShare, k8s.StepStart, k8s.StepReady, k8s.StepKubeconfig})
}

func (o *fakeK8sOps) Disable(context.Context) error {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	o.f.disables++
	o.f.running = false
	return nil
}

func (o *fakeK8sOps) ListPods(context.Context) ([]k8s.Pod, error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	if !o.f.running {
		return nil, errors.New("k3s is not running")
	}
	return append([]k8s.Pod(nil), o.f.pods...), nil
}

var restartFakeSteps = []string{k8s.StepEngine, k8s.StepShare, k8s.StepStart, k8s.StepReady, k8s.StepKubeconfig}

func (o *fakeK8sOps) Restart(ctx context.Context) ([]byte, error) {
	o.f.mu.Lock()
	o.f.restarts++
	o.f.running = false
	o.f.mu.Unlock()
	if err := o.step(ctx, k8s.StepStop); err != nil {
		return nil, err
	}
	return o.run(ctx, restartFakeSteps)
}

func (o *fakeK8sOps) Reset(ctx context.Context) ([]byte, error) {
	o.f.mu.Lock()
	o.f.resets++
	o.f.running = false
	o.f.mu.Unlock()
	for _, st := range []string{k8s.StepStop, k8s.StepWipe} {
		if err := o.step(ctx, st); err != nil {
			return nil, err
		}
	}
	o.f.mu.Lock()
	o.f.pods, o.f.services, o.f.stats, o.f.forwards = nil, nil, nil, nil
	o.f.mu.Unlock()
	return o.run(ctx, restartFakeSteps)
}

func (o *fakeK8sOps) DeletePod(_ context.Context, ns, name string) error {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	for i, p := range o.f.pods {
		if p.Namespace == ns && p.Name == name {
			o.f.pods = append(o.f.pods[:i:i], o.f.pods[i+1:]...)
			o.f.deleted = append(o.f.deleted, ns+"/"+name)
			return nil
		}
	}
	return errors.New("pod " + ns + "/" + name + " not found")
}

func (o *fakeK8sOps) ListServices(context.Context) ([]k8s.Service, error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	if !o.f.running {
		return nil, errors.New("k3s is not running")
	}
	return append([]k8s.Service(nil), o.f.services...), nil
}

func (o *fakeK8sOps) ListPodStats(context.Context) ([]k8s.PodStats, error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	if !o.f.running {
		return nil, errors.New("k3s is not running")
	}
	return append([]k8s.PodStats(nil), o.f.stats...), nil
}

func (o *fakeK8sOps) Forward(_ context.Context, req k8s.ForwardRequest) (k8s.PortForward, error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	if req.RemotePort == 0 {
		req.RemotePort = req.LocalPort
	}
	f := k8s.PortForward{Namespace: req.Namespace, Kind: req.Kind, Name: req.Name, LocalPort: req.LocalPort, RemotePort: req.RemotePort, Running: true, URL: k8s.LocalURL(req.LocalPort)}
	out := o.f.forwards[:0:0]
	for _, x := range o.f.forwards {
		if x.LocalPort != req.LocalPort {
			out = append(out, x)
		}
	}
	o.f.forwards = append(out, f)
	return f, nil
}

func (o *fakeK8sOps) ListForwards(context.Context) ([]k8s.PortForward, error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	return append([]k8s.PortForward(nil), o.f.forwards...), nil
}

func (o *fakeK8sOps) StopForward(_ context.Context, port int) error {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	out := o.f.forwards[:0:0]
	for _, x := range o.f.forwards {
		if x.LocalPort != port {
			out = append(out, x)
		}
	}
	o.f.forwards = out
	return nil
}

// FakeKubeconfig 是假 k8s 匯出的 kubeconfig（項目名稱已是 ContextName）。
const FakeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:26443
  name: wsld
contexts:
- context:
    cluster: wsld
    user: wsld
  name: wsld
current-context: wsld
users:
- name: wsld
  user:
    token: fake
`

// addApp 模擬 stack 內一個服務的部署：一個 Running pod（容器同名）＋可選的 Service。呼叫端持鎖。
func (f *FakeK8s) addApp(ns, name, image string, port int, expose string, hostPort int, labels map[string]string) k8s.DeployResult {
	now := time.Now()
	lb := map[string]string{"app": name}
	for k, v := range labels {
		lb[k] = v
	}
	pods := f.pods[:0:0]
	for _, p := range f.pods {
		if !(p.Namespace == ns && p.Labels["app"] == name) {
			pods = append(pods, p)
		}
	}
	f.pods = append(pods, k8s.Pod{Namespace: ns, Name: name + "-6b7f9c-abcde", Status: "Running", Ready: "1/1", IP: "172.17.0.9", Created: now, Labels: lb,
		Containers: []k8s.PodContainer{{Name: name, ID: "f00d" + name, Ready: true, State: "running"}}})
	res := k8s.DeployResult{Namespace: ns, Name: name, Expose: expose}
	if port == 0 {
		return res
	}
	svc := k8s.Service{Namespace: ns, Name: name, Type: "ClusterIP", ClusterIP: "10.43.50.1", Created: now, Labels: lb, URLs: []string{},
		Selector: map[string]string{"app": name}, Ports: []k8s.ServicePort{{Port: port, TargetPort: strconv.Itoa(port), Protocol: "TCP"}}}
	switch expose {
	case k8s.ExposeNodePort:
		svc.Type, svc.Ports[0].NodePort, res.NodePort = "NodePort", 30123, 30123
		svc.URLs = []string{k8s.LocalURL(30123)}
	case k8s.ExposeLoadBalancer:
		svc.Type, svc.Ports[0].Port, svc.Ports[0].NodePort, res.HostPort = "LoadBalancer", hostPort, 30124, hostPort
		svc.External, svc.URLs = []string{"172.20.3.68"}, []string{k8s.LocalURL(hostPort)}
	}
	svcs := f.services[:0:0]
	for _, x := range f.services {
		if !(x.Namespace == ns && x.Name == name) {
			svcs = append(svcs, x)
		}
	}
	f.services = append(svcs, svc)
	return res
}

func (o *fakeK8sOps) Apply(_ context.Context, manifest []byte) (string, error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	o.f.applied = append(o.f.applied, string(manifest))
	if strings.Contains(string(manifest), "fail-apply") {
		return "", errors.New(`error: error parsing manifest: fail-apply`)
	}
	return "deployment.apps/applied created", nil
}

func (o *fakeK8sOps) DeployStack(_ context.Context, spec k8s.StackSpec) (k8s.StackResult, error) {
	s, err := spec.Normalize()
	if err != nil {
		return k8s.StackResult{}, err
	}
	order, _ := s.Order()
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	res := k8s.StackResult{Namespace: s.Namespace, Name: s.Name}
	for _, sv := range order {
		res.Services = append(res.Services, o.f.addApp(s.Namespace, sv.Name, sv.Image, sv.Port, sv.Expose, sv.HostPort, map[string]string{k8s.StackLabel: s.Name}))
	}
	return res, nil
}

func (o *fakeK8sOps) DeleteStack(_ context.Context, ns, name string, _ bool) error {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	pods := o.f.pods[:0:0]
	for _, p := range o.f.pods {
		if !(p.Namespace == ns && p.Labels[k8s.StackLabel] == name) {
			pods = append(pods, p)
		}
	}
	svcs := o.f.services[:0:0]
	for _, x := range o.f.services {
		if !(x.Namespace == ns && x.Labels[k8s.StackLabel] == name) {
			svcs = append(svcs, x)
		}
	}
	o.f.pods, o.f.services = pods, svcs
	return nil
}
