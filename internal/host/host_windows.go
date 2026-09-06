//go:build windows

package host

import (
	"fmt"
	"os/exec"
)

func openFolder(distro string) error {
	// explorer 即使成功也常回非零；Start 後不等待。
	return exec.Command("explorer.exe", `\\wsl$\`+distro+`\`).Start()
}

func openTerminal(distro string) error {
	// Windows Terminal：新分頁跑 wsl -d <distro>
	if err := exec.Command("wt.exe", "-w", "0", "nt", "wsl.exe", "-d", distro).Start(); err == nil {
		return nil
	}
	// 後備：新主控台視窗
	cmd := exec.Command("wsl.exe", "-d", distro)
	cmd.SysProcAttr = newConsoleAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open terminal: %w", err)
	}
	return nil
}
