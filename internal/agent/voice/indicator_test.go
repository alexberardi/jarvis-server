package voice

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// The on-screen indicator (indicator.go) is told every state change of a turn, the follow-up
// window's time left included, and the loudness of what is heard and said.

type cueRec struct {
	Cue    Cue
	Window time.Duration
}

type recIndicator struct {
	mu     sync.Mutex
	cues   []cueRec
	levels map[Cue]int // Level calls with a non-zero level, by the state they came in
}

func (r *recIndicator) SetState(c Cue, w time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.cues); n > 0 && r.cues[n-1].Cue == c && c != CueFollowUp {
		return
	}
	r.cues = append(r.cues, cueRec{c, w})
}

func (r *recIndicator) Level(v float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.levels == nil {
		r.levels = map[Cue]int{}
	}
	if v > 0 && len(r.cues) > 0 {
		r.levels[r.cues[len(r.cues)-1].Cue]++
	}
}

func (r *recIndicator) states() []Cue {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Cue, len(r.cues))
	for i, c := range r.cues {
		out[i] = c.Cue
	}
	return out
}

func TestIndicatorFollowsTheTurn(t *testing.T) {
	srv := newServer()
	srv.texts = []string{"And the score is three to one."}
	srv.followUp = func(StreamRequest) (*Control, error) {
		return &Control{StopReason: "not_for_me", KeepListening: true}, nil
	}
	mic := oneWake()
	r := newRig(t, mic, srv)
	ind := &recIndicator{}
	r.p.Indicator = ind
	answer(r, mic, rep(speech, 30))
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.FollowUpEnd != EndTimeout || len(tr.FollowUps) != 1 || tr.FollowUps[0].Outcome != OutcomeIgnored {
		t.Fatalf("turn %+v", tr)
	}

	want := []Cue{CueListening, CueThinking, CueSpeaking, CueFollowUp, CueListening, CueThinking, CueFollowUp, CueIdle}
	if got := ind.states(); !slices.Equal(got, want) {
		t.Fatalf("cues %v, want %v", got, want)
	}
	ind.mu.Lock()
	defer ind.mu.Unlock()
	first, second := ind.cues[3].Window, ind.cues[6].Window
	if first != time.Duration(DefaultFollowUp().ListenSecs*float64(time.Second)) {
		t.Fatalf("first window %v", first)
	}
	// After the ignored answer the countdown carries on from what is left, not a fresh 8 s.
	if second >= first || second < time.Duration(DefaultFollowUp().IgnoredGraceSecs*float64(time.Second)) {
		t.Fatalf("window after the ignored answer %v (first %v)", second, first)
	}
	if ind.levels[CueListening] == 0 || ind.levels[CueSpeaking] == 0 {
		t.Fatalf("levels by state %v: want the mic while listening and the reply while speaking", ind.levels)
	}
}

func TestIndicatorFlashesAFailedTurn(t *testing.T) {
	srv := newServer()
	srv.reply = func(context.Context) (Reply, error) { return Reply{}, errors.New("boom") }
	r := newRig(t, oneWake(), srv)
	ind := &recIndicator{}
	r.p.Indicator = ind
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeError {
		t.Fatalf("turn %+v", tr)
	}
	got := ind.states()
	if len(got) == 0 || got[len(got)-1] != CueError || got[0] != CueListening {
		t.Fatalf("cues %v: want listening first and error last", got)
	}
}
