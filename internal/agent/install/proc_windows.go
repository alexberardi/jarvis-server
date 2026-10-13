package install

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts the child without a console of its own and outside install's process group.
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

// hideWindow keeps helper commands (powershell, schtasks) from flashing a console.
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
}
