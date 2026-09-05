// Package procutil：子程序啟動的平台細節。
//
// wsld-gui 以 -H windowsgui 建置、沒有主控台；若直接 exec wsl.exe／tasklist.exe，
// Windows 會替每個子程序開一個會閃現的黑色主控台視窗。HideConsole 讓子程序不建立視窗。
package procutil

import "os/exec"

// HideConsole 讓 cmd 啟動時不顯示主控台視窗（非 Windows 為 no-op）。
// 只適用於以 pipe 收集輸出的背景呼叫；需要互動 TTY 的命令（wsld exec）不要用。
func HideConsole(cmd *exec.Cmd) {
	hideConsole(cmd)
}
