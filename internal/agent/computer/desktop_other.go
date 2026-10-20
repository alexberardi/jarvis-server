//go:build !darwin

package computer

import "context"

// NativeDesktop is nil off macOS: desktop tasks on Linux come with AT-SPI snapshots and the
// vision fallback (plan P3 pieces 33 and 35).
func NativeDesktop(func(ctx context.Context, app string) error) *Desktop { return nil }
