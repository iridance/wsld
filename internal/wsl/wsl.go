// Package wsl 包裝 wsl.exe：列出發行版、在發行版內執行命令。
// 所有對外呼叫都透過 Runner 介面，便於以 fake 測試。
package wsl

import (
	"github.com/iridance/wsld/internal/procutil"

	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Runner 執行 wsl.exe。
type Runner interface {
	// Run 以 args 執行 wsl.exe，stdin 可為 nil。stdout/stderr 為原始 bytes（呼叫端視需要 DecodeOutput）。
	Run(ctx context.Context, stdin io.Reader, args ...string) (stdout, stderr []byte, err error)
}

// ExitError 表示 wsl.exe 或其內部命令以非 0 結束。
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("exit %d: %s", e.Code, strings.TrimSpace(e.Stderr))
	}
	return fmt.Sprintf("exit %d", e.Code)
}

// ExitCode 取出 ExitError 的碼；nil→0；非 ExitError→-1。
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *ExitError
	if errors.As(err, &e) {
		return e.Code
	}
	return -1
}

// ExecRunner 以 os/exec 呼叫真正的 wsl.exe。
type ExecRunner struct {
	Path string // 預設 "wsl.exe"
}

// Run 實作 Runner。
func (r ExecRunner) Run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	path := r.Path
	if path == "" {
		path = "wsl.exe"
	}
	cmd := exec.CommandContext(ctx, path, args...)
	procutil.HideConsole(cmd) // GUI（無主控台）呼叫時不可閃出黑視窗
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = stdin
	}
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.Bytes(), errb.Bytes(), &ExitError{Code: ee.ExitCode(), Stderr: DecodeOutput(errb.Bytes())}
		}
		return out.Bytes(), errb.Bytes(), err
	}
	return out.Bytes(), errb.Bytes(), nil
}

// DecodeOutput 把 wsl.exe 自身輸出（UTF-16LE，可能無 BOM）轉為 UTF-8；已是 UTF-8 則原樣回傳。
func DecodeOutput(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return decodeUTF16LE(b[2:])
	}
	if looksUTF16LE(b) {
		return decodeUTF16LE(b)
	}
	return string(b)
}

func looksUTF16LE(b []byte) bool {
	if len(b) < 2 || len(b)%2 != 0 {
		return false
	}
	if utf8.Valid(b) && !bytes.Contains(b, []byte{0}) {
		return false
	}
	// 奇數位置多為 0 → UTF-16LE ASCII 範圍
	zeros := 0
	for i := 1; i < len(b); i += 2 {
		if b[i] == 0 {
			zeros++
		}
	}
	return zeros*2 >= len(b)/2 // 至少一半的高位元組為 0
}

func decodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// Distro 對應 `wsl -l -v` 的一列。
type Distro struct {
	Name    string
	State   string
	Version int
	Default bool
}

// ParseList 解析 `wsl --list --verbose` 已解碼文字。
func ParseList(txt string) []Distro {
	var out []Distro
	for _, line := range strings.Split(strings.ReplaceAll(txt, "\r", ""), "\n") {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.TrimLeft(trimmed, "* "), "NAME") {
			continue
		}
		def := strings.HasPrefix(trimmed, "*")
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, "*"))
		f := strings.Fields(body)
		if len(f) < 3 {
			continue
		}
		v, err := strconv.Atoi(f[len(f)-1])
		if err != nil {
			continue
		}
		out = append(out, Distro{Name: f[0], State: f[1], Version: v, Default: def})
	}
	return out
}

// ListDistros 執行 `wsl --list --verbose`。
func ListDistros(ctx context.Context, r Runner) ([]Distro, error) {
	out, stderr, err := r.Run(ctx, nil, "--list", "--verbose")
	if err != nil {
		// 無任何發行版時 wsl 也會以非 0 結束
		txt := DecodeOutput(out) + DecodeOutput(stderr)
		if strings.Contains(txt, "no installed distributions") {
			return nil, nil
		}
		return nil, fmt.Errorf("wsl --list --verbose: %w", err)
	}
	return ParseList(DecodeOutput(out)), nil
}

// FindDistro 不分大小寫尋找。
func FindDistro(list []Distro, name string) (Distro, bool) {
	for _, d := range list {
		if strings.EqualFold(d.Name, name) {
			return d, true
		}
	}
	return Distro{}, false
}

