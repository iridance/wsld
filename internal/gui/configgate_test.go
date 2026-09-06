package gui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lifecycle"
)

func TestConfigMissingLifecycle(t *testing.T) {
	i18n.Set(i18n.EN)
	g := ConfigMissingLifecycle{Path: `C:\Users\me\AppData\Roaming\wsld\config.toml`}

	st := g.Status(context.Background())
	if st.Ready() {
		t.Fatal("status must not be ready without config")
	}
	if !strings.Contains(st.APIError, "wsld init") || !strings.Contains(st.APIError, g.Path) {
		t.Fatalf("APIError should tell user to run wsld init and show path, got %q", st.APIError)
	}

	_, err := g.Start(context.Background())
	if !errors.Is(err, lifecycle.ErrNotReady) {
		t.Fatalf("Start should return ErrNotReady, got %v", err)
	}
	if !strings.Contains(err.Error(), "wsld init") {
		t.Fatalf("Start error should mention wsld init, got %q", err)
	}
}
