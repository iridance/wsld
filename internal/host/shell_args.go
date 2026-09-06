package host

import (
	"fmt"
	"strconv"
)

// WSLShellArgs 組出互動 shell 的 wsl.exe 參數。
// 必須經 Linux script(1) 配 PTY：純 pipe 時 bash 非互動會立刻結束，GUI terminal「進不去」。
// --cd ~：避免繼承 Windows CWD 時出現「wsl: failed to translate ...」（常見於非預設 distro）。
func WSLShellArgs(distro, user string, cols, rows int) []string {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	cs, rs := strconv.Itoa(cols), strconv.Itoa(rows)
	inner := fmt.Sprintf(
		"export TERM=xterm-256color COLUMNS=%s LINES=%s; stty cols %s rows %s 2>/dev/null; cd ~ && exec bash -li",
		cs, rs, cs, rs,
	)
	args := []string{"-d", distro}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, "--cd", "~", "--", "script", "-qefc", inner, "/dev/null")
	return args
}
