package wsl

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type testProc struct {
	done chan struct{}
	once sync.Once
}

func (p *testProc) Kill() error { p.once.Do(func() { close(p.done) }); return nil }
func (p *testProc) Wait() error { <-p.done; return nil }

type testSpawner struct {
	mu    sync.Mutex
	calls [][]string
	procs []*testProc
	fail  bool
}

func (s *testSpawner) Start(args ...string) (Proc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return nil, errors.New("boom")
	}
	s.calls = append(s.calls, args)
	p := &testProc{done: make(chan struct{})}
	s.procs = append(s.procs, p)
	return p, nil
}

func (s *testSpawner) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func waitHeld(t *testing.T, k *KeepAlive, want []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := k.Held()
		if reflect.DeepEqual(got, want) || (len(got) == 0 && len(want) == 0) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("held = %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeepAliveHoldIsIdempotentAndUsesSleepInfinity(t *testing.T) {
	sp := &testSpawner{}
	k := &KeepAlive{Spawner: sp}
	if err := k.Hold("Debian"); err != nil {
		t.Fatal(err)
	}
	if err := k.Hold("debian"); err != nil { // 名稱不分大小寫
		t.Fatal(err)
	}
	if sp.n() != 1 {
		t.Fatalf("spawned %d, want 1", sp.n())
	}
	want := []string{"-d", "Debian", "--", "sleep", "infinity"}
	if !reflect.DeepEqual(sp.calls[0], want) {
		t.Fatalf("args = %v", sp.calls[0])
	}
	if got := k.Held(); !reflect.DeepEqual(got, []string{"Debian"}) {
		t.Fatalf("held = %v", got)
	}
}

func TestKeepAliveRejectsBadNameAndSpawnError(t *testing.T) {
	sp := &testSpawner{}
	k := &KeepAlive{Spawner: sp}
	if err := k.Hold("bad name;rm"); err == nil {
		t.Fatal("invalid name must be rejected")
	}
	sp.fail = true
	if err := k.Hold("Debian"); err == nil {
		t.Fatal("spawn error must surface")
	}
	if len(k.Held()) != 0 {
		t.Fatal("failed hold must not be recorded")
	}
}

func TestKeepAliveReleaseKillsAndPauseBlocksEnsure(t *testing.T) {
	sp := &testSpawner{}
	k := &KeepAlive{Spawner: sp}
	_ = k.Hold("Debian")
	k.Release("Debian", true)
	select {
	case <-sp.procs[0].done:
	default:
		t.Fatal("Release must kill the session")
	}
	waitHeld(t, k, nil)
	_ = k.Ensure("Debian")
	if sp.n() != 1 {
		t.Fatal("Ensure must not re-hold a paused distro")
	}
	_ = k.Hold("Debian") // 明確啟動解除暫停
	if sp.n() != 2 {
		t.Fatal("Hold must work again after pause")
	}
	_ = k.Ensure("Debian")
	if sp.n() != 2 {
		t.Fatal("Ensure must not duplicate")
	}
}

func TestKeepAliveProcessExitClearsEntry(t *testing.T) {
	sp := &testSpawner{}
	k := &KeepAlive{Spawner: sp}
	_ = k.Hold("Debian")
	_ = sp.procs[0].Kill() // 模擬 wsl --shutdown：session 自己結束
	waitHeld(t, k, nil)
	_ = k.Ensure("Debian")
	if sp.n() != 2 {
		t.Fatal("Ensure should re-hold after the session ended on its own")
	}
}

func TestKeepAliveHoldOnlyAndReleaseAll(t *testing.T) {
	sp := &testSpawner{}
	k := &KeepAlive{Spawner: sp}
	_ = k.Hold("A")
	_ = k.Hold("B")
	if err := k.HoldOnly("C"); err != nil {
		t.Fatal(err)
	}
	waitHeld(t, k, []string{"C"})
	k.ReleaseAll()
	waitHeld(t, k, nil)
}

func TestKeepAliveNilIsNoop(t *testing.T) {
	var k *KeepAlive
	if err := k.Hold("A"); err != nil {
		t.Fatal(err)
	}
	if err := k.Ensure("A"); err != nil {
		t.Fatal(err)
	}
	if err := k.HoldOnly("A"); err != nil {
		t.Fatal(err)
	}
	k.Release("A", true)
	k.ReleaseAll()
	if k.Held() != nil {
		t.Fatal("nil held")
	}
}

func TestKeepAliveHoldOnlyAcceptsSeveralAndReleasesTheRest(t *testing.T) {
	sp := &testSpawner{}
	k := &KeepAlive{Spawner: sp}
	_ = k.Hold("A")
	_ = k.Hold("B")
	if err := k.HoldOnly("B", "Engine"); err != nil {
		t.Fatal(err)
	}
	waitHeld(t, k, []string{"B", "Engine"})
	if sp.n() != 3 {
		t.Fatalf("B must not be re-spawned, spawns = %d", sp.n())
	}
}

func TestKeepAliveHoldOnlyWithNoNamesReleasesAll(t *testing.T) {
	k := &KeepAlive{Spawner: &testSpawner{}}
	_ = k.Hold("A")
	if err := k.HoldOnly(); err != nil {
		t.Fatal(err)
	}
	waitHeld(t, k, nil)
}
