//go:build unix

package engines

import (
	"os"
	"syscall"
)

// interrupt asks the engine's process group to exit.
func interrupt(p *os.Process) error {
	return syscall.Kill(-p.Pid, syscall.SIGTERM)
}

// killTree kills the engine's process group.
func killTree(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}

// killLeftovers kills whatever is left in the group of an exited engine, so a stray child
// can't keep holding GPU memory or the port.
func killLeftovers(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
