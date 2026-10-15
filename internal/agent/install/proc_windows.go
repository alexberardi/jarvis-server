package install

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts the child in a console of its own that has no window, outside install's
// process group: closing the terminal install ran in doesn't end it. Not DETACHED_PROCESS: an
// agent with no console at all would open a console window for each console program it runs
// (the Python sidecar, uv), where these inherit the hidden one.
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

// hideWindow keeps helper commands (powershell, schtasks) from flashing a console.
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
}
