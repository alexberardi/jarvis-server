//go:build !linux && !darwin && !windows

package device

const nativeBackend = ""

func openNativeCapture(string, int, int) (Capture, error)   { return nil, ErrUnsupported }
func openNativePlayback(string, int, int) (Playback, error) { return nil, ErrUnsupported }
