//go:build windows

package lan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iridance/wsld/internal/procutil"
)

// SystemHost 是真實 Windows 實作。
type SystemHost struct{}

// NewHost 回傳目前平台的 Host。
func NewHost() Host { return SystemHost{} }

// ConfigPath 實作 Host。
func (SystemHost) ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".wslconfig"), nil
}

// RemoveFirewallRules 實作 Host：跳出 UAC 以系統管理員執行移除腳本。
func (SystemHost) RemoveFirewallRules(ctx context.Context) error {
	_, err := runPowerShell(ctx, ElevateScript(RemoveFirewallScript()))
	return err
}

// FirewallRules 實作 Host。
func (SystemHost) FirewallRules(ctx context.Context) int {
	out, err := runPowerShell(ctx, RulesScript())
	if err != nil {
		return -1
	}
	return ParseRules(out)
}

// Interfaces 實作 Host。
func (SystemHost) Interfaces() ([]Iface, error) { return SystemInterfaces() }

// PortInUse 實作 Host：以 netstat.exe 檢查（含 TIME_WAIT）。
func (SystemHost) PortInUse(ctx context.Context, port int) (bool, error) {
	cmd := exec.CommandContext(ctx, "netstat.exe", "-ano", "-p", "TCP")
	procutil.HideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return NetstatPortInUse(string(out), port), nil
}

func runPowerShell(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", EncodeCommand(script))
	procutil.HideConsole(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		msg := strings.TrimSpace(errb.String())
		if errors.As(err, &ee) && msg == "" {
			msg = fmt.Sprintf("exit %d", ee.ExitCode())
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}
