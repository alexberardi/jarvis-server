//go:build windows

package device

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winmm waveIn/waveOut (no cgo, no COM). Both directions open WAVE_MAPPER, the default device,
// which converts from the hardware format, so the pipeline gets the rate and channels it asks
// for, as with AudioQueue on macOS. Completion is signalled through an event (CALLBACK_EVENT),
// so no Windows thread ever calls into Go: Read and Write wait on the event and check each
// header's WHDR_DONE flag themselves.

const nativeBackend = "winmm"

const (
	waveMapper    = 0xFFFFFFFF
	callbackEvent = 0x00050000
	waveFormatPCM = 1

	whdrDone = 0x1

	mmBufferMS  = 40 // per buffer
	mmOutBufs   = 4  // playback: buffers in flight (160 ms queued)
	mmInBufs    = 25 // capture: 1 s handed to the driver before Read must catch up
	mmErrNoDev  = 2  // MMSYSERR_BADDEVICEID
	mmErrInUse  = 4  // MMSYSERR_ALLOCATED
	mmErrFormat = 32 // WAVERR_BADFORMAT
)

// waveFormatEx is WAVEFORMATEX. Go's layout matches C's packed 18 bytes (plus trailing padding,
// which winmm doesn't read for PCM).
type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

// waveHdr is WAVEHDR (64-bit layout). Data points at a Go slice the owner keeps alive; Go's
// heap doesn't move, so winmm may write to it while it's queued.
type waveHdr struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

var (
	winmm = windows.NewLazySystemDLL("winmm.dll")

	procWaveInOpen            = winmm.NewProc("waveInOpen")
	procWaveInPrepareHeader   = winmm.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHeader = winmm.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer       = winmm.NewProc("waveInAddBuffer")
	procWaveInStart           = winmm.NewProc("waveInStart")
	procWaveInReset           = winmm.NewProc("waveInReset")
	procWaveInClose           = winmm.NewProc("waveInClose")

	procWaveOutOpen            = winmm.NewProc("waveOutOpen")
	procWaveOutPrepareHeader   = winmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprepareHeader = winmm.NewProc("waveOutUnprepareHeader")
	procWaveOutWrite           = winmm.NewProc("waveOutWrite")
	procWaveOutReset           = winmm.NewProc("waveOutReset")
	procWaveOutClose           = winmm.NewProc("waveOutClose")
)

// go:uintptrescapes keeps the pointers converted in callers' args alive for the call, as it
// does for LazyProc.Call itself.
//
//go:uintptrescapes
func mmCall(p *windows.LazyProc, args ...uintptr) error {
	if err := p.Find(); err != nil {
		return fmt.Errorf("audio: winmm: %w", err)
	}
	r, _, _ := p.Call(args...)
	return mmErr(p.Name, r)
}

func mmErr(op string, r uintptr) error {
	switch r {
	case 0:
		return nil
	case mmErrNoDev:
		return fmt.Errorf("audio: %s: no such audio device (is a microphone/speaker connected and enabled?)", op)
	case mmErrInUse:
		return fmt.Errorf("audio: %s: the audio device is in use", op)
	case mmErrFormat:
		return fmt.Errorf("audio: %s: the device can't do this format", op)
	}
	return fmt.Errorf("audio: %s: MMRESULT %d", op, r)
}

func pcmFormat(rate, channels int) waveFormatEx {
	return waveFormatEx{FormatTag: waveFormatPCM, Channels: uint16(channels), SamplesPerSec: uint32(rate),
		AvgBytesPerSec: uint32(rate * 2 * channels), BlockAlign: uint16(2 * channels), BitsPerSample: 16}
}

// mmBuffers allocates n headers of samples each.
func mmBuffers(n, samples int) ([]*waveHdr, [][]int16) {
	hdrs := make([]*waveHdr, n)
	data := make([][]int16, n)
	for i := range n {
		data[i] = make([]int16, samples)
		hdrs[i] = &waveHdr{Data: uintptr(unsafe.Pointer(&data[i][0])), BufferLength: uint32(2 * samples)}
	}
	return hdrs, data
}

func done(h *waveHdr) bool { return atomic.LoadUint32(&h.Flags)&whdrDone != 0 }

// waitDone waits on ev until h is done, or timeout.
func waitDone(ev windows.Handle, h *waveHdr, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for !done(h) {
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		// Auto-reset event: a signal for an earlier header may have been consumed already, so
		// wake at least every 50 ms to recheck the flag.
		_, _ = windows.WaitForSingleObject(ev, uint32(min(left, 50*time.Millisecond)/time.Millisecond)+1)
	}
	return true
}

func mmBufferSamples(rate, channels int) int { return rate * mmBufferMS / 1000 * channels }

// --- capture ---

type mmCapture struct {
	h        uintptr
	ev       windows.Handle
	channels int
	hdrs     []*waveHdr
	data     [][]int16
	next     int     // the header Read waits on (buffers complete in order)
	pending  []int16 // completed audio not yet returned
	overruns int
}

func openNativeCapture(name string, rate, channels int) (Capture, error) {
	if name != "default" {
		return nil, fmt.Errorf("audio: Windows capture supports only the default input device, not %q", name)
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("audio: CreateEvent: %w", err)
	}
	c := &mmCapture{ev: ev, channels: channels}
	f := pcmFormat(rate, channels)
	if err := mmCall(procWaveInOpen, uintptr(unsafe.Pointer(&c.h)), waveMapper, uintptr(unsafe.Pointer(&f)),
		uintptr(ev), 0, callbackEvent); err != nil {
		windows.CloseHandle(ev)
		return nil, err
	}
	c.hdrs, c.data = mmBuffers(mmInBufs, mmBufferSamples(rate, channels))
	for i, hd := range c.hdrs {
		if err := mmCall(procWaveInPrepareHeader, c.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd)); err != nil {
			c.hdrs = c.hdrs[:i] // only the prepared ones get unprepared
			c.Close()
			return nil, err
		}
		if err := mmCall(procWaveInAddBuffer, c.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd)); err != nil {
			c.hdrs = c.hdrs[:i+1]
			c.Close()
			return nil, err
		}
	}
	if err := mmCall(procWaveInStart, c.h); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

