package wsl

import (
	"context"
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

// EnsureUser 確保 distro 內存在非 root 使用者（Add --no-launch 後常見只有 root）。
// 已存在則成功；否則 useradd -m，並 best-effort 加入 sudo／wheel。
func EnsureUser(ctx context.Context, r Runner, distro, name string) error {
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	if !ValidLinuxUsername(name) {
		return fmt.Errorf("invalid linux username %q", name)
	}
	if _, _, err := RunIn(ctx, r, distro, "root", nil, "id", "-u", name); err == nil {
		return nil
	}
	_, stderr, err := RunIn(ctx, r, distro, "root", nil, "useradd", "-m", "-s", "/bin/bash", name)
	if err != nil {
		return fmt.Errorf("useradd %s: %w: %s", name, err, strings.TrimSpace(DecodeOutput(stderr)))
	}
	_, _, _ = RunIn(ctx, r, distro, "root", nil, "usermod", "-aG", "sudo", name)
	_, _, _ = RunIn(ctx, r, distro, "root", nil, "usermod", "-aG", "wheel", name)
	return nil
}
