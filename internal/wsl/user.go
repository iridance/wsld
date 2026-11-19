package wsl

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"strings"
)

// CurrentUsername 回傳目前 OS 使用者名稱（可在測試中覆寫）。
var CurrentUsername = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// ValidLinuxUsername 檢查是否為合理的 Linux 登入名（[a-z_][a-z0-9_-]*，≤32）。
func ValidLinuxUsername(s string) bool {
	if s == "" || len(s) > 32 || s == "root" {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if i == 0 {
			ok = r >= 'a' && r <= 'z' || r == '_'
		}
		if !ok {
			return false
		}
	}
	return true
}

// SanitizeLinuxUsername 把 Windows／任意字串收成可用的 Linux 使用者名；無法收斂則回 "wsluser"。
func SanitizeLinuxUsername(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.LastIndexAny(raw, `/\`); i >= 0 {
		raw = raw[i+1:]
	}
	if strings.EqualFold(raw, "root") {
		return "wsluser"
	}
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	s := b.String()
	for strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	if s == "" {
		return "wsluser"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "u" + s
	}
	if len(s) > 32 {
		s = s[:32]
	}
	if !ValidLinuxUsername(s) {
		return "wsluser"
	}
	return s
}

// SuggestLinuxUser 依目前 Windows／主機使用者推一個 Linux 登入名。
func SuggestLinuxUser() string {
	name, err := CurrentUsername()
	if err != nil || name == "" {
		return "wsluser"
	}
	return SanitizeLinuxUsername(name)
}

// ValidLinuxPassword 檢查密碼可安全交給 chpasswd（stdin 一行 "user:password"）。
// 空字串代表「不設密碼」，視為有效；不可含換行、CR 或 NUL。
func ValidLinuxPassword(pw string) bool {
	return !strings.ContainsAny(pw, "\r\n\x00")
}

// UserExists 回報 distro 內是否已有該使用者。
func UserExists(ctx context.Context, r Runner, distro, name string) bool {
	_, _, err := RunIn(ctx, r, distro, "root", nil, "id", "-u", name)
	return err == nil
}

// EnsureUser 確保 distro 內存在非 root 使用者（Add --no-launch 後常見只有 root）。
// 已存在則成功且不動其密碼；否則 useradd -m，password 非空時以 chpasswd（經 stdin，不進 argv）設定，
// 並 best-effort 加入 sudo／wheel。
func EnsureUser(ctx context.Context, r Runner, distro, name, password string) error {
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	if !ValidLinuxUsername(name) {
		return fmt.Errorf("invalid linux username %q", name)
	}
	if !ValidLinuxPassword(password) {
		return errors.New("password must not contain line breaks")
	}
	if UserExists(ctx, r, distro, name) {
		return nil
	}
	_, stderr, err := RunIn(ctx, r, distro, "root", nil, "useradd", "-m", "-s", "/bin/bash", name)
	if err != nil {
		return fmt.Errorf("useradd %s: %w: %s", name, err, strings.TrimSpace(DecodeOutput(stderr)))
	}
	if password != "" {
		in := strings.NewReader(name + ":" + password + "\n")
		if _, stderr, err := RunIn(ctx, r, distro, "root", in, "chpasswd"); err != nil {
			return fmt.Errorf("chpasswd %s: %w: %s", name, err, strings.TrimSpace(DecodeOutput(stderr)))
		}
	}
	_, _, _ = RunIn(ctx, r, distro, "root", nil, "usermod", "-aG", "sudo", name)
	_, _, _ = RunIn(ctx, r, distro, "root", nil, "usermod", "-aG", "wheel", name)
	return nil
}

// WithDefaultUser 回傳把 [user] default=name 併入 wsl.conf 內容後的結果（保留其他區段與鍵）。
func WithDefaultUser(conf, name string) string {
	lines := strings.Split(strings.ReplaceAll(conf, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	sec, userSec, set := "", -1, false
	for i, ln := range lines {
		tr := strings.TrimSpace(ln)
		if strings.HasPrefix(tr, "[") && strings.HasSuffix(tr, "]") {
			sec = strings.ToLower(strings.TrimSpace(tr[1 : len(tr)-1]))
			if sec == "user" && userSec < 0 {
				userSec = i
			}
			continue
		}
		if sec != "user" {
			continue
		}
		if k, _, ok := strings.Cut(tr, "="); ok && strings.EqualFold(strings.TrimSpace(k), "default") {
			lines[i] = "default=" + name
			set = true
		}
	}
	switch {
	case set:
	case userSec >= 0:
		lines = append(lines[:userSec+1], append([]string{"default=" + name}, lines[userSec+1:]...)...)
	default:
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "[user]", "default="+name)
	}
	return strings.Join(lines, "\n") + "\n"
}

// SetDefaultUser 把 /etc/wsl.conf 的 [user] default 設為 name，並 terminate 讓下次啟動生效。
func SetDefaultUser(ctx context.Context, r Runner, distro, name string) error {
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	if !ValidLinuxUsername(name) {
		return fmt.Errorf("invalid linux username %q", name)
	}
	cur := ""
	if _, _, err := RunIn(ctx, r, distro, "root", nil, "test", "-e", "/etc/wsl.conf"); err == nil {
		// 檔案存在卻讀不到時不可覆寫（會丟掉既有設定，如 [boot] systemd）
		out, stderr, err := RunIn(ctx, r, distro, "root", nil, "cat", "/etc/wsl.conf")
		if err != nil {
			return fmt.Errorf("read /etc/wsl.conf: %w: %s", err, strings.TrimSpace(DecodeOutput(stderr)))
		}
		cur = DecodeOutput(out)
	}
	in := strings.NewReader(WithDefaultUser(cur, name))
	if _, stderr, err := RunIn(ctx, r, distro, "root", in, "sh", "-c", "cat > /etc/wsl.conf"); err != nil {
		return fmt.Errorf("write /etc/wsl.conf: %w: %s", err, strings.TrimSpace(DecodeOutput(stderr)))
	}
	return Terminate(ctx, r, distro)
}
