//go:build unix

package update

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid is running. EPERM (another user's process, such as a root
// CLI against the service account's) is alive.
func processAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
