package host

import (
	"fmt"
	"strconv"
	"strings"
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

// shellQuote 為 POSIX sh 單引號跳脫；只含安全字元時原樣回傳。
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// WSLCommandArgs 組出在 distro 內執行單一指令的 wsl.exe 參數（用於 kubectl logs／exec 串流）。
// tty=false：直接以 pipe 執行（logs）；tty=true：經 Linux script(1) 配 PTY（kubectl exec -it）。
func WSLCommandArgs(distro, user string, argv []string, tty bool, cols, rows int) []string {
	args := []string{"-d", distro}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, "--cd", "~", "--")
	if !tty {
		return append(args, argv...)
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	cs, rs := strconv.Itoa(cols), strconv.Itoa(rows)
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	inner := fmt.Sprintf("export TERM=xterm-256color COLUMNS=%s LINES=%s; stty cols %s rows %s 2>/dev/null; exec %s",
		cs, rs, cs, rs, strings.Join(q, " "))
	return append(args, "script", "-qefc", inner, "/dev/null")
}
