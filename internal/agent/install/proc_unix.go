//go:build !windows

package install

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session, so it outlives install and its terminal.
func detach(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func hideWindow(*exec.Cmd) {}
