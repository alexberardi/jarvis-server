package voice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// An alarm rings between turns like a take (docs/ALARMS.md D1, D5): bursts of a synthesised
// tone with a listening gap after each, starting quiet and ramping up, until it is stopped,
// snoozed or the limit runs out. The gaps are where D6's voice controls will listen; for now
// they only keep the microphone's queue drained and wait for a control.

// RingAction ends a ring.
type RingAction int

const (
	RingStop    RingAction = iota + 1 // stopped (button, app, voice)
	RingSnooze                        // snoozed: the alarm rings again later
	RingTimeout                       // the limit ran out with nobody stopping it
)

func (a RingAction) String() string {
	switch a {
	case RingStop:
		return "stop"
	case RingSnooze:
		return "snooze"
	case RingTimeout:
		return "timeout"
	}
	return fmt.Sprintf("RingAction(%d)", int(a))
}

// Ring is one alarm ringing.
type Ring struct {
	Limit   time.Duration     // how long it rings unanswered (D1: 4 minutes); 0 is DefaultRingLimit
	Ramp    time.Duration     // from RingFloor to full over this long (D2); 0 is DefaultRingRamp
	Control <-chan RingAction // stop or snooze from outside (button, app); read between bursts

	cancel <-chan struct{} // Mic.Ring's ctx: the caller gave up
}

const (
	DefaultRingLimit = 4 * time.Minute
	DefaultRingRamp  = time.Minute
	// RingFloor is the first burst's share of full loudness.
	RingFloor = 0.15
	// ringGapChunks is the listening gap after each burst: 12 chunks, 0.96 s.
	ringGapChunks = 12
	// ringAmp is a burst's peak at full loudness, of full scale: louder than the chimes
	// (0.25), with headroom. The node's volume scales it further.
	ringAmp = 0.6
)

// ringBurst is one burst at full loudness: four 880 Hz beeps, the classic alarm-clock pattern.
var ringBurst = func() []int16 {
	var ts []tone
	for range 4 {
		ts = append(ts, tone{880, 110}, tone{0, 70})
	}
	pcm := synth(ts...)
	for i, v := range pcm {
		pcm[i] = int16(float64(v) * ringAmp / 0.25) // synth's peak is 0.25
	}
	return pcm
}()

// ringCycle is one burst and its gap.
var ringCycle = time.Duration(len(ringBurst))*time.Second/chimeRate + ringGapChunks*ChunkSamples*time.Second/CaptureRate

// ringGain is the share of full loudness for burst i (from 0) of a ring that ramps over ramp.
func ringGain(i int, ramp time.Duration) float64 {
	n := int(ramp / ringCycle)
	if n <= 0 || i >= n {
		return 1
	}
	return RingFloor + (1-RingFloor)*float64(i)/float64(n)
}

func scaled(pcm []int16, g float64) []int16 {
	out := make([]int16, len(pcm))
	for i, v := range pcm {
		out[i] = int16(math.Round(float64(v) * g))
	}
	return out
}

// Ring rings r between turns with the wake word paused and returns how it ended. It waits for
// a turn in progress to end; ErrMicUnavailable at once if no pipeline is running. Cancelling
// ctx stops the ring (the error is ctx's).
func (m *Mic) Ring(ctx context.Context, r Ring) (RingAction, error) {
	r.cancel = ctx.Done()
	res := make(chan RingAction, 1)
	if _, err := m.Record(ctx, Take{ring: &r, rang: res}); err != nil {
		return 0, err
	}
	return <-res, nil
}

// ring runs r on the pipeline goroutine: a burst, then the gap (what the microphone heard of
// the burst flushed first), until a control, the limit or ctx ends it.
func (p *Pipeline) ring(ctx context.Context, chunks <-chan []int16, r Ring) (RingAction, error) {
	limit, ramp := r.Limit, r.Ramp
	if limit <= 0 {
		limit = DefaultRingLimit
	}
	if ramp <= 0 {
		ramp = DefaultRingRamp
	}
	cycles := int((limit + ringCycle - 1) / ringCycle)
	p.Log.Info("voice: ringing", "limit", limit, "bursts", cycles)
	p.cue(CueSpeaking, 0)
	for i := range cycles {
		if err := p.play(chimeRate, 1, scaled(ringBurst, ringGain(i, ramp))); err != nil {
			return 0, fmt.Errorf("ring: %w", err)
		}
		flush(chunks) // the burst, as heard by the microphone
		for range ringGapChunks {
			select {
			case a := <-r.Control:
				return a, nil
			case <-r.cancel:
				return 0, context.Canceled
			default:
			}
			if _, err := recv(ctx, chunks, time.Second); err != nil {
				if errors.Is(err, errChunkTimeout) {
					return 0, errors.New("ring: no audio from the microphone for 1 s")
				}
				return 0, err
			}
		}
	}
	return RingTimeout, nil
}
