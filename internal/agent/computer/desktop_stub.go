//go:build !darwin && !linux

package computer

// NativeDesktop is nil where desktop tasks aren't supported yet (Windows).
func NativeDesktop(Apps) *Desktop { return nil }
