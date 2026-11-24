package gui

import (
	"context"
	"os"
	"testing"

	"github.com/iridance/wsld/internal/wsl"
)

// 手動驗證：WSLD_REAL_WSL=<distro> go test ./internal/gui -run TestRealWSLFS -v
func TestRealWSLFS(t *testing.T) {
	d := os.Getenv("WSLD_REAL_WSL")
	if d == "" {
		t.Skip("set WSLD_REAL_WSL to a distro name")
	}
	f := wslFS{r: wsl.ExecRunner{}, distro: d}
	ctx := context.Background()
	h, err := f.Home(ctx)
	t.Logf("home=%q err=%v", h, err)
	ents, err := f.List(ctx, h)
	t.Logf("entries=%v err=%v", ents, err)
	if len(ents) == 0 {
		t.Fatal("no entries")
	}
	ents, err = f.List(ctx, "/home/irice/Projects/wsld")
	t.Logf("repo=%v err=%v", importable(ents), err)
	b, err := f.Read(ctx, "/home/irice/Projects/wsld/go.mod", 1<<20)
	t.Logf("go.mod first line=%q err=%v", string(b[:20]), err)
	if _, err := f.Read(ctx, "/nonexistent file", 100); err == nil {
		t.Fatal("expected not-exist error")
	}
}
