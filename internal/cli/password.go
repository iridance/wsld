package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/wsl"
)

// ErrNoTerminal 表示 stdin 不是終端機，無法以隱藏輸入詢問密碼。
var ErrNoTerminal = errors.New("stdin is not a terminal")

// TerminalPassword 在終端機上顯示 prompt 並以隱藏輸入讀一行（不回顯）。stdin 非終端機時回 ErrNoTerminal。
func TerminalPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", ErrNoTerminal
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

// readPasswordLine 從 r 讀第一行（去掉結尾 CR/LF）。
func readPasswordLine(r io.Reader) (string, error) {
	if r == nil {
		return "", errors.New("no stdin")
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// newUserPassword 回傳 lifecycle.InitOptions.Password：僅在要新建 Linux 使用者時被呼叫。
// fromStdin → 讀 a.Stdin 第一行；否則以 a.ReadPassword 隱藏輸入兩次確認；無終端機則不設密碼。
func (a *App) newUserPassword(fromStdin bool) func(user string) (string, error) {
	return func(user string) (string, error) {
		if fromStdin {
			pw, err := readPasswordLine(a.Stdin)
			if err != nil {
				return "", err
			}
			if !wsl.ValidLinuxPassword(pw) {
				return "", errors.New(i18n.T("gui.machines.password_invalid"))
			}
			return pw, nil
		}
		if a.ReadPassword == nil {
			return "", nil
		}
		for range 3 {
			pw, err := a.ReadPassword(i18n.T("cli.password_prompt", user))
			if errors.Is(err, ErrNoTerminal) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			if pw == "" {
				return "", nil
			}
			again, err := a.ReadPassword(i18n.T("cli.password_confirm"))
			if err != nil {
				return "", err
			}
			if again == pw {
				return pw, nil
			}
			fmt.Fprintln(a.Stderr, i18n.T("gui.machines.password_mismatch"))
		}
		return "", errors.New(i18n.T("gui.machines.password_mismatch"))
	}
}
