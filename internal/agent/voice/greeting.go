package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// The wake greeting (go-pi-node.md §1.1 rows 6 and 20, N:core/wake_response.py): while idle the
// Pi asks command-center for its next greeting (/wake-response: a few words from the live
// model), renders it once with TTS and keeps the audio in memory; the next wake plays it instead
// of the chime. A miss (nothing cached yet, or the fetch failed) plays the chime. Each greeting
// plays once. The next one is fetched at start and after each turn, never on the wake's
// critical path: there it would land between the warmup and the command's first inference and
// evict the warmed KV cache of the model's single slot (the Python node's finding).

// WakeResponder is command-center's /wake-response (*HTTPServer).
type WakeResponder interface {
	WakeResponse(ctx context.Context) (string, error)
}

const (
	greetingTimeout = 30 * time.Second
	// greetingMaxBytes bounds the rendered greeting (~40 s of 24 kHz audio; a real one is ~1 s).
	greetingMaxBytes = 2 << 20
	// greetingSilence is the level under which TTS's lead-in and tail are trimmed (Python's
	// _trim_wav_silence: about -42 dBFS).
	greetingSilence = 200
)

type greeting struct {
	text           string
	rate, channels int
	pcm            []int16
}

// greeter is the cached greeting: the pipeline goroutine takes it, a prefetch fills it.
type greeter struct {
	mu   sync.Mutex
	next *greeting
	busy bool // a prefetch is on its way
}

// greetingSource is the server's /wake-response when the greeting is on.
func (p *Pipeline) greetingSource() (WakeResponder, bool) {
	wr, ok := p.Server.(WakeResponder)
	return wr, ok && p.Cfg.Greeting && p.Cfg.Chimes
}

// prefetchGreeting fetches the next greeting in the background, unless one is cached or on its
// way. Run waits for it (p.ends); ctx ending abandons it.
func (p *Pipeline) prefetchGreeting(ctx context.Context) {
	wr, ok := p.greetingSource()
	if !ok {
		return
	}
	p.greet.mu.Lock()
	if p.greet.busy || p.greet.next != nil {
		p.greet.mu.Unlock()
		return
	}
	p.greet.busy = true
	p.greet.mu.Unlock()
	p.ends.Add(1)
	go func() {
		defer p.ends.Done()
		g, err := p.fetchGreeting(ctx, wr)
		if err != nil && ctx.Err() == nil {
			p.Log.Warn("wake greeting: not fetched, the chime stands in", "err", err)
		}
		p.greet.mu.Lock()
		p.greet.next, p.greet.busy = g, false
		p.greet.mu.Unlock()
	}()
}

func (p *Pipeline) fetchGreeting(ctx context.Context, wr WakeResponder) (*greeting, error) {
	ctx, cancel := context.WithTimeout(ctx, greetingTimeout)
	defer cancel()
	text, err := wr.WakeResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("wake-response: %w", err)
	}
	a, err := p.Server.Speak(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("tts: %w", err)
	}
	defer a.Close()
	raw, err := io.ReadAll(io.LimitReader(a.Body, greetingMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("tts: %w", err)
	}
	if len(raw) > greetingMaxBytes {
		return nil, fmt.Errorf("tts: greeting %q over %d bytes", text, greetingMaxBytes)
	}
	raw = raw[:len(raw)-len(raw)%(2*a.Channels)]
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	pcm = trimSilence(pcm, a.Channels, greetingSilence)
	if len(pcm) == 0 {
		return nil, fmt.Errorf("tts: greeting %q is silent", text)
	}
	return &greeting{text: text, rate: a.Rate, channels: a.Channels, pcm: pcm}, nil
}

// takeGreeting is the cached greeting, now used up, or nil.
func (p *Pipeline) takeGreeting() *greeting {
	if _, ok := p.greetingSource(); !ok {
		return nil
	}
	p.greet.mu.Lock()
	defer p.greet.mu.Unlock()
	g := p.greet.next
	p.greet.next = nil
	return g
}

// acknowledge answers the wake word (row 6): the cached greeting, else the wake chime. It
// returns once the speaker has played it (the wake word is not listened for meanwhile: the
// pipeline is in the turn), and reports whether the greeting played.
func (p *Pipeline) acknowledge(ctx context.Context) bool {
	if g := p.takeGreeting(); g != nil && ctx.Err() == nil {
		err := p.play(g.rate, g.channels, g.pcm)
		if err == nil {
			p.Log.Debug("wake greeting played", "text", g.text)
			return true
		}
		p.Log.Warn("wake greeting failed, playing the chime", "err", err)
	}
	p.chime(ctx, wakeChime)
	return false
}

// play plays pcm and waits for the speaker to drain.
func (p *Pipeline) play(rate, channels int, pcm []int16) error {
	pb, err := p.Playback(rate, channels)
	if err != nil {
		return fmt.Errorf("speaker unavailable: %w", err)
	}
	defer pb.Close()
	if err := pb.Write(pcm); err != nil {
		return err
	}
	return pb.Drain()
}

// trimSilence drops the frames before the first and after the last one with a sample at level
// or above.
func trimSilence(pcm []int16, channels, level int) []int16 {
	channels = max(channels, 1)
	loud := func(f int) bool {
		for _, v := range pcm[f*channels : (f+1)*channels] {
			if max(int(v), -int(v)) >= level {
				return true
			}
		}
		return false
	}
	n := len(pcm) / channels
	first, last := 0, n-1
	for first < n && !loud(first) {
		first++
	}
	for last >= first && !loud(last) {
		last--
	}
	return pcm[first*channels : (last+1)*channels]
}

// WakeResponse is POST /wake-response: the next wake greeting's text (jarvisd answers 200
// "Yes?" when its model can't).
func (s *HTTPServer) WakeResponse(ctx context.Context) (string, error) {
	var r struct {
		Text string `json:"text"`
	}
	if err := s.postJSON(ctx, "/api/v0/wake-response", map[string]any{}, &r); err != nil {
		return "", err
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return "", errors.New("no greeting text")
	}
	return text, nil
}
