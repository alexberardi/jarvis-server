//go:build !windows

package install

// nativeRegistry is nil off Windows.
func nativeRegistry() Registry { return nil }
