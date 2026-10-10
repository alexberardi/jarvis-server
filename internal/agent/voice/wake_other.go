//go:build !linux && !darwin

package voice

import "errors"

// Supported reports whether this build can run the voice pipeline. Windows comes later
// (WASAPI capture, go-pi-node.md §7 phase 3); until then the agent runs without voice.
const Supported = false

// ErrUnsupported is LoadDetector's answer where voice isn't built in.
var ErrUnsupported = errors.New("voice: not supported on this platform yet")

// LoadDetector is unsupported here, and keeps onnxruntime out of the binary.
func LoadDetector(string, string) (Detector, func(), error) { return nil, nil, ErrUnsupported }
