package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
}

// terminate stops a detached process (the -pi lane, which runs on Linux only).
func terminate(c *exec.Cmd) { _ = c.Process.Kill() }