const readTimeout = 2 * time.Second

func (c *mmCapture) Read(buf []int16) error {
	if !wholeFrames(len(buf), c.channels) {
		return fmt.Errorf("audio: read of %d samples is not whole %d-channel frames", len(buf), c.channels)
	}
	if c.h == 0 {
		return errors.New("audio: capture closed")
	}
	for len(c.pending) < len(buf) {
		hd := c.hdrs[c.next]
		if !waitDone(c.ev, hd, readTimeout) {
			return errors.New("audio: no input from the default microphone for 2 s (is the input device gone?)")
		}
		// Every buffer done means the driver had none left to fill: audio was dropped.
		if c.allDone() {
			c.overruns++
		}
		n := int(atomic.LoadUint32(&hd.BytesRecorded)) / 2
		c.pending = append(c.pending, c.data[c.next][:n]...)
		hd.BytesRecorded = 0
		atomic.StoreUint32(&hd.Flags, atomic.LoadUint32(&hd.Flags)&^whdrDone)
		if err := mmCall(procWaveInAddBuffer, c.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd)); err != nil {
			return err
		}
		c.next = (c.next + 1) % len(c.hdrs)
	}
	copy(buf, c.pending)
	c.pending = append(c.pending[:0], c.pending[len(buf):]...)
	return nil
}

func (c *mmCapture) allDone() bool {
	for _, hd := range c.hdrs {
		if !done(hd) {
			return false
		}
	}
	return true
}

func (c *mmCapture) Overruns() int { return c.overruns }

func (c *mmCapture) Close() error {
	if c.h != 0 {
		_ = mmCall(procWaveInReset, c.h) // marks every queued buffer done
		for _, hd := range c.hdrs {
			_ = mmCall(procWaveInUnprepareHeader, c.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd))
		}
		_ = mmCall(procWaveInClose, c.h)
		c.h = 0
	}
	if c.ev != 0 {
		windows.CloseHandle(c.ev)
		c.ev = 0
	}
	return nil
}

// --- playback ---

type mmPlayback struct {
	h        uintptr
	ev       windows.Handle
	channels int
	hdrs     []*waveHdr
	data     [][]int16
	next     int // the next header to fill (they come back in order)
}

func openNativePlayback(name string, rate, channels int) (Playback, error) {
	if name != "default" {
		return nil, fmt.Errorf("audio: Windows playback supports only the default output device, not %q", name)
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("audio: CreateEvent: %w", err)
	}
	p := &mmPlayback{ev: ev, channels: channels}
	f := pcmFormat(rate, channels)
	if err := mmCall(procWaveOutOpen, uintptr(unsafe.Pointer(&p.h)), waveMapper, uintptr(unsafe.Pointer(&f)),
		uintptr(ev), 0, callbackEvent); err != nil {
		windows.CloseHandle(ev)
		return nil, err
	}
	p.hdrs, p.data = mmBuffers(mmOutBufs, mmBufferSamples(rate, channels))
	for i, hd := range p.hdrs {
		if err := mmCall(procWaveOutPrepareHeader, p.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd)); err != nil {
			p.hdrs = p.hdrs[:i]
			p.Close()
			return nil, err
		}
		hd.Flags |= whdrDone // free until written
	}
	return p, nil
}

func (p *mmPlayback) Write(pcm []int16) error {
	if !wholeFrames(len(pcm), p.channels) {
		return fmt.Errorf("audio: write of %d samples is not whole %d-channel frames", len(pcm), p.channels)
	}
	if p.h == 0 {
		return errors.New("audio: playback closed")
	}
	for len(pcm) > 0 {
		hd := p.hdrs[p.next]
		if !waitDone(p.ev, hd, 5*time.Second) {
			return errors.New("audio: the output device stopped returning buffers")
		}
		n := copy(p.data[p.next], pcm)
		hd.BufferLength = uint32(2 * n)
		atomic.StoreUint32(&hd.Flags, atomic.LoadUint32(&hd.Flags)&^whdrDone)
		if err := mmCall(procWaveOutWrite, p.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd)); err != nil {
			atomic.StoreUint32(&hd.Flags, atomic.LoadUint32(&hd.Flags)|whdrDone)
			return err
		}
		pcm = pcm[n:]
		p.next = (p.next + 1) % len(p.hdrs)
	}
	return nil
}

// Drain waits until every written buffer has come back, i.e. all of it was played.
func (p *mmPlayback) Drain() error {
	for _, hd := range p.hdrs {
		if !waitDone(p.ev, hd, 5*time.Second) {
			return errors.New("audio: output did not drain within 5 s")
		}
	}
	return nil
}

func (p *mmPlayback) Close() error {
	if p.h != 0 {
		_ = mmCall(procWaveOutReset, p.h)
		for _, hd := range p.hdrs {
			_ = mmCall(procWaveOutUnprepareHeader, p.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd))
		}
		_ = mmCall(procWaveOutClose, p.h)
		p.h = 0
	}
	if p.ev != 0 {
		windows.CloseHandle(p.ev)
		p.ev = 0
	}
	return nil
}
