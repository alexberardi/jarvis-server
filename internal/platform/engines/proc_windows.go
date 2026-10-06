package engines

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// setProcAttrs gives the engine its own process group and no console window.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

// interrupt has no graceful equivalent for a console engine on Windows; the caller kills.
func interrupt(*os.Process) error { return errors.ErrUnsupported }

// killTree kills the engine and its descendants, best effort. taskkill walks the tree from
// the live parent, so this must run before the engine exits.
func killTree(p *os.Process) {
	tk := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	tk.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = tk.Run()
	_ = p.Kill()
}

// killLeftovers is a no-op: the PID may already be reused, and there is no process group
// to address once the parent has gone.
func killLeftovers(int) {}
