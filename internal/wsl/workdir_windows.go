//go:build windows

package wsl

import "os"

// safeWSLWorkDir 給 wsl.exe 一個非 UNC／非他 distro 路徑的工作目錄。
func safeWSLWorkDir() string {
	if d := os.TempDir(); d != "" {
		return d
	}
	if d := os.Getenv("SystemRoot"); d != "" {
		return d
	}
	return `C:\`
}
