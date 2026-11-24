package wsl

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/iridance/wsld/internal/procutil"
)

// Proc 是一個長駐的 wsl.exe 子程序。
type Proc interface {
	// Kill 結束程序（wsl.exe 用戶端關閉後 WSL 才會開始回收 distro）。
	Kill() error
	// Wait 在程序結束後回傳。
	Wait() error
}

// Spawner 啟動長駐的 wsl.exe 子程序（與只能跑完即回的 Runner 分開）。
type Spawner interface {
	Start(args ...string) (Proc, error)
}

type execProc struct{ cmd *exec.Cmd }

func (p execProc) Kill() error { return p.cmd.Process.Kill() }
func (p execProc) Wait() error { return p.cmd.Wait() }

// Start 實作 Spawner：背景啟動 wsl.exe（不收集輸出、無主控台視窗）。
func (r ExecRunner) Start(args ...string) (Proc, error) {
	path := r.Path
	if path == "" {
		path = "wsl.exe"
	}
	cmd := exec.Command(path, args...)
	if dir := safeWSLWorkDir(); dir != "" {
		cmd.Dir = dir
	}
	procutil.HideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProc{cmd: cmd}, nil
}

// KeepAlive 替指定的 distro 掛著一個 `wsl -d <distro> -- sleep infinity` session。
// WSL 在最後一個 wsl.exe session 結束後約 20 秒就回收 distro（systemd 服務不算 session），
// 所以主要機器要靠這個 session 才不會自己停止。session 屬於呼叫端程序（wsld-gui），
// 程序結束或 ReleaseAll 後 distro 就回到 WSL 預設的閒置回收。
type KeepAlive struct {
	Spawner Spawner

	mu     sync.Mutex
	procs  map[string]*keepProc // key：小寫 distro 名稱
	paused map[string]bool      // 使用者明確停止過：Ensure 不再自動重掛，直到下次 Hold
}

type keepProc struct {
	name string
	proc Proc
}

func keepKey(distro string) string { return strings.ToLower(distro) }

// Hold 確保 distro 有保活 session（已有則不重複；會喚醒停止中的 distro）。
func (k *KeepAlive) Hold(distro string) error {
	if k == nil || k.Spawner == nil {
		return nil
	}
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.paused, keepKey(distro))
	return k.holdLocked(distro)
}

// Ensure 同 Hold，但略過使用者明確停止過的 distro（供狀態輪詢自動補掛用）。
func (k *KeepAlive) Ensure(distro string) error {
	if k == nil || k.Spawner == nil {
		return nil
	}
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.paused[keepKey(distro)] {
		return nil
	}
	return k.holdLocked(distro)
}

func (k *KeepAlive) holdLocked(distro string) error {
	key := keepKey(distro)
	if _, ok := k.procs[key]; ok {
		return nil
	}
	p, err := k.Spawner.Start("-d", distro, "--", "sleep", "infinity")
	if err != nil {
		return fmt.Errorf("keep-alive %s: %w", distro, err)
	}
	kp := &keepProc{name: distro, proc: p}
	if k.procs == nil {
		k.procs = map[string]*keepProc{}
	}
	k.procs[key] = kp
	go func() {
		_ = p.Wait()
		k.mu.Lock()
		if k.procs[key] == kp {
			delete(k.procs, key) // distro 被關掉（wsl --shutdown／--terminate）→ session 隨之結束
		}
		k.mu.Unlock()
	}()
	return nil
}

// Release 放掉 distro 的保活 session。pause 為 true 時，Ensure 之後不會自動重掛（使用者明確停止）。
func (k *KeepAlive) Release(distro string, pause bool) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if pause {
		if k.paused == nil {
			k.paused = map[string]bool{}
		}
		k.paused[keepKey(distro)] = true
	}
	k.releaseLocked(keepKey(distro))
}

func (k *KeepAlive) releaseLocked(key string) {
	if kp, ok := k.procs[key]; ok {
		_ = kp.proc.Kill()
		delete(k.procs, key)
	}
}

// ReleaseAll 放掉全部 session（wsld-gui 結束、或 wsl --shutdown 之前）。
func (k *KeepAlive) ReleaseAll() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for key := range k.procs {
		k.releaseLocked(key)
	}
}

// Held 回傳目前保活中的 distro 名稱（排序後）。
func (k *KeepAlive) Held() []string {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.procs))
	for _, kp := range k.procs {
		out = append(out, kp.name)
	}
	sort.Strings(out)
	return out
}

// HoldOnly 保活 distro，並放掉其他所有 session（「設為主要」換手用）。
func (k *KeepAlive) HoldOnly(distro string) error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	for key := range k.procs {
		if key != keepKey(distro) {
			k.releaseLocked(key)
		}
	}
	k.mu.Unlock()
	return k.Hold(distro)
}
