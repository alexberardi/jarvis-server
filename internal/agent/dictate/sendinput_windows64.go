//go:build windows && (amd64 || arm64)

package dictate

import "unsafe"

// INPUT is 40 bytes on 64-bit Windows; SendInput rejects any other cbSize. This fails to
// compile if the struct drifts.
var _ = [1]struct{}{}[unsafe.Sizeof(input{})-40]
