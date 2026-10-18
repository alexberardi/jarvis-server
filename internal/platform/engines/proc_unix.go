//go:build unix

package engines

import (
	"os"
	"syscall"
)

// interrupt asks the engine's process group to exit.
func interrupt(p *os.Process) error { return interruptGroup(p.Pid) }

// interruptGroup sends SIGTERM to the process group led by pid.
func interruptGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGTERM)
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

// contain is a no-op on unix. On Linux Pdeathsig takes the engine down with jarvisd. macOS has
// no equivalent: an engine outlives a SIGKILLed or crashed jarvisd (it is reparented to
// launchd, in its own process group), and the pid file and Sweep at the next start clean up.
func contain(*os.Process) error { return nil }
