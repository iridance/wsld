//go:build !windows

package lan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type otherHost struct{}

// NewHost 回傳目前平台的 Host（非 Windows：只讀，不支援切換）。
func NewHost() Host { return otherHost{} }

func (otherHost) ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".wslconfig"), nil
}

func (otherHost) Build() uint32 { return 0 }

func (otherHost) SetFirewall(context.Context, bool) error {
	return errors.New("LAN access is only supported on Windows")
}

func (otherHost) Probe(context.Context) (Probe, error) {
	return Probe{Rules: -1}, errors.New("LAN access is only supported on Windows")
}

func (otherHost) Interfaces() ([]Iface, error) { return SystemInterfaces() }
