//go:build darwin

package device

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// AudioToolbox AudioQueue over purego (no cgo). Both directions use the system default device
// and let AudioQueue convert from the hardware format, so the pipeline always gets the
// 48 kHz mono S16 it asks for.
//
// Callbacks run on AudioQueue's own thread (no run loop is passed). purego callbacks are a
// process-wide, never-freed resource, so there is exactly one input and one output callback;
// they find their queue through a registry keyed by the user-data id.

const nativeBackend = "audioqueue"

const (
	atPath = "/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox"

	kAudioFormatLinearPCM = 0x6C70636D // 'lpcm'
	// kLinearPCMFormatFlagIsSignedInteger | kLinearPCMFormatFlagIsPacked
	pcmFlags = 0x4 | 0x8

	aqBufferMS  = 40 // per AudioQueue buffer
	aqBuffers   = 4  // playback: buffers in flight (160 ms queued)
	aqInBuffers = 8  // capture: buffers handed to the queue
	ringSeconds = 2  // capture: how much audio may wait for Read before it's an overrun
)

// asbd is AudioStreamBasicDescription.
type asbd struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

// aqBuffer is AudioQueueBuffer (64-bit layout; Go inserts the same padding C does).
type aqBuffer struct {
	Capacity           uint32
	Data               unsafe.Pointer
	ByteSize           uint32
	UserData           uintptr
	PacketDescCapacity uint32
	PacketDescs        uintptr
	PacketDescCount    uint32
}

var (
	aqOnce    sync.Once
	aqLoadErr error

	aqNewInput      func(format *asbd, cb, user, runLoop, mode uintptr, flags uint32, out *uintptr) int32
	aqNewOutput     func(format *asbd, cb, user, runLoop, mode uintptr, flags uint32, out *uintptr) int32
	aqAllocBuffer   func(aq uintptr, size uint32, out *uintptr) int32
	aqEnqueueBuffer func(aq, buf uintptr, nDescs uint32, descs uintptr) int32
	aqStart         func(aq, startTime uintptr) int32
	aqStop          func(aq uintptr, immediate bool) int32
	aqDispose       func(aq uintptr, immediate bool) int32

	inputCB, outputCB uintptr

	regMu   sync.Mutex
	regNext uintptr
	inputs  = map[uintptr]*aqCapture{}
	outputs = map[uintptr]*aqPlayback{}
)

func loadAQ() error {
	aqOnce.Do(func() {
		h, err := purego.Dlopen(atPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			aqLoadErr = fmt.Errorf("audio: AudioToolbox: %w", err)
			return
		}
		purego.RegisterLibFunc(&aqNewInput, h, "AudioQueueNewInput")
		purego.RegisterLibFunc(&aqNewOutput, h, "AudioQueueNewOutput")
		purego.RegisterLibFunc(&aqAllocBuffer, h, "AudioQueueAllocateBuffer")
		purego.RegisterLibFunc(&aqEnqueueBuffer, h, "AudioQueueEnqueueBuffer")
		purego.RegisterLibFunc(&aqStart, h, "AudioQueueStart")
		purego.RegisterLibFunc(&aqStop, h, "AudioQueueStop")
		purego.RegisterLibFunc(&aqDispose, h, "AudioQueueDispose")
		inputCB = purego.NewCallback(onInput)
		outputCB = purego.NewCallback(onOutput)
	})
	return aqLoadErr
}

func format(rate, channels int) asbd {
	return asbd{SampleRate: float64(rate), FormatID: kAudioFormatLinearPCM, FormatFlags: pcmFlags,
		BytesPerPacket: uint32(2 * channels), FramesPerPacket: 1, BytesPerFrame: uint32(2 * channels),
		ChannelsPerFrame: uint32(channels), BitsPerChannel: 16}
}

