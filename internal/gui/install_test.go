package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// fakeProbe 可控制「已下載位元組」與「總大小」。
type fakeProbe struct {
	mu       sync.Mutex
	size     int64
	exists   bool
	total    int64
	totalErr error
}

func (p *fakeProbe) TotalSize(context.Context, string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total, p.totalErr
}

func (p *fakeProbe) Downloaded(string) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.size, p.exists
}

func (p *fakeProbe) set(size int64, exists bool) {
	p.mu.Lock()
	p.size, p.exists = size, exists
	p.mu.Unlock()
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time            { return c.t }
func (c *fakeClock) advance(d time.Duration)   { c.t = c.t.Add(d) }
func newClock() *fakeClock                     { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }
func (tr *installTracker) totalNow(n int64)    { tr.setTotal("AzureLinux-4", n, nil) }
func (tr *installTracker) totalFailed(e error) { tr.setTotal("AzureLinux-4", 0, e) }

func TestInstallTrackerPhasesAndSpeed(t *testing.T) {
	p := &fakeProbe{}
	clk := newClock()
	tr := &installTracker{probe: p, now: clk.now}

	if got := tr.snapshot(); got.Active {
		t.Fatalf("idle tracker should be inactive: %+v", got)
	}
	if !tr.begin("AzureLinux-4", "az") {
		t.Fatal("begin should succeed when idle")
	}
	if tr.begin("Debian", "Debian") {
		t.Fatal("second concurrent install must be rejected")
	}

	// 尚未建立暫存檔、總大小未知 → preparing
	got := tr.snapshot()
	if !got.Active || got.Phase != "preparing" || got.Distribution != "AzureLinux-4" || got.Name != "az" {
		t.Fatalf("preparing = %+v", got)
	}

	tr.totalNow(200_000_000)
	p.set(10_000_000, true)
	clk.advance(2 * time.Second)
	got = tr.snapshot()
	if got.Phase != "downloading" || got.Downloaded != 10_000_000 || got.Total != 200_000_000 || got.ElapsedSec != 2 {
		t.Fatalf("downloading = %+v", got)
	}

	// 每秒 250 KB → 速度以最近幾秒平均
	for i := 0; i < 4; i++ {
		clk.advance(time.Second)
		p.set(10_000_000+int64(i+1)*250_000, true)
		got = tr.snapshot()
	}
	if got.BytesPerSec != 250_000 {
		t.Fatalf("speed = %d, want 250000 (%+v)", got.BytesPerSec, got)
	}

	// 下載完成（達總大小）→ installing（wsl 匯入 vhdx 中）
	p.set(200_000_000, true)
	clk.advance(time.Second)
	if got = tr.snapshot(); got.Phase != "installing" || got.Downloaded != 200_000_000 {
		t.Fatalf("download complete = %+v", got)
	}
	// 暫存檔被刪除後仍為 installing，不回到 preparing
	p.set(0, false)
	if got = tr.snapshot(); got.Phase != "installing" {
		t.Fatalf("after temp removed = %+v", got)
	}

	tr.end()
	if got = tr.snapshot(); got.Active {
		t.Fatalf("ended tracker should be inactive: %+v", got)
	}
	if !tr.begin("Debian", "Debian") {
		t.Fatal("begin after end should succeed")
	}
}

func TestInstallTrackerUntrackedDistro(t *testing.T) {
	// 不在 modern 清單（無 .wsl 暫存檔可追）→ 直接顯示 installing（只有經過時間）
	tr := &installTracker{probe: &fakeProbe{}, now: newClock().now}
	tr.begin("AzureLinux-4", "AzureLinux-4")
	tr.totalFailed(errors.New("not in manifest"))
	if got := tr.snapshot(); got.Phase != "installing" || got.Total != 0 {
		t.Fatalf("untracked = %+v", got)
	}
	// 過期的 setTotal（不同發行版）不得覆寫
	tr.end()
	tr.begin("Debian", "Debian")
	tr.totalNow(123)
	if got := tr.snapshot(); got.Total != 0 || got.Phase != "preparing" {
		t.Fatalf("stale total leaked: %+v", got)
	}
}

func TestInstallTrackerWithoutProbe(t *testing.T) {
	tr := &installTracker{now: newClock().now}
	tr.begin("Debian", "Debian")
	if got := tr.snapshot(); !got.Active || got.Phase != "installing" {
		t.Fatalf("no probe = %+v", got)
	}
}

func TestInstallProgressEndpoint(t *testing.T) {
	e := newEnv(t, "")
	w := NewFakeWSL()
	hold := make(chan struct{})
	w.InstallHold = hold
	e.s.WSL = w
	p := &fakeProbe{total: 1000}
	p.set(400, true)
	e.s.InstallProbe = p

	resp, b := e.req(t, http.MethodGet, "/api/machines/install/progress", "")
	var got InstallProgress
	_ = json.Unmarshal(b, &got)
	if resp.StatusCode != 200 || got.Active {
		t.Fatalf("idle progress = %d %s", resp.StatusCode, b)
	}

	done := make(chan int, 1)
	go func() {
		r, _ := e.req(t, http.MethodPost, "/api/machines/install", `{"distribution":"Debian","name":"Deb"}`)
		done <- r.StatusCode
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, b = e.req(t, http.MethodGet, "/api/machines/install/progress", "")
		got = InstallProgress{}
		_ = json.Unmarshal(b, &got)
		if got.Active && got.Total == 1000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress never became active with total: %s", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Phase != "downloading" || got.Downloaded != 400 || got.Distribution != "Debian" || got.Name != "Deb" {
		t.Fatalf("in-flight progress = %+v", got)
	}

	// 同時第二個安裝 → 409
	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"name":"Alpine"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("concurrent install = %d %s", resp.StatusCode, b)
	}

	close(hold)
	if code := <-done; code != 200 {
		t.Fatalf("install = %d", code)
	}
	_, b = e.req(t, http.MethodGet, "/api/machines/install/progress", "")
	got = InstallProgress{}
	_ = json.Unmarshal(b, &got)
	if got.Active {
		t.Fatalf("progress should be inactive after install: %s", b)
	}
}

func TestInstallProgressShowsJoiningOnceTheDistroIsInstalled(t *testing.T) {
	tr := &installTracker{}
	if !tr.begin("Debian", "Debian1") {
		t.Fatal("begin")
	}
	if got := tr.snapshot().Phase; got == "joining" {
		t.Fatalf("not joining yet: %s", got)
	}
	tr.setJoining()
	p := tr.snapshot()
	if !p.Active || p.Phase != "joining" {
		t.Fatalf("snapshot = %+v", p)
	}
	tr.end()
	if tr.snapshot().Active {
		t.Fatal("must be inactive after end")
	}
}