// PickUbuntu 挑選要沿用的 Ubuntu（WSL2）：預設者優先，否則第一個 Ubuntu*。
func PickUbuntu(list []Distro) (Distro, bool) {
	var first *Distro
	for i := range list {
		d := list[i]
		if d.Version != 2 || !strings.HasPrefix(strings.ToLower(d.Name), "ubuntu") {
			continue
		}
		if d.Default {
			return d, true
		}
		if first == nil {
			first = &list[i]
		}
	}
	if first != nil {
		return *first, true
	}
	return Distro{}, false
}

// RunIn 在 distro 內執行命令：wsl -d <distro> [-u <user>] -- cmd args...
func RunIn(ctx context.Context, r Runner, distro, user string, stdin io.Reader, cmd ...string) ([]byte, []byte, error) {
	args := []string{"-d", distro}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, "--")
	args = append(args, cmd...)
	return r.Run(ctx, stdin, args...)
}

// Wake 喚醒 distro（執行 true）。
func Wake(ctx context.Context, r Runner, distro string) error {
	_, _, err := RunIn(ctx, r, distro, "", nil, "true")
	return err
}

// Terminate 執行 wsl --terminate <distro>。
func Terminate(ctx context.Context, r Runner, distro string) error {
	_, _, err := r.Run(ctx, nil, "--terminate", distro)
	return err
}

// Unregister 執行 wsl --unregister <distro>（永久刪除發行版）。
func Unregister(ctx context.Context, r Runner, distro string) error {
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	_, stderr, err := r.Run(ctx, nil, "--unregister", distro)
	if err != nil {
		return fmt.Errorf("wsl --unregister %s: %w: %s", distro, err, strings.TrimSpace(DecodeOutput(stderr)))
	}
	return nil
}

// StopDocker 在指定 distro 內 systemctl stop docker（best-effort；失敗由呼叫端決定）。
func StopDocker(ctx context.Context, r Runner, distro string) error {
	_, stderr, err := RunIn(ctx, r, distro, "root", nil, "systemctl", "stop", "docker")
	if err != nil {
		return fmt.Errorf("systemctl stop docker: %w: %s", err, strings.TrimSpace(DecodeOutput(stderr)))
	}
	return nil
}

// OnlineDistro 為 `wsl --list --online` 的一列。
type OnlineDistro struct {
	Name     string
	Friendly string
}

// ParseOnline 解析 `wsl --list --online` 已解碼文字：略過說明行與 NAME 標題列。
func ParseOnline(txt string) []OnlineDistro {
	var out []OnlineDistro
	started := false
	for _, line := range strings.Split(strings.ReplaceAll(txt, "\r", ""), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !started {
			if strings.HasPrefix(t, "NAME") {
				started = true
			}
			continue
		}
		f := strings.Fields(t)
		if len(f) == 0 {
			continue
		}
		out = append(out, OnlineDistro{Name: f[0], Friendly: strings.Join(f[1:], " ")})
	}
	return out
}

// ListOnline 執行 `wsl --list --online`。
func ListOnline(ctx context.Context, r Runner) ([]OnlineDistro, error) {
	out, _, err := r.Run(ctx, nil, "--list", "--online")
	if err != nil {
		return nil, fmt.Errorf("wsl --list --online: %w", err)
	}
	return ParseOnline(DecodeOutput(out)), nil
}

// Install 執行 `wsl --install -d <distribution> [--name <name>] --no-launch`。
// distribution 為線上目錄名稱；name 為本機自訂名稱（空則與 distribution 相同）。
func Install(ctx context.Context, r Runner, distribution, name string) error {
	if !ValidDistroName(distribution) {
		return fmt.Errorf("invalid distribution %q", distribution)
	}
	if name == "" {
		name = distribution
	}
	if !ValidDistroName(name) {
		return fmt.Errorf("invalid distro name %q", name)
	}
	args := []string{"--install", "-d", distribution}
	if name != distribution {
		args = append(args, "--name", name)
	}
	args = append(args, "--no-launch")
	_, stderr, err := r.Run(ctx, nil, args...)
	if err != nil {
		return fmt.Errorf("wsl --install %s: %w: %s", distribution, err, strings.TrimSpace(DecodeOutput(stderr)))
	}
	return nil
}

// ValidDistroName 檢查 distro 名稱是否只含 [A-Za-z0-9._-] 且長度 1..64。
func ValidDistroName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