func osErr(op string, st int32) error {
	if st == 0 {
		return nil
	}
	// OSStatus values are often four-char codes.
	u := uint32(st)
	cc := []byte{byte(u >> 24), byte(u >> 16), byte(u >> 8), byte(u)}
	printable := true
	for _, c := range cc {
		if c < 0x20 || c > 0x7e {
			printable = false
		}
	}
	if printable {
		return fmt.Errorf("audio: %s: OSStatus %d ('%s')", op, st, cc)
	}
	return fmt.Errorf("audio: %s: OSStatus %d", op, st)
}

// cBuffer views an AudioQueueBufferRef (C memory). unsafe.Add from nil keeps go vet's
// uintptr-to-Pointer check quiet, as macax does: the memory isn't Go's, so the GC rule it
// guards doesn't apply.
func cBuffer(ref uintptr) *aqBuffer { return (*aqBuffer)(unsafe.Add(unsafe.Pointer(nil), ref)) }

func bufferBytes(rate, channels int) uint32 { return uint32(rate * aqBufferMS / 1000 * 2 * channels) }

// --- capture ---

type aqCapture struct {
	id       uintptr
	aq       uintptr
	channels int

	mu       sync.Mutex
	cond     *sync.Cond
	ring     []int16 // pending samples, oldest first
	limit    int
	overruns int
	closed   bool
}

func openNativeCapture(name string, rate, channels int) (Capture, error) {
	if name != "default" {
		return nil, fmt.Errorf("audio: macOS capture supports only the default input device, not %q", name)
	}
	if err := loadAQ(); err != nil {
		return nil, err
	}
	c := &aqCapture{channels: channels, limit: rate * channels * ringSeconds}
	c.cond = sync.NewCond(&c.mu)
	regMu.Lock()
	regNext++
	c.id = regNext
	inputs[c.id] = c
	regMu.Unlock()
	f := format(rate, channels)
	if err := osErr("AudioQueueNewInput", aqNewInput(&f, inputCB, c.id, 0, 0, 0, &c.aq)); err != nil {
		c.unregister()
		return nil, err
	}
	for range aqInBuffers {
		var b uintptr
		if err := osErr("AudioQueueAllocateBuffer", aqAllocBuffer(c.aq, bufferBytes(rate, channels), &b)); err != nil {
			c.dispose()
			return nil, err
		}
		if err := osErr("AudioQueueEnqueueBuffer", aqEnqueueBuffer(c.aq, b, 0, 0)); err != nil {
			c.dispose()
			return nil, err
		}
	}
	if err := osErr("AudioQueueStart (input)", aqStart(c.aq, 0)); err != nil {
		c.dispose()
		return nil, err
	}
	return c, nil
}

// onInput is AudioQueueInputCallback: copy the captured bytes into the ring and hand the
// buffer straight back to the queue.
func onInput(user, aq, buf, _, _, _ uintptr) {
	regMu.Lock()
	c := inputs[user]
	regMu.Unlock()
	if c == nil {
		return
	}
	b := cBuffer(buf)
	n := int(b.ByteSize) / 2
	c.mu.Lock()
	if !c.closed && n > 0 {
		src := unsafe.Slice((*int16)(b.Data), n)
		c.ring = append(c.ring, src...)
		if over := len(c.ring) - c.limit; over > 0 {
			// The reader fell more than ringSeconds behind: drop the oldest audio, as ALSA
			// does on an overrun, rather than grow without bound.
			over += over % c.channels
			c.ring = append(c.ring[:0], c.ring[over:]...)
			c.overruns++
		}
		c.cond.Broadcast()
	}
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		aqEnqueueBuffer(aq, buf, 0, 0)
	}
}

const readTimeout = 2 * time.Second

func (c *aqCapture) Read(buf []int16) error {
	if !wholeFrames(len(buf), c.channels) {
		return fmt.Errorf("audio: read of %d samples is not whole %d-channel frames", len(buf), c.channels)
	}
	deadline := time.Now().Add(readTimeout)
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.ring) < len(buf) && !c.closed {
		if time.Now().After(deadline) {
			return errors.New("audio: no input from the default microphone for 2 s (is the input device gone?)")
		}
		// sync.Cond has no timed wait; poke ourselves so the deadline is rechecked.
		t := time.AfterFunc(100*time.Millisecond, c.cond.Broadcast)
		c.cond.Wait()
		t.Stop()
	}
	if c.closed {
		return errors.New("audio: capture closed")
	}
	copy(buf, c.ring)
	c.ring = append(c.ring[:0], c.ring[len(buf):]...)
	return nil
}

