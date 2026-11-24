// Package migrate 把 client distro 內舊的本機 dockerd 資料（image、具名 volume）複製到共用 engine。
//
// 全部在 client distro 內以 docker context 完成：來源是 `default`（本機 /var/run/docker.sock），
// 目的地是 `wsld`（engine 的共用 socket）。資料不經過 Windows。不刪任何東西：來源保持原狀。
package migrate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/iridance/wsld/internal/wsl"
)

// ErrNoLocalDocker 表示 client distro 沒有可用的本機 dockerd（沒有要遷移的資料）。
var ErrNoLocalDocker = errors.New("no local dockerd to migrate from")

// Options 選擇要搬什麼。
type Options struct {
	Images  bool
	Volumes bool
	DryRun  bool // 只列出，不複製
}

// Result 回報搬了什麼（DryRun 時為「會搬」的清單）。
type Result struct {
	Images  []string
	Volumes []string
	// Skipped 是刻意略過的項目（沒有 tag 的 image、匿名 volume、名稱不安全者），附原因。
	Skipped []string
}

var (
	imageRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
	volumeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	anonRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Run 在 distro 內把本機 dockerd 的 image／volume 複製到 engine（user 為該 distro 的 Linux 使用者）。
func Run(ctx context.Context, r wsl.Runner, distro, user string, o Options) (Result, error) {
	var res Result
	if err := ensureLocalDocker(ctx, r, distro, user); err != nil {
		return res, err
	}
	if o.Images {
		out, _, err := wsl.RunIn(ctx, r, distro, user, nil, "docker", "--context", "default", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
		if err != nil {
			return res, fmt.Errorf("list images: %w", err)
		}
		for _, ref := range lines(out) {
			switch {
			case strings.HasPrefix(ref, "<none>") || strings.HasSuffix(ref, ":<none>"):
				res.Skipped = append(res.Skipped, ref+" (untagged image)")
			case !imageRe.MatchString(ref):
				res.Skipped = append(res.Skipped, ref+" (unsafe name)")
			default:
				res.Images = append(res.Images, ref)
			}
		}
	}
	if o.Volumes {
		out, _, err := wsl.RunIn(ctx, r, distro, user, nil, "docker", "--context", "default", "volume", "ls", "-q")
		if err != nil {
			return res, fmt.Errorf("list volumes: %w", err)
		}
		for _, v := range lines(out) {
			switch {
			case anonRe.MatchString(v):
				res.Skipped = append(res.Skipped, v[:12]+"… (anonymous volume)")
			case !volumeRe.MatchString(v):
				res.Skipped = append(res.Skipped, v+" (unsafe name)")
			default:
				res.Volumes = append(res.Volumes, v)
			}
		}
	}
	if o.DryRun {
		return res, nil
	}
	if len(res.Images) > 0 {
		quoted := make([]string, len(res.Images))
		for i, ref := range res.Images {
			quoted[i] = "'" + ref + "'"
		}
		script := "set -o pipefail; docker --context default save " + strings.Join(quoted, " ") + " | docker --context wsld load"
		if _, stderr, err := wsl.RunIn(ctx, r, distro, user, nil, "bash", "-c", script); err != nil {
			return res, fmt.Errorf("copy images: %w: %s", err, strings.TrimSpace(wsl.DecodeOutput(stderr)))
		}
	}
	for _, v := range res.Volumes {
		script := fmt.Sprintf("set -o pipefail; docker --context default run --rm -v '%[1]s':/from alpine tar -C /from -cf - . | docker --context wsld run --rm -i -v '%[1]s':/to alpine tar -C /to -xf -", v)
		if _, stderr, err := wsl.RunIn(ctx, r, distro, user, nil, "bash", "-c", script); err != nil {
			return res, fmt.Errorf("copy volume %s: %w: %s", v, err, strings.TrimSpace(wsl.DecodeOutput(stderr)))
		}
	}
	return res, nil
}

// ensureLocalDocker：本機 dockerd 沒在跑就試著啟動（舊資料在它的 data-root 裡）；仍不通就回 ErrNoLocalDocker。
func ensureLocalDocker(ctx context.Context, r wsl.Runner, distro, user string) error {
	ping := func() error {
		_, _, err := wsl.RunIn(ctx, r, distro, user, nil, "docker", "--context", "default", "info")
		return err
	}
	if ping() == nil {
		return nil
	}
	if _, _, err := wsl.RunIn(ctx, r, distro, "root", nil, "systemctl", "start", "docker"); err != nil {
		return fmt.Errorf("%w: %s", ErrNoLocalDocker, distro)
	}
	if err := ping(); err != nil {
		return fmt.Errorf("%w: %s", ErrNoLocalDocker, distro)
	}
	return nil
}

func lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(wsl.DecodeOutput(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
