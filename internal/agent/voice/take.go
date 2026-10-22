package voice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"
)

// Take is a fixed-length recording the pipeline makes between turns: a voice-profile sample
// for enroll_voice / verify_voice (N:scripts/mqtt_tts_listener.py handle_enroll_voice).
type Take struct {
	Cue     string        // said first; "" says nothing. A cue that can't be said is logged, not fatal
	Preroll time.Duration // then this much audio is skipped while the reader starts
	Secs    float64       // then this many seconds are recorded

	sayOnly bool // Say's: the cue is all, and failing to say it is the error
}

// ErrMicUnavailable: no voice pipeline is listening (voice off, or the microphone is being
// reopened), so there is nobody to record a take.
var ErrMicUnavailable = errors.New("voice: no microphone is listening")

// Mic hands takes to the running pipeline, which owns the microphone: it records one between
// turns with the wake word paused (the Python node's wake_paused()), then listens again on
// fresh audio. One Mic outlives pipeline restarts.
type Mic struct {
	reqs     chan *takeReq
	attached atomic.Int32
}

type takeReq struct {
	take Take
	done chan takeResult
}

type takeResult struct {
	pcm []int16
	err error
}

func NewMic() *Mic { return &Mic{reqs: make(chan *takeReq)} }

// Record asks the pipeline for a take and returns it as 48 kHz mono PCM. A take waits for a
// turn in progress to end; ErrMicUnavailable at once if no pipeline is running.
func (m *Mic) Record(ctx context.Context, t Take) ([]int16, error) {
	if m == nil || m.attached.Load() == 0 {
		return nil, ErrMicUnavailable
	}
	r := &takeReq{take: t, done: make(chan takeResult, 1)}
	select {
	case m.reqs <- r:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case res := <-r.done:
		return res.pcm, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Say speaks text between turns with the wake word paused (a timer's expiry, a command's
// audio_speak), as the Python node's announcements do, then listens again on fresh audio. It
// waits for a turn in progress to end; ErrMicUnavailable at once if no pipeline is running.
func (m *Mic) Say(ctx context.Context, text string) error {
	_, err := m.Record(ctx, Take{Cue: text, sayOnly: true})
	return err
}

// takes is the channel the wake loop serves (nil, never ready, without a Mic).
func (p *Pipeline) takes() chan *takeReq {
	if p.Mic == nil {
		return nil
	}
	return p.Mic.reqs
}

// take records t on the pipeline goroutine: the cue, the preroll skipped, then t.Secs of
// audio. A microphone silent for 1 s fails the take.
func (p *Pipeline) take(ctx context.Context, chunks <-chan []int16, t Take) ([]int16, error) {
	if !t.sayOnly {
		p.Log.Info("voice: recording a take", "secs", t.Secs)
	}
	defer p.cue(CueIdle, 0)
	defer p.holdCapture()()
	if t.sayOnly {
		p.cue(CueSpeaking, 0)
		_, err := p.speak(ctx, t.Cue, nil)
		return nil, err
	}
	if t.Cue != "" {
		p.cue(CueSpeaking, 0)
		if _, err := p.speak(ctx, t.Cue, nil); err != nil {
			p.Log.Warn("voice: take cue not said; recording anyway", "err", err)
		}
	}
	flush(chunks) // the cue, as heard by the microphone
	p.cue(CueListening, 0)
	skip := int(math.Ceil(t.Preroll.Seconds() * CaptureRate / ChunkSamples))
	want := int(t.Secs * CaptureRate)
	pcm := make([]int16, 0, want+ChunkSamples)
	for len(pcm) < want {
		c, err := recv(ctx, chunks, time.Second)
		if errors.Is(err, errChunkTimeout) {
			return nil, errors.New("no audio from the microphone for 1 s")
		}
		if err != nil {
			return nil, fmt.Errorf("microphone stopped: %w", err)
		}
		if skip > 0 {
			skip--
			continue
		}
		p.level(c)
		pcm = append(pcm, c...)
	}
	return pcm[:want], nil
}
