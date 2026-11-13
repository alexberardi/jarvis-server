// Package opusnative decodes and encodes Opus by loading the system libopus at run time with
// purego, so jarvisd keeps building with CGO_ENABLED=0 (the same approach as the sherpa-onnx
// loader). Discord voice is 48 kHz stereo in 20 ms frames.
package opusnative

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	SampleRate = 48000
	Channels   = 2
	FrameSize  = 960 // samples per channel in 20 ms
	maxPacket  = 4000

	appVoIP = 2048 // OPUS_APPLICATION_VOIP
)

var (
	loadOnce sync.Once
	loadErr  error

	decoderCreate  func(fs int32, channels int32, err *int32) uintptr
	decoderDestroy func(d uintptr)
	decode         func(d uintptr, data unsafe.Pointer, n int32, pcm unsafe.Pointer, frameSize int32, fec int32) int32
	encoderCreate  func(fs int32, channels int32, app int32, err *int32) uintptr
	encoderDestroy func(e uintptr)
	encode         func(e uintptr, pcm unsafe.Pointer, frameSize int32, out unsafe.Pointer, maxBytes int32) int32
)

func libName() string {
	switch runtime.GOOS {
	case "windows":
		return "opus.dll"
	case "darwin":
		return "libopus.0.dylib"
	}
	return "libopus.so.0"
}

// Load opens libopus once. It returns an error naming the library when it is not installed.
func Load() error {
	loadOnce.Do(func() {
		lib, err := openLib(libName())
		if err != nil {
			loadErr = fmt.Errorf("opusnative: cannot load %s: %w", libName(), err)
			return
		}
		purego.RegisterLibFunc(&decoderCreate, lib, "opus_decoder_create")
		purego.RegisterLibFunc(&decoderDestroy, lib, "opus_decoder_destroy")
		purego.RegisterLibFunc(&decode, lib, "opus_decode")
		purego.RegisterLibFunc(&encoderCreate, lib, "opus_encoder_create")
		purego.RegisterLibFunc(&encoderDestroy, lib, "opus_encoder_destroy")
		purego.RegisterLibFunc(&encode, lib, "opus_encode")
	})
	return loadErr
}

// Decoder turns Opus packets into interleaved 16-bit stereo PCM. One per speaker.
type Decoder struct{ d uintptr }

func NewDecoder() (*Decoder, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	var e int32
	d := decoderCreate(SampleRate, Channels, &e)
	if e != 0 || d == 0 {
		return nil, fmt.Errorf("opusnative: decoder create failed (%d)", e)
	}
	return &Decoder{d: d}, nil
}

// Decode returns FrameSize*Channels samples for one packet. A nil packet conceals a lost frame.
func (d *Decoder) Decode(packet []byte) ([]int16, error) {
	pcm := make([]int16, FrameSize*Channels*6) // up to 120 ms
	var p unsafe.Pointer
	if len(packet) > 0 {
		p = unsafe.Pointer(&packet[0])
	}
	n := decode(d.d, p, int32(len(packet)), unsafe.Pointer(&pcm[0]), int32(FrameSize*6), 0)
	if n < 0 {
		return nil, fmt.Errorf("opusnative: decode error %d", n)
	}
	return pcm[:int(n)*Channels], nil
}

func (d *Decoder) Close() { decoderDestroy(d.d); d.d = 0 }

// Encoder turns one 20 ms stereo PCM frame into an Opus packet.
type Encoder struct{ e uintptr }

func NewEncoder() (*Encoder, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	var e int32
	enc := encoderCreate(SampleRate, Channels, appVoIP, &e)
	if e != 0 || enc == 0 {
		return nil, fmt.Errorf("opusnative: encoder create failed (%d)", e)
	}
	return &Encoder{e: enc}, nil
}

func (e *Encoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != FrameSize*Channels {
		return nil, errors.New("opusnative: frame must be 960 stereo samples")
	}
	out := make([]byte, maxPacket)
	n := encode(e.e, unsafe.Pointer(&pcm[0]), FrameSize, unsafe.Pointer(&out[0]), maxPacket)
	if n < 0 {
		return nil, fmt.Errorf("opusnative: encode error %d", n)
	}
	return out[:n], nil
}

func (e *Encoder) Close() { encoderDestroy(e.e); e.e = 0 }
