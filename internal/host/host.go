// Package host 提供本機宿主操作（開啟 WSL 資料夾／終端）。
package host

import (
	"fmt"
	"net"
)

// OpenFolder 在檔案總管開啟 \\wsl$\<distro>\；若提供 user 則開到他的家目錄（見 FolderPath）。
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

// StartCommand 在 distro 內執行單一指令並回傳雙向連線（GUI 的 kubectl logs／exec 串流）。
// tty=true 時以 script(1) 配 PTY；連線關閉即終止程序。
func StartCommand(distro, user string, argv []string, tty bool, cols, rows int) (net.Conn, error) {
	if distro == "" {
		return nil, fmt.Errorf("distro is empty")
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("command is empty")
	}
	return startProc(WSLCommandArgs(distro, user, argv, tty, cols, rows))
}

// FolderPath 回傳檔案總管要開的 UNC 路徑：\\wsl$\<distro>\，有 user 時為他的家目錄
// （root 是 \root，其他使用者是 \home\<user>；k8s-engine 以 root 登記）。
func FolderPath(distro, user string) string {
	path := `\\wsl$\` + distro + `\`
	switch user {
	case "":
	case "root":
		path += "root"
	default:
		path += `home\` + user
	}
	return path
}
