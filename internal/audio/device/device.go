// Package device is audio capture and playback for the Jarvis voice pipeline: signed 16-bit
// little-endian interleaved PCM, opened by device name at a requested rate and channel count.
//
// Backends:
//   - Linux: ALSA (libasound) over purego, no cgo. "default" goes through pipewire-alsa or the
//     pulse plugin on a desktop; asound.conf names (dsnoopmic, output) work on a Pi.
//   - macOS: AudioToolbox AudioQueue over purego, no cgo. The device name must be "default" (the
//     system default input/output); AudioQueue converts from the hardware rate.
//   - Windows: winmm waveIn/waveOut, no cgo. The device name must be "default" (WAVE_MAPPER,
//     which converts from the hardware format).
//   - Elsewhere: ErrUnsupported.
//   - "wav:PATH", on every OS: a WAV file as the microphone (paced in real time, then
//     silence), or a WAV file as the speaker. For tests and scripted end-to-end checks.
//
// A Capture or Playback belongs to one goroutine: Read/Write block, and Close must not race
// them (ALSA's snd_pcm_readi can't be interrupted safely from another thread).
package device

import (
	"errors"
	"strings"
)

// ErrUnsupported: this OS has no audio backend yet.
var ErrUnsupported = errors.New("audio: no capture/playback backend on this platform yet")

// Capture is a microphone.
type Capture interface {
	// Read fills buf (interleaved samples, a whole number of frames) completely.
	Read(buf []int16) error
	// Overruns is how many times captured audio was lost (ALSA xruns, a full AudioQueue
	// ring) and recovered from.
	Overruns() int
	Close() error
}

// Playback is a speaker.
type Playback interface {
	// Write queues buf (interleaved, whole frames) for playback, blocking while the device
	// buffer is full.
	Write(buf []int16) error
	// Drain blocks until everything written has been played.
	Drain() error
	Close() error
}

// WAVPrefix selects the WAV-file backend: "wav:/path/to/file.wav".
const WAVPrefix = "wav:"

// OpenCapture opens a microphone: "wav:PATH" (any OS) or a native device name ("" means
// "default").
func OpenCapture(name string, rate, channels int) (Capture, error) {
	if p, ok := strings.CutPrefix(name, WAVPrefix); ok {
		return OpenWAVCapture(p, rate, channels, true)
	}
	if name == "" {
		name = "default"
	}
	return openNativeCapture(name, rate, channels)
}

// OpenPlayback opens a speaker: "wav:PATH" (any OS; the file is written on Close) or a native
// device name ("" means "default").
func OpenPlayback(name string, rate, channels int) (Playback, error) {
	if p, ok := strings.CutPrefix(name, WAVPrefix); ok {
		return CreateWAVPlayback(p, rate, channels)
	}
	if name == "" {
		name = "default"
	}
	return openNativePlayback(name, rate, channels)
}

// Backend names the native backend on this OS ("alsa", "audioqueue", "winmm" or "" when
// unsupported).
func Backend() string { return nativeBackend }

func wholeFrames(n, channels int) bool { return channels > 0 && n%channels == 0 }
