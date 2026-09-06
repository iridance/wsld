package gui

import (
	"context"
	"net"

	"github.com/irice/wsl-docker/internal/host"
)

// DistroShell 開啟連到指定 WSL distro 的雙向 shell（Machines 底部 terminal）。
type DistroShell interface {
	Open(ctx context.Context, distro, user string, cols, rows int) (net.Conn, error)
}

// FakeDistroShell 測試用回顯 shell。
type FakeDistroShell struct{}

// Open 實作 DistroShell。
func (FakeDistroShell) Open(context.Context, string, string, int, int) (net.Conn, error) {
	return OpenFakeShell(true), nil
}

// HostDistroShell 以本機 wsl.exe 開 shell（Windows）。
type HostDistroShell struct{}

// Open 實作 DistroShell。
func (HostDistroShell) Open(_ context.Context, distro, user string, cols, rows int) (net.Conn, error) {
	return host.StartShell(distro, user, cols, rows)
}
