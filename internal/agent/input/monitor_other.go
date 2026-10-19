//go:build !darwin

package input

// NativeMonitor is nil where there is no input monitor yet (Linux, Windows).
func NativeMonitor() Monitor { return nil }
