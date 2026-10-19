//go:build !darwin

package dictate

// NativeMacPoster is nil off macOS.
func NativeMacPoster() MacPoster { return nil }
