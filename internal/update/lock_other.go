//go:build !unix && !windows

package update

// processAlive can't tell here, so a lock with a PID is never taken over.
func processAlive(int) bool { return true }
