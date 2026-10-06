//go:build unix && !linux

package engines

import (
	"os/exec"
	"syscall"
)

// setProcAttrs puts the engine in its own process group.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
