package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/iridance/wsld/internal/wsl"
)

// runInherit 以繼承的 stdin/stdout/stderr 執行程式（互動式 docker exec）。
func runInherit(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &wsl.ExitError{Code: ee.ExitCode()}
		}
		return err
	}
	return nil
}
