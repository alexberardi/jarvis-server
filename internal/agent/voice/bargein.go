package voice

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// Barge-in (go-pi-node.md §1.1 row 17, N:core/barge_in.py): while a reply plays, the wake
// detector keeps scoring the microphone at a much lower threshold, and a wake word heard over
// the reply stops it. Speaker echo would trip a low threshold on its own, so an energy gate
// learns the reply's level through the mic during its first second and asks for twice that.
// Nothing is sent to the server: the pipeline just goes back to waiting for the wake word.

// bargeInBaselineMultiplier is how far above the reply's own level (its p75 RMS) a voice must
// be (_BASELINE_MULTIPLIER).
const bargeInBaselineMultiplier = 2.0

// bargeInWindowSecs is the trailing energy window: openWakeWord's score peaks 0.5-1 s after
// the voice's energy, so the gate asks whether a spike happened recently, not on this chunk.
const bargeInWindowSecs = 1.0

// BargeInConfig tunes barge-in. DefaultBargeIn is the Python node's.
type BargeInConfig struct {
	Enabled       bool          // barge_in_enabled (true)
	Threshold     float64       // wake score above this counts (barge_in_threshold 0.04)
	EnergyFloor   float64       // RMS the trailing window must beat at least (barge_in_energy_threshold 500)
	ConfirmChunks int           // qualifying chunks in a row that stop the reply (2)
	Skip          time.Duration // audio ignored at the start of a reply (0.5 s)
	Baseline      time.Duration // audio after the skip that learns the reply's level (1 s)
}

// DefaultBargeIn is the Python node's barge-in (N:core/vad_thresholds.py, barge_in.py).
func DefaultBargeIn() BargeInConfig {
	return BargeInConfig{Enabled: true, Threshold: 0.04, EnergyFloor: 500, ConfirmChunks: 2,
		Skip: 500 * time.Millisecond, Baseline: time.Second}
}

// bargeIn decides, one 80 ms capture chunk at a time, whether the user is talking over the
// reply. It is pure: the pipeline feeds it each chunk's RMS and wake score.
type bargeIn struct {
	cfg       BargeInConfig
	skipN     int
	baselineN int
	skipped   int
	base      []float64
	recent    ring[float64]
	effective float64 // the energy gate: max(EnergyFloor, baseline p75 × 2)
	run       int     // qualifying chunks in a row
}

func newBargeIn(cfg BargeInConfig) *bargeIn {
	return &bargeIn{cfg: cfg, skipN: frames(cfg.Skip.Seconds()),
		baselineN: max(1, frames(cfg.Baseline.Seconds())),
		recent:    ring[float64]{n: max(1, frames(bargeInWindowSecs))}, effective: cfg.EnergyFloor}
}

// skipChunk reports whether the next chunk falls in the skip at the start of the reply (and
// counts it): such a chunk is neither scored nor fed.
func (b *bargeIn) skipChunk() bool {
	if b.skipped < b.skipN {
		b.skipped++
		return true
	}
	return false
}

// learning reports whether the baseline is still being collected (scores are not checked yet,
// but the detector should still see the audio to keep its state primed).
func (b *bargeIn) learning() bool { return len(b.base) < b.baselineN }

// recentMax is the loudest chunk in the trailing window.
func (b *bargeIn) recentMax() float64 { return slices.Max(b.recent.items) }

// feed takes one chunk's RMS and wake score and reports whether to stop the reply.
func (b *bargeIn) feed(rms float64, score float32) bool {
	b.recent.push(rms)
	if b.learning() {
		b.base = append(b.base, rms)
		if !b.learning() {
			sorted := slices.Sorted(slices.Values(b.base))
			b.effective = max(b.cfg.EnergyFloor, sorted[int(float64(len(sorted))*0.75)]*bargeInBaselineMultiplier)
		}
		return false
	}
	// (Python's "tier 1", score > 0.5 with the same energy gate, is implied by this.)
	if float64(score) > b.cfg.Threshold && b.recentMax() > b.effective {
		b.run++
	} else {
		b.run = 0
	}
	return b.run >= b.cfg.ConfirmChunks
}

// errBargeIn is playAudio's error when the user talked over the reply. Callers report it as
// OutcomeInterrupted and EndBargeIn, not as a failure: no error chime, no follow-up window.
var errBargeIn = errors.New("voice: the user talked over the reply")

// watchBargeIn scores the microphone while a reply plays and calls interrupt once the user
// talks over it. stop ends the watch, waits for it, and resets the detector, as the Python
// node does after every monitored reply.
//
// It runs p.Detector on its own goroutine. That is safe because the wake loop (listen), the
// detector's only other user, is blocked in turn() for as long as a reply plays, and stop
// waits for the goroutine before playAudio returns.
func (p *Pipeline) watchBargeIn(log *slog.Logger, chunks <-chan []int16, interrupt func()) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flush(chunks) // captured while the reply was being prepared, not over it
		b := newBargeIn(p.Cfg.BargeIn)
		dec := audio.NewDecimator(decimation)
		var pend []int16
		var score, maxScore float32
		var maxRMS float64
		n := 0
		defer func() {
			log.Debug("barge-in: stopped", "chunks", n, "max_score", fmt.Sprintf("%.3f", maxScore),
				"max_rms", int(maxRMS), "effective", int(b.effective))
		}()
		for {
			var c []int16
			select {
			case <-done:
				return
			case cc, ok := <-chunks:
				if !ok {
					return
				}
				c = cc
			}
			if b.skipChunk() {
				continue
			}
			n++
			rms := RMS(c)
			maxRMS = max(maxRMS, rms)
			pend = dec.Process(c, pend)
			for len(pend) >= wakeChunk {
				s, err := p.Detector.Process(pend[:wakeChunk])
				if err != nil {
					log.Warn("barge-in: wake word", "err", err)
					return
				}
				score = s
				pend = append(pend[:0], pend[wakeChunk:]...)
			}
			learning := b.learning()
			if !learning {
				maxScore = max(maxScore, score)
			}
			fire := b.feed(rms, score)
			switch {
			case fire:
				log.Info("barge-in: the user talked over the reply", "score", fmt.Sprintf("%.3f", score),
					"rms", int(rms), "recent_max_rms", int(b.recentMax()), "effective", int(b.effective))
				interrupt()
				return
			case learning && !b.learning():
				log.Debug("barge-in: baseline", "effective", int(b.effective))
			case !learning && score > 0.05:
				log.Debug("barge-in: score", "score", fmt.Sprintf("%.3f", score), "rms", int(rms),
					"recent_max_rms", int(b.recentMax()), "effective", int(b.effective))
			case n%25 == 1:
				log.Debug("barge-in: audio", "chunk", n, "rms", int(rms), "recent_max_rms", int(b.recentMax()),
					"effective", int(b.effective), "max_score", fmt.Sprintf("%.3f", maxScore))
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
		p.Detector.Reset()
	}
}
