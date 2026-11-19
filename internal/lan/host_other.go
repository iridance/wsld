//go:build !windows

package lan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type otherHost struct{}

// NewHost 回傳目前平台的 Host（非 Windows：只讀，不支援移除防火牆規則）。
func NewHost() Host { return otherHost{} }

func (otherHost) ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".wslconfig"), nil
}

func (otherHost) RemoveFirewallRules(context.Context) error {
	return errors.New("firewall rules can only be removed on Windows")
}

func (otherHost) FirewallRules(context.Context) int { return 0 }

func (otherHost) Interfaces() ([]Iface, error) { return SystemInterfaces() }

func (otherHost) PortInUse(context.Context, int) (bool, error) { return false, nil }

func (otherHost) AppRules(context.Context, string) ([]FwRule, error) {
	return nil, errors.New("firewall rules can only be read on Windows")
}

func (otherHost) RunElevated(context.Context, string, string) error {
	return errors.New("firewall rules can only be added on Windows")
}
