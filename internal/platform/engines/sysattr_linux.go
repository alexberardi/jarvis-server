package engines

import (
	"os/exec"
	"syscall"
)

// setProcAttrs puts the engine in its own process group, and has the kernel kill it if
// jarvisd dies, so an orphaned llama-server can't keep the GPU.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
