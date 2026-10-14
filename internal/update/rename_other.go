//go:build !windows

package update

// transientLock: a rename on unix doesn't fail because another process has the file open.
func transientLock(error) bool { return false }
