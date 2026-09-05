//go:build !windows

// wsld-gui 只支援 Windows；此檔讓非 Windows 平台仍可 `go vet ./...`。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "wsld-gui only runs on Windows (build with GOOS=windows). For UI development use cmd/wsld-gui-testhost.")
	os.Exit(1)
}
