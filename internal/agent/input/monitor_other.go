//go:build !darwin && !linux

package input

// NativeMonitor is nil where there is no input monitor yet (Windows).
func NativeMonitor() Monitor { return nil }
