//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, so stop ends everything it started.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// signalGroup sends TERM (or KILL) to cmd's whole process group.
func signalGroup(cmd *exec.Cmd, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	syscall.Kill(-cmd.Process.Pid, sig)
}
