//go:build linux

package device

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ALSA (libasound.so.2) over purego, ported from jarvis-host-agent spikes/pinode/alsa: open a
// PCM by name so asound.conf devices (dsnoopmic, output) and the desktop "default"
// (pipewire-alsa / pulse plugin) all work, set S16_LE interleaved params with ALSA's plug
// layer allowed to convert, and read/write with xrun recovery.

const nativeBackend = "alsa"

const (
	sndStreamPlayback = 0
	sndStreamCapture  = 1
	sndFormatS16LE    = 2
	sndAccessRWInterl = 3

	// Latency asked of snd_pcm_set_params. The Zero 2 W spike (80 ms) saw 140 xruns in 15 min
	// (go-pi-node.md §3.6), so both directions get a generous buffer: capture latency is only
	// queueing (the pipeline reads 80 ms chunks as fast as they come), and nothing here is
	// duplex-latency sensitive.
	captureLatencyUS  = 200_000
	playbackLatencyUS = 200_000
)

var (
	alsaOnce    sync.Once
	alsaLoadErr error

	sndPcmOpen      func(pcm *uintptr, name *byte, stream, mode int32) int32
	sndPcmSetParams func(pcm uintptr, format, access int32, channels, rate uint32, softResample int32, latencyUS uint32) int32
	sndPcmReadi     func(pcm uintptr, buf unsafe.Pointer, frames uint64) int64
	sndPcmWritei    func(pcm uintptr, buf unsafe.Pointer, frames uint64) int64
	sndPcmRecover   func(pcm uintptr, err, silent int32) int32
	sndPcmDrain     func(pcm uintptr) int32
	sndPcmClose     func(pcm uintptr) int32
	sndStrerror     func(err int32) string
)

func loadALSA() error {
	alsaOnce.Do(func() {
		h, err := purego.Dlopen("libasound.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			alsaLoadErr = fmt.Errorf("audio: libasound.so.2 not found (install alsa-lib / libasound2): %w", err)
			return
		}
		for _, f := range []struct {
			ptr  any
			name string
		}{
			{&sndPcmOpen, "snd_pcm_open"}, {&sndPcmSetParams, "snd_pcm_set_params"},
			{&sndPcmReadi, "snd_pcm_readi"}, {&sndPcmWritei, "snd_pcm_writei"},
			{&sndPcmRecover, "snd_pcm_recover"}, {&sndPcmDrain, "snd_pcm_drain"},
			{&sndPcmClose, "snd_pcm_close"}, {&sndStrerror, "snd_strerror"},
		} {
			if _, err := purego.Dlsym(h, f.name); err != nil {
				alsaLoadErr = fmt.Errorf("audio: libasound lacks %s: %w", f.name, err)
				return
			}
			purego.RegisterLibFunc(f.ptr, h, f.name)
		}
	})
	return alsaLoadErr
}

type alsaPCM struct {
	h        uintptr
	name     string
	channels int
	xruns    int
	closed   bool
}

func openALSA(name string, stream int32, rate, channels, latencyUS int) (*alsaPCM, error) {
	if err := loadALSA(); err != nil {
		return nil, err
	}
	if rate <= 0 || channels <= 0 {
		return nil, fmt.Errorf("audio: bad format %d Hz × %d", rate, channels)
	}
	cname := append([]byte(name), 0)
	p := &alsaPCM{name: name, channels: channels}
	if rc := sndPcmOpen(&p.h, &cname[0], stream, 0); rc < 0 {
		return nil, fmt.Errorf("audio: open ALSA %q: %s", name, sndStrerror(rc))
	}
	if rc := sndPcmSetParams(p.h, sndFormatS16LE, sndAccessRWInterl, uint32(channels), uint32(rate), 1, uint32(latencyUS)); rc < 0 {
		sndPcmClose(p.h)
		return nil, fmt.Errorf("audio: ALSA %q at %d Hz × %d: %s", name, rate, channels, sndStrerror(rc))
	}
	return p, nil
}

func (p *alsaPCM) Overruns() int { return p.xruns }

func (p *alsaPCM) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	if rc := sndPcmClose(p.h); rc < 0 {
		return fmt.Errorf("audio: close ALSA %q: %s", p.name, sndStrerror(rc))
	}
	return nil
}

type alsaCapture struct{ *alsaPCM }

func openNativeCapture(name string, rate, channels int) (Capture, error) {
	p, err := openALSA(name, sndStreamCapture, rate, channels, captureLatencyUS)
	if err != nil {
		return nil, err
	}
	return alsaCapture{p}, nil
}

// Read fills buf completely, recovering from overruns (counted in Overruns).
func (c alsaCapture) Read(buf []int16) error {
	if !wholeFrames(len(buf), c.channels) {
		return fmt.Errorf("audio: read of %d samples is not whole %d-channel frames", len(buf), c.channels)
	}
	frames := len(buf) / c.channels
	for got := 0; got < frames; {
		n := sndPcmReadi(c.h, unsafe.Pointer(&buf[got*c.channels]), uint64(frames-got))
		if n < 0 {
			c.xruns++
			if rc := sndPcmRecover(c.h, int32(n), 1); rc < 0 {
				return fmt.Errorf("audio: ALSA read %q: %s", c.name, sndStrerror(int32(n)))
			}
			continue
		}
		got += int(n)
	}
	return nil
}

type alsaPlayback struct{ *alsaPCM }

func openNativePlayback(name string, rate, channels int) (Playback, error) {
	p, err := openALSA(name, sndStreamPlayback, rate, channels, playbackLatencyUS)
	if err != nil {
		return nil, err
	}
	return alsaPlayback{p}, nil
}

// Write plays buf, recovering from underruns (a slow network stream causes them; they're
// harmless gaps).
func (p alsaPlayback) Write(buf []int16) error {
	if !wholeFrames(len(buf), p.channels) {
		return fmt.Errorf("audio: write of %d samples is not whole %d-channel frames", len(buf), p.channels)
	}
	frames := len(buf) / p.channels
	for done := 0; done < frames; {
		n := sndPcmWritei(p.h, unsafe.Pointer(&buf[done*p.channels]), uint64(frames-done))
		if n < 0 {
			p.xruns++
			if rc := sndPcmRecover(p.h, int32(n), 1); rc < 0 {
				return fmt.Errorf("audio: ALSA write %q: %s", p.name, sndStrerror(int32(n)))
			}
			continue
		}
		done += int(n)
	}
	return nil
}

func (p alsaPlayback) Drain() error {
	if rc := sndPcmDrain(p.h); rc < 0 {
		return fmt.Errorf("audio: ALSA drain %q: %s", p.name, sndStrerror(rc))
	}
	return nil
}
