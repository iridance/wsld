//go:build !windows

package i18n

import "os"

// Detect 非 Windows：只看環境變數。
func Detect() Lang { return DetectFrom(0, os.Getenv) }
