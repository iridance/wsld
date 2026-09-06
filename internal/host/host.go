// Package host 提供本機宿主操作（開啟 WSL 資料夾／終端）。
package host

import (
	"fmt"
	"net"
)

// OpenFolder 在檔案總管開啟 \\wsl$\<distro>\；若提供 user 則開到 home\<user>。
func OpenFolder(distro string, user ...string) error {
	if distro == "" {
		return fmt.Errorf("distro is empty")
	}
	u := ""
	if len(user) > 0 {
		u = user[0]
	}
	return openFolder(distro, u)
}

// OpenTerminal 開啟連到指定 distro 的終端（優先 Windows Terminal）；可選 user。
func OpenTerminal(distro string, user ...string) error {
	if distro == "" {
		return fmt.Errorf("distro is empty")
	}
	u := ""
	if len(user) > 0 {
		u = user[0]
	}
	return openTerminal(distro, u)
}

// StartShell 以 pipe＋Linux script PTY 連到 distro 互動 shell（GUI 內嵌 xterm）。
func StartShell(distro, user string, cols, rows int) (net.Conn, error) {
	if distro == "" {
		return nil, fmt.Errorf("distro is empty")
	}
	return startShell(distro, user, cols, rows)
}
