//go:build windows

package gui

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// RegistryAutostart 以 HKCU\...\Run 實作「登入時啟動 Tray」（預設關閉，由設定頁開啟）。
type RegistryAutostart struct {
	Name    string // 登錄值名稱，例如 "wsld"
	Command string // 例如 "C:\path\wsld-gui.exe" --tray
}

// Enabled 實作 Autostart。
func (a RegistryAutostart) Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(a.Name)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Set 實作 Autostart。
func (a RegistryAutostart) Set(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(a.Name, a.Command)
	}
	if err := k.DeleteValue(a.Name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// PlatformAutostart 回傳目前平台的 Autostart 實作。
func PlatformAutostart(name, command string) Autostart {
	return RegistryAutostart{Name: name, Command: command}
}
