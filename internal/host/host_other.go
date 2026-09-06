//go:build !windows

package host

import (
	"fmt"
	"net"
)

func openFolder(string, string) error {
	return fmt.Errorf("open folder is only supported on Windows")
}

func openTerminal(string, string) error {
	return fmt.Errorf("open terminal is only supported on Windows")
}

func startShell(string, string, int, int) (net.Conn, error) {
	return nil, fmt.Errorf("start shell is only supported on Windows")
}
