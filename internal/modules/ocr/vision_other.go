//go:build !darwin

package ocr

import "errors"

// newNativeAppleVision: Apple Vision exists only on macOS.
func newNativeAppleVision() (Engine, error) {
	return nil, errors.New("Apple Vision needs macOS")
}
