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

	"golang.org/x/sys/windows"

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

// Build 實作 Host。
func (SystemHost) Build() uint32 {
	return windows.RtlGetVersion().BuildNumber
}

// SetFirewall 實作 Host：跳出 UAC 以系統管理員執行防火牆腳本。
func (SystemHost) SetFirewall(ctx context.Context, enable bool) error {
	_, err := runPowerShell(ctx, ElevateScript(FirewallScript(enable)))
	return err
}

// Probe 實作 Host。
func (SystemHost) Probe(ctx context.Context) (Probe, error) {
	out, err := runPowerShell(ctx, ProbeScript())
	if err != nil {
		return Probe{Rules: -1}, err
	}
	return ParseProbe(out)
}

// Interfaces 實作 Host。
func (SystemHost) Interfaces() ([]Iface, error) { return SystemInterfaces() }

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
