//go:build !linux && !darwin && !windows

package voice

import "errors"

// Supported reports whether this build can run the voice pipeline: not on this OS, which has
// no audio backend (internal/audio/device) or embedded onnxruntime.
const Supported = false

// ErrUnsupported is LoadDetector's answer where voice isn't built in.
var ErrUnsupported = errors.New("voice: not supported on this platform yet")

// LoadDetector is unsupported here, and keeps onnxruntime out of the binary.
func LoadDetector(string, string) (Detector, func(), error) { return nil, nil, ErrUnsupported }
