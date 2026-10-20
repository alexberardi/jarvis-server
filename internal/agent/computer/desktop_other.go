//go:build !darwin

package computer

// NativeDesktop is nil off macOS: desktop tasks on Linux come with AT-SPI snapshots and the
// vision fallback (plan P3 pieces 33 and 35).
func NativeDesktop(Apps) *Desktop { return nil }
