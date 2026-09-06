// Package host 提供本機宿主操作（開啟 WSL 資料夾／終端）。
package host

import "fmt"

// OpenFolder 在檔案總管開啟 \\wsl$\<distro>\。
func OpenFolder(distro string) error {
	if distro == "" {
		return fmt.Errorf("distro is empty")
	}
	return openFolder(distro)
}

// OpenTerminal 開啟連到指定 distro 的終端（優先 Windows Terminal）。
func OpenTerminal(distro string) error {
	if distro == "" {
		return fmt.Errorf("distro is empty")
	}
	return openTerminal(distro)
}
