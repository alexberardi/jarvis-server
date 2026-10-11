//go:build !windows

package browser

// NativeRegistry is nil off Windows: browsers there find hosts by manifest directory.
func NativeRegistry() Registry { return nil }
