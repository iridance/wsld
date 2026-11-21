//go:build windows

package wsl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const lxssKey = `Software\Microsoft\Windows\CurrentVersion\Lxss`

// VHDXSize 回傳 distro 虛擬磁碟（BasePath\ext4.vhdx）在 Windows 上的檔案大小。
// 讀 HKCU\…\Lxss 的登錄檔，不呼叫 wsl.exe，因此不會喚醒已停止的 distro。
func VHDXSize(name string) (int64, error) {
	p, err := vhdxPath(name)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// VHDXLimit 回傳 distro 虛擬磁碟的容量上限（讀 VHDX 標頭；不喚醒 distro）。
func VHDXLimit(name string) (int64, error) {
	p, err := vhdxPath(name)
	if err != nil {
		return 0, err
	}
	return VHDXVirtualSize(p)
}

// vhdxPath 從 Lxss 登錄檔找出 distro 的 vhdx 路徑。
func vhdxPath(name string) (string, error) {
	root, err := registry.OpenKey(registry.CURRENT_USER, lxssKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return "", err
	}
	defer root.Close()
	subs, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return "", err
	}
	for _, sub := range subs {
		k, err := registry.OpenKey(root, sub, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		dn, _, _ := k.GetStringValue("DistributionName")
		if !strings.EqualFold(dn, name) {
			k.Close()
			continue
		}
		base, _, err := k.GetStringValue("BasePath")
		file, _, ferr := k.GetStringValue("VhdFileName")
		k.Close()
		if err != nil || base == "" {
			return "", errors.New("wsl: no BasePath for " + name)
		}
		if ferr != nil || file == "" {
			file = "ext4.vhdx"
		}
		return filepath.Join(base, file), nil
	}
	return "", errors.New("wsl: distro not in registry: " + name)
}
