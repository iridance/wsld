package gui

import (
	"context"
	"fmt"

	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lifecycle"
)

// ConfigMissingLifecycle 在 config.toml 不存在時取代真正的 lifecycle.Manager：
// GUI 仍可開啟，但 Status 不就緒、Start 回 ErrNotReady，並提示執行 wsld init（SPEC §5.4 退出碼 3 的 GUI 對應）。
type ConfigMissingLifecycle struct {
	Path string
}

// Status 實作 Lifecycle。
func (g ConfigMissingLifecycle) Status(context.Context) lifecycle.Status {
	return lifecycle.Status{APIError: i18n.T("cli.config_missing", g.Path)}
}

// Start 實作 Lifecycle。
func (g ConfigMissingLifecycle) Start(ctx context.Context) (lifecycle.Status, error) {
	return g.Status(ctx), fmt.Errorf("%w: %s", lifecycle.ErrNotReady, i18n.T("cli.config_missing", g.Path))
}
