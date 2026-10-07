// Package sysinfo reads the few host facts the admin's system info bar shows that the Go
// runtime doesn't: total RAM, the OS release, and free disk space. Every function is
// best-effort and returns the zero value when the OS won't say. No cgo (x/sys only).
package sysinfo

// TotalMemory is the host's physical RAM in bytes, or 0.
func TotalMemory() uint64 { return totalMemory() }

// Release is the OS release (the kernel release on Linux and macOS, "major.minor.build" on
// Windows), or "".
func Release() string { return release() }

// DiskFree is the space available to this user on the filesystem holding path, in bytes, or 0.
func DiskFree(path string) uint64 { return diskFree(path) }
