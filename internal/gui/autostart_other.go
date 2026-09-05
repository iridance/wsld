//go:build !windows

package gui

// PlatformAutostart 非 Windows：不支援。
func PlatformAutostart(_, _ string) Autostart { return UnsupportedAutostart{} }
