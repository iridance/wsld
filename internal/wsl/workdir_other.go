//go:build !windows

package wsl

func safeWSLWorkDir() string { return "" }