func (c *aqCapture) Overruns() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overruns
}

func (c *aqCapture) unregister() {
	regMu.Lock()
	delete(inputs, c.id)
	regMu.Unlock()
}

func (c *aqCapture) dispose() {
	c.mu.Lock()
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
	if c.aq != 0 {
		aqStop(c.aq, true)
		aqDispose(c.aq, true) // frees the buffers too
		c.aq = 0
	}
	c.unregister()
}

func (c *aqCapture) Close() error {
	c.dispose()
	return nil
}

// --- playback ---

type aqPlayback struct {
	id       uintptr
	aq       uintptr
	channels int
	size     uint32 // bytes per buffer

	free    chan uintptr // buffers not queued
	started bool
	closed  bool
}

func openNativePlayback(name string, rate, channels int) (Playback, error) {
	if name != "default" {
		return nil, fmt.Errorf("audio: macOS playback supports only the default output device, not %q", name)
	}
	if err := loadAQ(); err != nil {
		return nil, err
	}
	p := &aqPlayback{channels: channels, size: bufferBytes(rate, channels), free: make(chan uintptr, aqBuffers)}
	regMu.Lock()
	regNext++
	p.id = regNext
	outputs[p.id] = p
	regMu.Unlock()
	f := format(rate, channels)
	if err := osErr("AudioQueueNewOutput", aqNewOutput(&f, outputCB, p.id, 0, 0, 0, &p.aq)); err != nil {
		p.unregister()
		return nil, err
	}
	for range aqBuffers {
		var b uintptr
		if err := osErr("AudioQueueAllocateBuffer", aqAllocBuffer(p.aq, p.size, &b)); err != nil {
			p.Close()
			return nil, err
		}
		p.free <- b
	}
	return p, nil
}

// onOutput is AudioQueueOutputCallback: the queue finished playing buf.
func onOutput(user, _, buf uintptr) {
	regMu.Lock()
	p := outputs[user]
	regMu.Unlock()
	if p != nil {
		p.free <- buf // capacity aqBuffers: never blocks
	}
}

func (p *aqPlayback) Write(pcm []int16) error {
	if !wholeFrames(len(pcm), p.channels) {
		return fmt.Errorf("audio: write of %d samples is not whole %d-channel frames", len(pcm), p.channels)
	}
	per := int(p.size) / 2
	for len(pcm) > 0 {
		var b uintptr
		select {
		case b = <-p.free:
		case <-time.After(5 * time.Second):
			return errors.New("audio: the output queue stopped returning buffers")
		}
		n := min(per, len(pcm))
		ab := cBuffer(b)
		copy(unsafe.Slice((*int16)(ab.Data), n), pcm[:n])
		ab.ByteSize = uint32(2 * n)
		if err := osErr("AudioQueueEnqueueBuffer", aqEnqueueBuffer(p.aq, b, 0, 0)); err != nil {
			p.free <- b
			return err
		}
		pcm = pcm[n:]
		if !p.started && (len(p.free) == 0 || len(pcm) == 0) {
			if err := osErr("AudioQueueStart (output)", aqStart(p.aq, 0)); err != nil {
				return err
			}
			p.started = true
		}
	}
	return nil
}

// Drain waits until every buffer has come back from the queue, i.e. all of it was played.
func (p *aqPlayback) Drain() error {
	if !p.started {
		return nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(p.free) < aqBuffers {
		if time.Now().After(deadline) {
			return errors.New("audio: output did not drain within 5 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func (p *aqPlayback) unregister() {
	regMu.Lock()
	delete(outputs, p.id)
	regMu.Unlock()
}

func (p *aqPlayback) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	if p.aq != 0 {
		aqStop(p.aq, true)
		aqDispose(p.aq, true)
		p.aq = 0
	}
	p.unregister()
	return nil
}
