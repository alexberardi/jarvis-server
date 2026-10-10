package device

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// WAVCapture is a WAV file posing as a microphone: its samples (downmixed to mono, linearly
// resampled to the requested rate if needed, copied to every channel), then silence forever.
type WAVCapture struct {
	samples  []int16 // mono, at rate
	pos      int
	channels int
	rate     int
	paced    bool
	start    time.Time
	read     int // frames handed out so far
}

// OpenWAVCapture loads path. paced makes Read block in real time like a device does; unpaced
// returns immediately (tests).
func OpenWAVCapture(path string, rate, channels int, paced bool) (*WAVCapture, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, fileRate, err := audio.ReadWAV(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return NewWAVCapture(s, fileRate, rate, channels, paced), nil
}

// NewWAVCapture serves mono [-1, 1] samples at srcRate as a rate/channels microphone.
func NewWAVCapture(s []float32, srcRate, rate, channels int, paced bool) *WAVCapture {
	if srcRate != rate && srcRate > 0 {
		s = resampleLinear(s, srcRate, rate)
	}
	pcm := make([]int16, len(s))
	for i, v := range s {
		pcm[i] = int16(max(-32768, min(32767, v*32768)))
	}
	return &WAVCapture{samples: pcm, channels: max(1, channels), rate: rate, paced: paced}
}

func resampleLinear(s []float32, from, to int) []float32 {
	n := int(int64(len(s)) * int64(to) / int64(from))
	out := make([]float32, n)
	for i := range out {
		x := float64(i) * float64(from) / float64(to)
		j := int(x)
		frac := float32(x - float64(j))
		a := s[min(j, len(s)-1)]
		b := s[min(j+1, len(s)-1)]
		out[i] = a + (b-a)*frac
	}
	return out
}

func (c *WAVCapture) Read(buf []int16) error {
	if !wholeFrames(len(buf), c.channels) {
		return fmt.Errorf("audio: read of %d samples is not whole %d-channel frames", len(buf), c.channels)
	}
	frames := len(buf) / c.channels
	for i := 0; i < frames; i++ {
		var v int16
		if c.pos < len(c.samples) {
			v = c.samples[c.pos]
			c.pos++
		}
		for ch := 0; ch < c.channels; ch++ {
			buf[i*c.channels+ch] = v
		}
	}
	if c.paced {
		if c.start.IsZero() {
			c.start = time.Now()
		}
		c.read += frames
		due := c.start.Add(time.Duration(int64(c.read) * int64(time.Second) / int64(c.rate)))
		if d := time.Until(due); d > 0 {
			time.Sleep(d)
		}
	}
	return nil
}

// Done reports whether the whole file has been read (what follows is silence).
func (c *WAVCapture) Done() bool { return c.pos >= len(c.samples) }

func (c *WAVCapture) Overruns() int { return 0 }
func (c *WAVCapture) Close() error  { return nil }

// WAVPlayback is a WAV file posing as a speaker: everything written is appended to path when
// the playback is closed. Successive playbacks to the same path append (each one a new file
// would lose all but the last reply), so the file holds every utterance at the first one's
// format; a later utterance at another rate or channel count is an error.
type WAVPlayback struct {
	path     string
	rate     int
	channels int
	pcm      []int16
	closed   bool
}

var wavMu sync.Mutex // serialises read-modify-write of playback files

// CreateWAVPlayback returns a speaker that writes to path.
func CreateWAVPlayback(path string, rate, channels int) (*WAVPlayback, error) {
	if rate <= 0 || channels <= 0 {
		return nil, fmt.Errorf("audio: bad format %d Hz × %d", rate, channels)
	}
	return &WAVPlayback{path: path, rate: rate, channels: channels}, nil
}

func (p *WAVPlayback) Write(buf []int16) error {
	if !wholeFrames(len(buf), p.channels) {
		return fmt.Errorf("audio: write of %d samples is not whole %d-channel frames", len(buf), p.channels)
	}
	p.pcm = append(p.pcm, buf...)
	return nil
}

func (p *WAVPlayback) Drain() error { return nil }

func (p *WAVPlayback) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	wavMu.Lock()
	defer wavMu.Unlock()
	var prev []byte
	if f, err := os.Open(p.path); err == nil {
		s, rate, ch, err := readPCM(f)
		f.Close()
		if err != nil {
			return err
		}
		if rate != p.rate || ch != p.channels {
			return fmt.Errorf("audio: %s holds %d Hz × %d audio; can't append %d Hz × %d", p.path, rate, ch, p.rate, p.channels)
		}
		prev = s
	}
	out, err := os.Create(p.path)
	if err != nil {
		return err
	}
	data := append(prev, pcmBytes(p.pcm)...)
	if err := audio.WritePCM16WAV(out, data, p.channels, p.rate); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// readPCM reads a canonical 44-byte-header WAV as written by WritePCM16WAV.
func readPCM(f *os.File) ([]byte, int, int, error) {
	var h [44]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return nil, 0, 0, err
	}
	if string(h[0:4]) != "RIFF" || string(h[36:40]) != "data" {
		return nil, 0, 0, fmt.Errorf("audio: %s is not a WAV this backend wrote", f.Name())
	}
	ch := int(binary.LittleEndian.Uint16(h[22:]))
	rate := int(binary.LittleEndian.Uint32(h[24:]))
	n := int(binary.LittleEndian.Uint32(h[40:]))
	b := make([]byte, n)
	m, _ := io.ReadFull(f, b)
	return b[:m], rate, ch, nil
}

func pcmBytes(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}
