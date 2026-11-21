//go:build !windows

package wsl

import "errors"

// VHDXSize 只在 Windows 可用（讀 Lxss 登錄檔）。
func VHDXSize(string) (int64, error) {
	return 0, errors.New("wsl: vhdx size unsupported on this platform")
}

// VHDXLimit 只在 Windows 可用（讀 Lxss 登錄檔找 vhdx）。
func VHDXLimit(string) (int64, error) {
	return 0, errors.New("wsl: vhdx limit unsupported on this platform")
}
