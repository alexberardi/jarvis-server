package voice

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Wake-threshold auto-calibration (N:core/wake_calibration.py), on with
// wake_word_threshold_auto: each wake that led to a real exchange (not not_for_me, not an
// error, not over the node's own music) records its score, and once there are enough the
// threshold sits just under the quieter real wakes.
const (
	wakeScoresMax        = 20
	wakeScoresMinSamples = 5
	wakeScoresFile       = "wake_scores.json"
)

// WakeScores is the recent legitimate wake scores, kept in Path (a JSON list of floats) so
// calibration survives a restart. Disk errors are logged, never fatal. Empty Path keeps them
// in memory only. One WakeScores outlives the pipelines Serve restarts.
type WakeScores struct {
	Path string
	Log  *slog.Logger

	mu     sync.Mutex
	loaded bool
	scores []float64
}

// WakeScoresPath is the history's file in the voice cache dir (CacheDir).
func WakeScoresPath(cacheDir string) string { return filepath.Join(cacheDir, wakeScoresFile) }

func (w *WakeScores) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

func validScore(s float64) bool { return s >= 0 && s <= 1 }

// load reads Path once. Values outside [0, 1] (or not numbers) are dropped. Call with mu held.
func (w *WakeScores) load() {
	if w.loaded {
		return
	}
	w.loaded = true
	if w.Path == "" {
		return
	}
	b, err := os.ReadFile(w.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var raw []any
	if err == nil {
		err = json.Unmarshal(b, &raw)
	}
	if err != nil {
		w.log().Warn("voice: failed to load the wake score history", "path", w.Path, "err", err)
		return
	}
	for _, v := range raw[max(0, len(raw)-wakeScoresMax):] {
		if f, ok := v.(float64); ok && validScore(f) {
			w.scores = append(w.scores, f)
		}
	}
}

// Record adds one legitimate wake's score (outside [0, 1] is ignored) and saves the history.
func (w *WakeScores) Record(score float64) {
	if !validScore(score) {
		return
	}
	w.mu.Lock()
	w.load()
	w.scores = append(w.scores, score)
	if n := len(w.scores); n > wakeScoresMax {
		w.scores = slices.Delete(w.scores, 0, n-wakeScoresMax)
	}
	snapshot := slices.Clone(w.scores)
	w.mu.Unlock()
	if w.Path == "" {
		return
	}
	b, _ := json.Marshal(snapshot)
	err := os.MkdirAll(filepath.Dir(w.Path), 0o700)
	if err == nil {
		err = os.WriteFile(w.Path, b, 0o600)
	}
	if err != nil {
		w.log().Warn("voice: failed to save the wake score history", "path", w.Path, "err", err)
	}
}

// Threshold is the calibrated wake threshold: the 20th percentile of the recent scores less
// 15%, clamped to [0.10, 0.50]; fallback until there are wakeScoresMinSamples of them.
func (w *WakeScores) Threshold(fallback float64) float64 {
	w.mu.Lock()
	w.load()
	scores := slices.Clone(w.scores)
	w.mu.Unlock()
	if len(scores) < wakeScoresMinSamples {
		return fallback
	}
	slices.Sort(scores)
	p20 := scores[int(float64(len(scores))*0.20)]
	return max(0.10, min(0.50, p20*0.85))
}

// NotForMeConfig is the soft cool-down after a not_for_me verdict (N:core/voice_filters.py,
// its defaults): the wake gate stays shut for Quiet (Escalated when there were 2 or more
// verdicts within Window), except to a score of Override or more. Quiet <= 0 turns it off.
type NotForMeConfig struct {
	Quiet     time.Duration // not_for_me_quiet_seconds (20 s)
	Escalated time.Duration // not_for_me_escalated_quiet_seconds (60 s)
	Window    time.Duration // not_for_me_escalation_window_seconds (120 s)
	Override  float64       // not_for_me_override_threshold (0.6)
}

// DefaultNotForMe is N:'s cool-down.
func DefaultNotForMe() NotForMeConfig {
	return NotForMeConfig{Quiet: 20 * time.Second, Escalated: time.Minute, Window: 2 * time.Minute, Override: 0.6}
}

// wakeGate is when a wake above the threshold may fire (N:core/wake_detector.py
// decide_wake_fire): the same-utterance debounce after every fire, and the not_for_me
// cool-down. Used on the pipeline goroutine only.
type wakeGate struct {
	minNext  time.Time   // no fire before this
	override float64     // while minNext is ahead, a score this high fires anyway (0: none)
	history  []time.Time // recent not_for_me verdicts, for the escalation
}

type gateVerdict struct {
	fire      bool
	overrode  bool          // fired through the not_for_me cool-down
	remaining time.Duration // of the gate, when it was armed
	override  float64       // the gate's override then
}

// decide is a wake above the threshold at now. A fire (an override's too) re-arms only the
// debounce and disarms the override; a suppressed one leaves the gate as it was.
func (g *wakeGate) decide(score float64, now time.Time, debounce time.Duration) gateVerdict {
	v := gateVerdict{remaining: g.minNext.Sub(now), override: g.override}
	switch {
	case v.remaining > 0 && g.override > 0 && score >= g.override:
		v.fire, v.overrode = true, true
	case v.remaining > 0:
		return v
	default:
		v.fire = true
	}
	g.override, g.minNext = 0, now.Add(debounce)
	return v
}

// armNotForMe arms the cool-down after a not_for_me verdict at now and returns its length
// (0: off). The gate never moves backward.
func (g *wakeGate) armNotForMe(now time.Time, c NotForMeConfig) time.Duration {
	if c.Quiet <= 0 {
		return 0
	}
	g.history = append(g.history, now)
	g.history = slices.DeleteFunc(g.history, func(t time.Time) bool { return t.Before(now.Add(-c.Window)) })
	d := c.Quiet
	if len(g.history) >= 2 {
		d = c.Escalated
	}
	if until := now.Add(d); until.After(g.minNext) {
		g.minNext = until
	}
	g.override = c.Override
	return d
}

// wakeThreshold is the score a wake must beat now: the calibrated one when it is on.
func (p *Pipeline) wakeThreshold() float64 {
	if p.Cfg.WakeThresholdAuto && p.WakeScores != nil {
		return p.WakeScores.Threshold(p.Cfg.WakeThreshold)
	}
	return p.Cfg.WakeThreshold
}

// fire is whether a wake above the threshold goes through the gate now.
func (p *Pipeline) fire(score float32) bool {
	v := p.gate.decide(float64(score), p.Now(), p.Cfg.Debounce)
	switch {
	case v.overrode:
		p.Log.Info("wake-override-cooldown", "score", fmt.Sprintf("%.3f", score), "override_threshold", v.override,
			"cooldown_remaining_s", fmt.Sprintf("%.2f", v.remaining.Seconds()))
	case !v.fire && score > 0.3:
		p.Log.Info("wake-suppressed-gate", "score", fmt.Sprintf("%.3f", score),
			"cooldown_remaining_s", fmt.Sprintf("%.2f", v.remaining.Seconds()), "soft_override_threshold", v.override)
	}
	return v.fire
}

// afterWake feeds the turn's verdict back to the wake gate (N:core/wake_loop.py, follow_up_loop.py):
// not_for_me, from the woken command or a follow-up answer, arms the cool-down; a wake that
// led to a real exchange, not over the node's own music, is a calibration sample. Errors and
// a wake that heard no words are neither.
func (p *Pipeline) afterWake(log *slog.Logger, t *Turn, selfPlaying bool) {
	switch {
	case t.Outcome == OutcomeNotForMe || t.FollowUpEnd == EndNotForMe:
		if d := p.gate.armNotForMe(p.Now(), p.Cfg.NotForMe); d > 0 {
			log.Info("not-for-me-cooldown-armed", "cooldown_s", d.Seconds(), "recent_events", len(p.gate.history),
				"override_threshold", p.Cfg.NotForMe.Override)
		}
	case t.Err != nil || selfPlaying || p.WakeScores == nil:
	case t.Outcome == OutcomeSpoken || t.Outcome == OutcomeSilent || t.Outcome == OutcomeInterrupted:
		p.WakeScores.Record(float64(t.WakeScore))
	}
}
