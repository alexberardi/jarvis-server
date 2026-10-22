package voice

import (
	"context"
	"fmt"
	"math"
	"time"
)

// TentativeConfig is the two-stage wake during the node's own music (N:core/wake_loop.py:200-236,
// 403-466): a score in [Threshold, the wake threshold (wakeThreshold)] ducks the music without a turn, so the rest of
// "hey Jarvis" arrives over quieter music and can complete a fire the loud music held below the
// wake threshold. It is not a fire: no debounce, chime, indicator or turn.
type TentativeConfig struct {
	Threshold float64       // wake_word_tentative_threshold, 0.20 (0 disables)
	Window    time.Duration // tentative_wake_window_seconds, 1.6 s: how long the duck waits for a fire
	Cooldown  time.Duration // tentative_wake_cooldown_seconds, 10 s: one tentative at most this often
}

// DefaultTentative is the Python node's configuration out of the box.
func DefaultTentative() TentativeConfig {
	return TentativeConfig{Threshold: 0.20, Window: 1600 * time.Millisecond, Cooldown: 10 * time.Second}
}

// tentative is the wake loop's tentative state since it last (re)started listening.
type tentative struct {
	open      bool
	started   time.Time
	peak      float32
	triggered bool // one opened in this cycle (logged with the fire)
}

// selfPlaying is the music's cached "the node's own music is playing".
func (p *Pipeline) selfPlaying() bool { return p.Music != nil && p.Music.SelfPlaying() }

// wakeClipLen is the wake clip's ring size: 2 s, and while the tentative band is armed (the
// node's own music) the window on top, so a fire that completes a tentative still has the
// phrase from before the duck ("hey jar-").
func (p *Pipeline) wakeClipLen(selfPlaying bool) int {
	if selfPlaying && p.Cfg.Tentative.Threshold > 0 {
		return wakeClipChunks + int(math.Round(p.Cfg.Tentative.Window.Seconds()/chunkSecs))
	}
	return wakeClipChunks
}

// tentativeExpire tracks an open tentative's peak score and closes it once the window has run
// out without a fire.
func (p *Pipeline) tentativeExpire(ctx context.Context, tw *tentative, now time.Time, score float32) {
	if !tw.open {
		return
	}
	tw.peak = max(tw.peak, score)
	if now.Sub(tw.started) >= p.Cfg.Tentative.Window {
		p.tentativeEnd(ctx, tw, "window")
	}
}

// tentativeTrigger opens a tentative for a score in the band (a frame that did not fire),
// during the node's own music and outside the cool-down: the music is ducked now.
func (p *Pipeline) tentativeTrigger(ctx context.Context, tw *tentative, now time.Time, score float32, selfPlaying bool) {
	c := p.Cfg.Tentative
	if tw.open || c.Threshold <= 0 || !selfPlaying || float64(score) < c.Threshold || float64(score) > p.wakeThreshold() {
		return
	}
	if !p.tentativeAt.IsZero() && now.Sub(p.tentativeAt) < c.Cooldown {
		return
	}
	*tw = tentative{open: true, started: now, peak: score, triggered: true}
	p.tentativeAt = now
	p.duck(ctx)
	p.Log.Info("tentative wake triggered", "score", fmt.Sprintf("%.3f", score), "tentative_threshold", c.Threshold,
		"threshold_used", p.wakeThreshold(), "window", c.Window)
}

// tentativeEnd closes an open tentative without a fire and puts the music back.
func (p *Pipeline) tentativeEnd(ctx context.Context, tw *tentative, reason string) {
	if !tw.open {
		return
	}
	tw.open = false
	p.restore(ctx)
	p.Log.Info("tentative wake expired", "score_peak", fmt.Sprintf("%.3f", tw.peak), "window", p.Cfg.Tentative.Window,
		"threshold_used", p.wakeThreshold(), "tentative_threshold", p.Cfg.Tentative.Threshold, "reason", reason)
}
