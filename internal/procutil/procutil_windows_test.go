//go:build windows

package procutil

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHideConsoleSetsNoWindowFlags(t *testing.T) {
	cmd := exec.Command("wsl.exe", "-l", "-v")
	HideConsole(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow must be set")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("CREATE_NO_WINDOW must be set, flags=%#x", cmd.SysProcAttr.CreationFlags)
	}
}

func TestHideConsolePreservesExistingAttrs(t *testing.T) {
	cmd := exec.Command("tasklist.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	HideConsole(cmd)
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatal("existing CreationFlags must be preserved")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatal("no-window flags must be added")
	}
}

// 真的啟動一個主控台程式：有 CREATE_NO_WINDOW 時不應失敗，且輸出可正常收集。
func TestHideConsoleCommandStillRuns(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo hi")
	HideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(out) != "hi\r\n" {
		t.Fatalf("unexpected output %q", out)
	}
}
