//go:build windows

package host

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"

	"github.com/iridance/wsld/internal/procutil"
)

func openFolder(distro, user string) error {
	path := FolderPath(distro, user)
	// explorer 即使成功也常回非零；Start 後不等待。
	return exec.Command("explorer.exe", path).Start()
}

func openTerminal(distro, user string) error {
	args := []string{"-w", "0", "nt", "wsl.exe", "-d", distro}
	if user != "" {
		args = append(args, "-u", user)
	}
	// --cd ~ 避免「failed to translate」Windows 工作目錄
	args = append(args, "--cd", "~", "--", "bash", "-lc", "cd ~ && exec bash")
	if err := exec.Command("wt.exe", args...).Start(); err == nil {
		return nil
	}
	cmdArgs := []string{"-d", distro}
	if user != "" {
		cmdArgs = append(cmdArgs, "-u", user)
	}
	cmdArgs = append(cmdArgs, "--cd", "~")
	cmd := exec.Command("wsl.exe", cmdArgs...)
	cmd.SysProcAttr = newConsoleAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open terminal: %w", err)
	}
	return nil
}

func startShell(distro, user string, cols, rows int) (net.Conn, error) {
	return startProc(WSLShellArgs(distro, user, cols, rows))
}

// startProc 以 wsl.exe 啟動並把 stdin／stdout／stderr 接成單一連線；連線關閉即終止程序。
func startProc(args []string) (net.Conn, error) {
	cmd := exec.Command("wsl.exe", args...)
	// 即使有 --cd ~，也避免 GUI 進程的 Windows CWD 被拿去翻譯
	cmd.Dir = os.TempDir()
	procutil.HideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start wsl: %w", err)
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		defer func() { _ = cmd.Process.Kill() }()
		out := &lockedWriter{w: server}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(out, stdout) }()
		go func() { defer wg.Done(); _, _ = io.Copy(out, stderr) }()
		_, _ = io.Copy(stdin, server)
		_ = stdin.Close()
		wg.Wait()
		_ = cmd.Wait()
	}()
	return client, nil
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
