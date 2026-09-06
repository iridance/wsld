//go:build !windows

package host

import "fmt"

func openFolder(string) error {
	return fmt.Errorf("open folder is only supported on Windows")
}

func openTerminal(string) error {
	return fmt.Errorf("open terminal is only supported on Windows")
}
