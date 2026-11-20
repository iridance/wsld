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
	root, err := registry.OpenKey(registry.CURRENT_USER, lxssKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	subs, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return 0, err
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
			return 0, errors.New("wsl: no BasePath for " + name)
		}
		if ferr != nil || file == "" {
			file = "ext4.vhdx"
		}
		st, err := os.Stat(filepath.Join(base, file))
		if err != nil {
			return 0, err
		}
		return st.Size(), nil
	}
	return 0, errors.New("wsl: distro not in registry: " + name)
}
