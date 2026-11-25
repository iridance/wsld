package gui

import (
	"context"
	"errors"
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
	enables   int
	starts    int
	disables  int
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
func (f *FakeK8s) SetInstalled(v bool)      { f.installed = v }
func (f *FakeK8s) SetRunning(v bool)        { f.running = v }
func (f *FakeK8s) SetFailStep(s string)     { f.failStep = s }
func (f *FakeK8s) SetDelay(d time.Duration) { f.delay = d }
func (f *FakeK8s) SetPods(p []k8s.Pod)      { f.pods = p }
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
	f.enables, f.starts, f.disables = 0, 0, 0
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
	return []byte("apiVersion: v1\ncurrent-context: " + k8s.ContextName + "\n"), nil
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
