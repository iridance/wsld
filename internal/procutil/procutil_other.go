//go:build !windows

package procutil

import "os/exec"

func hideConsole(*exec.Cmd) {}
