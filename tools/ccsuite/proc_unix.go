//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, so stop ends everything it started.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// joinGroup starts cmd in the existing process group pgid, so it ends with that group.
func joinGroup(cmd *exec.Cmd, pgid int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
}

// signalGroup sends TERM (or KILL) to cmd's whole process group.
func signalGroup(cmd *exec.Cmd, kill bool) { killGroup(cmd.Process.Pid, kill) }

// killGroup sends TERM (or KILL) to process group pgid.
func killGroup(pgid int, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	syscall.Kill(-pgid, sig)
}

// groupAlive tells whether process group pgid still has a process.
func groupAlive(pgid int) bool { return syscall.Kill(-pgid, 0) == nil }
