//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func detach(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// terminate stops a detached process and its children (its process group), as systemd stops a
// unit's cgroup: SIGTERM.
func terminate(c *exec.Cmd) { _ = syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
