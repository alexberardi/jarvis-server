package voice

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// N:tests/test_wake_calibration.py: no calibration on a fresh node.
func TestWakeScoresFallBackUnderFiveSamples(t *testing.T) {
	w := &WakeScores{Path: filepath.Join(t.TempDir(), "wake_scores.json")}
	for _, s := range []float64{0.9, 0.8, 0.7, 0.6} {
		w.Record(s)
	}
	if got := w.Threshold(0.4); got != 0.4 {
		t.Fatalf("threshold with 4 samples %v, want the fallback 0.4", got)
	}
	w.Record(0.5)
	if got := w.Threshold(0.4); got == 0.4 {
		t.Fatal("5 samples still give the fallback")
	}
}

// p20 of the sorted scores, discounted by 15%, clamped to [0.10, 0.50].
func TestWakeScoresThresholdIsP20Discounted(t *testing.T) {
	for _, c := range []struct {
		name   string
		scores []float64
		want   float64
	}{
		// sorted 0.3 0.4 0.5 0.6 0.7: int(5*0.2)=1 → 0.4 × 0.85
		{"p20", []float64{0.7, 0.3, 0.5, 0.4, 0.6}, 0.34},
		{"in range", []float64{0.40, 0.45, 0.48, 0.50, 0.52}, 0.45 * 0.85},
		{"clamped high", []float64{0.99, 0.98, 0.97, 0.96, 0.95}, 0.50},
		{"clamped low", []float64{0.05, 0.06, 0.07, 0.08, 0.09}, 0.10},
	} {
		w := &WakeScores{}
		for _, s := range c.scores {
			w.Record(s)
		}
		if got := w.Threshold(0.4); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("%s: threshold %v, want %v", c.name, got, c.want)
		}
	}
}

// Only the last 20 scores count.
func TestWakeScoresKeepTheLastTwenty(t *testing.T) {
	w := &WakeScores{}
	for range 20 {
		w.Record(0.1) // would clamp to 0.10
	}
	for range 20 {
		w.Record(0.4)
	}
	if got := w.Threshold(0.3); got < 0.34-1e-9 || got > 0.34+1e-9 {
		t.Fatalf("threshold %v, want 0.4×0.85 from the last 20 only", got)
	}
}

func TestWakeScoresRejectOutOfRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wake_scores.json")
	if err := os.WriteFile(path, []byte(`[0.5, -0.1, 1.5, "x", 0.6, 0.7, 0.8]`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &WakeScores{Path: path}
	if got := w.Threshold(0.4); got != 0.4 {
		t.Fatalf("threshold %v: the 3 bad values were counted", got)
	}
	w.Record(-0.2)
	w.Record(1.01)
	if got := w.Threshold(0.4); got != 0.4 {
		t.Fatalf("threshold %v: out-of-range records were counted", got)
	}
	w.Record(0.9) // the fifth good one
	if got := w.Threshold(0.4); got == 0.4 {
		t.Fatal("five good samples still give the fallback")
	}
}

// The history survives a restart through the file; a broken file is not fatal.
func TestWakeScoresSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wake_scores.json")
	w := &WakeScores{Path: path}
	for range 5 {
		w.Record(0.4)
	}
	again := &WakeScores{Path: path}
	if got := again.Threshold(0.3); got < 0.34-1e-9 || got > 0.34+1e-9 {
		t.Fatalf("threshold after a restart %v, want 0.4×0.85", got)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := &WakeScores{Path: path}
	if got := broken.Threshold(0.4); got != 0.4 {
		t.Fatalf("threshold from a broken file %v, want the fallback", got)
	}
	unwritable := &WakeScores{Path: filepath.Join(path, "wake_scores.json")} // under a file
	unwritable.Record(0.7)                                                   // logged, not fatal
	if got := unwritable.Threshold(0.4); got != 0.4 {
		t.Fatalf("threshold %v", got)
	}
}

func notForMe() NotForMeConfig {
	return NotForMeConfig{Quiet: 20 * time.Second, Escalated: time.Minute, Window: 2 * time.Minute, Override: 0.6}
}

// N:core/wake_detector.py decide_wake_fire: the same-utterance debounce.
func TestWakeGateDebounces(t *testing.T) {
	var g wakeGate
	t0 := time.Unix(1000, 0)
	if v := g.decide(0.5, t0, 8*time.Second); !v.fire {
		t.Fatal("the first fire was held")
	}
	if v := g.decide(0.99, t0.Add(7*time.Second), 8*time.Second); v.fire {
		t.Fatal("a second fire 7 s later went through the debounce")
	}
	if v := g.decide(0.5, t0.Add(8*time.Second), 8*time.Second); !v.fire {
		t.Fatal("a fire 8 s later was held: the suppressed one extended the gate")
	}
}

// After not_for_me the gate holds a normal wake for 20 s, but a decisive one punches through,
// disarms the cool-down and leaves only the debounce.
func TestWakeGateNotForMeCooldown(t *testing.T) {
	var g wakeGate
	t0 := time.Unix(1000, 0)
	if d := g.armNotForMe(t0, notForMe()); d != 20*time.Second {
		t.Fatalf("cool-down %v, want 20 s", d)
	}
	if v := g.decide(0.5, t0.Add(5*time.Second), 8*time.Second); v.fire || v.remaining != 15*time.Second || v.override != 0.6 {
		t.Fatalf("0.5 within the cool-down: %+v", v)
	}
	// A suppressed fire does not extend the gate.
	if v := g.decide(0.5, t0.Add(19*time.Second), 8*time.Second); v.fire {
		t.Fatal("0.5 at 19 s fired")
	}
	if v := g.decide(0.5, t0.Add(20*time.Second), 8*time.Second); !v.fire || v.overrode {
		t.Fatalf("0.5 once the cool-down is over: %+v", v)
	}

	g = wakeGate{}
	g.armNotForMe(t0, notForMe())
	v := g.decide(0.6, t0.Add(time.Second), 8*time.Second)
	if !v.fire || !v.overrode {
		t.Fatalf("0.6 (the override) did not punch through: %+v", v)
	}
	// Disarmed: the debounce is hard (no override), then a normal wake fires.
	if v := g.decide(0.99, t0.Add(8*time.Second), 8*time.Second); v.fire {
		t.Fatal("0.99 inside the debounce after an override fired")
	}
	if v := g.decide(0.5, t0.Add(9*time.Second), 8*time.Second); !v.fire {
		t.Fatal("0.5 after the override's debounce was held: the cool-down was not disarmed")
	}
}

// Two verdicts within 120 s escalate to 60 s; the gate never moves backward; quiet <= 0
// turns the cool-down off.
func TestWakeGateNotForMeEscalates(t *testing.T) {
	var g wakeGate
	t0 := time.Unix(1000, 0)
	g.armNotForMe(t0, notForMe())
	if d := g.armNotForMe(t0.Add(30*time.Second), notForMe()); d != time.Minute {
		t.Fatalf("second verdict in 120 s: %v, want 60 s", d)
	}
	if v := g.decide(0.5, t0.Add(89*time.Second), 8*time.Second); v.fire {
		t.Fatal("0.5 at 89 s fired inside the escalated cool-down")
	}
	// Outside the window it is a single verdict again, but the gate armed until 90 s stays.
	g.armNotForMe(t0.Add(200*time.Second), notForMe())
	if d := g.armNotForMe(t0.Add(400*time.Second), notForMe()); d != 20*time.Second {
		t.Fatalf("verdicts 200 s apart: %v, want 20 s", d)
	}
	g = wakeGate{}
	g.armNotForMe(t0, notForMe())
	g.armNotForMe(t0.Add(time.Second), notForMe()) // until 61 s
	g.history = nil
	g.armNotForMe(t0.Add(2*time.Second), notForMe()) // 22 s: shorter, the gate stays at 61 s
	if v := g.decide(0.5, t0.Add(40*time.Second), 8*time.Second); v.fire {
		t.Fatal("a shorter cool-down moved the gate backward")
	}

	g = wakeGate{}
	off := notForMe()
	off.Quiet = 0
	if d := g.armNotForMe(t0, off); d != 0 {
		t.Fatalf("quiet 0 armed %v", d)
	}
	if v := g.decide(0.5, t0.Add(time.Second), 8*time.Second); !v.fire {
		t.Fatal("quiet 0: a wake right after not_for_me was held")
	}
}

// The pipeline: a not_for_me turn arms the cool-down, so a 0.5 wake right after it is held
// and a 0.95 one punches through; neither not_for_me wake is a calibration sample.
func TestNotForMeHoldsAWeakWakeButNotADecisiveOne(t *testing.T) {
	srv := newServer()
	srv.reply = control("not_for_me", nil)
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.WakeScores = &WakeScores{}
	stop := r.run(t)
	defer func() { _ = stop() }()
	if tr := r.turn(t); tr.Outcome != OutcomeNotForMe || tr.FollowUpEnd != EndNotForMe {
		t.Fatalf("turn %+v", tr)
	}
	n := mic.readCount()
	mic.push(rep(weak, 3), rep(speech, 12))
	r.noTurnBy(t, mic, n+3+12+20)
	mic.push(rep(wake, 3), rep(speech, 12))
	if tr := r.turn(t); tr.WakeScore != 0.95 {
		t.Fatalf("the decisive wake: %+v", tr)
	}
	if got := len(r.p.WakeScores.scores); got != 0 {
		t.Fatalf("%d not_for_me wakes recorded", got)
	}
}

// Without the cool-down the same weak wake fires: what held it above was not_for_me.
func TestWeakWakeFiresAfterASpokenTurn(t *testing.T) {
	mic := oneWake()
	r := newRig(t, mic, newServer())
	r.p.Cfg.Debounce = time.Millisecond
	r.p.WakeScores = &WakeScores{}
	stop := r.run(t)
	defer func() { _ = stop() }()
	if tr := r.turn(t); tr.Outcome != OutcomeSpoken {
		t.Fatalf("turn %+v", tr)
	}
	mic.push(rep(weak, 3), rep(speech, 12))
	if tr := r.turn(t); tr.WakeScore != 0.5 {
		t.Fatalf("weak wake: %+v", tr)
	}
	// Both spoken turns are calibration samples.
	if got := r.p.WakeScores.scores; len(got) != 2 || got[0] != float64(float32(0.95)) || got[1] != 0.5 {
		t.Fatalf("recorded %v", got)
	}
}

// A wake over the node's own music is not a calibration sample (its score is music-degraded).
func TestWakeOverOwnMusicIsNotRecorded(t *testing.T) {
	r := newRig(t, oneWake(), newServer())
	r.p.Music = &fakeMusic{done: make(chan struct{}), self: true}
	r.p.WakeScores = &WakeScores{}
	stop := r.run(t)
	if tr := r.turn(t); tr.Outcome != OutcomeSpoken {
		t.Fatalf("turn %+v", tr)
	}
	_ = stop()
	if got := r.p.WakeScores.scores; len(got) != 0 {
		t.Fatalf("recorded %v over the node's own music", got)
	}
}

// With wake_word_threshold_auto the calibrated threshold replaces the static one: 0.6 holds
// a 0.5 wake, but five 0.55 wakes calibrate it to 0.4675, and the 0.5 one fires.
func TestAutoThresholdFromTheHistory(t *testing.T) {
	for _, auto := range []bool{false, true} {
		mic := script(rep(noise, 40), rep(weak, 3), rep(speech, 12))
		r := newRig(t, mic, newServer())
		r.p.Cfg.WakeThreshold, r.p.Cfg.WakeThresholdAuto = 0.6, auto
		r.p.WakeScores = &WakeScores{}
		for range 5 {
			r.p.WakeScores.Record(0.55)
		}
		stop := r.run(t)
		if auto {
			if tr := r.turn(t); tr.WakeScore != 0.5 {
				t.Fatalf("auto: %+v", tr)
			}
		} else {
			r.noTurnBy(t, mic, 40+3+12+20)
		}
		_ = stop()
	}
}
