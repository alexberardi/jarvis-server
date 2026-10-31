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
	// After the ignored answer the countdown carries on from what is left, not a fresh 3 s.
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

// The follow-up window is 3 s on every iteration (#296): no 8 s, then 6, then 5.
func TestFollowUpWindowIsThreeSecondsEveryIteration(t *testing.T) {
	if got := DefaultFollowUp().ListenSecs; got != 3 || MaxFollowUpListenSecs != 3 {
		t.Fatalf("default window %v s, want 3 (the cap %v)", got, MaxFollowUpListenSecs)
	}
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"And what's the date?", "Thanks for that."}, pcmOf(50)
	srv.followUp = func(req StreamRequest) (*Control, error) {
		if req.VoiceCommand == "And what's the date?" {
			return complete("It's October tenth."), nil
		}
		return complete("You're welcome."), nil
	}
	mic := oneWake()
	r := newRig(t, mic, srv)
	ind := &recIndicator{}
	r.p.Indicator = ind
	answer(r, mic, utterance, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if len(tr.FollowUps) != 2 || tr.FollowUpEnd != EndTimeout {
		t.Fatalf("turn %+v", tr)
	}
	if got := followUpWindows(ind); !slices.Equal(got, []time.Duration{3 * time.Second, 3 * time.Second, 3 * time.Second}) {
		t.Fatalf("follow-up windows %v, want 3 s each", got)
	}
}

// A reply that asks something waits for the answer: 8 s, not 3 ("Google's open. What are you
// looking for?" closed before the user had started answering).
func TestFollowUpWindowAfterAQuestion(t *testing.T) {
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"Dogs."}, pcmOf(50)
	srv.reply = func(context.Context) (Reply, error) {
		return audioReply(24000, "Google's open. What are you looking for?", pcmOf(100)), nil
	}
	srv.followUp = func(StreamRequest) (*Control, error) { return complete("Here are dogs."), nil }
	mic := oneWake()
	r := newRig(t, mic, srv)
	ind := &recIndicator{}
	r.p.Indicator = ind
	answer(r, mic, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if got := followUpWindows(ind); !slices.Equal(got, []time.Duration{8 * time.Second, 3 * time.Second}) {
		t.Fatalf("follow-up windows %v, want 8 s after the question, then 3 s (turn %+v)", got, tr)
	}
	for _, r := range []string{"What are you looking for?", `Want me to open "Dogs"?`} {
		if !AsksQuestion(r) {
			t.Errorf("%q is a question", r)
		}
	}
	if AsksQuestion("Done. Anything else? Just say.") {
		t.Error("a question mid-reply isn't the ending")
	}
}

// A window shorter than the old 5 s floor (follow_up_min_timeout_secs) is not raised to it.
func TestShortFollowUpWindowIsKept(t *testing.T) {
	mic := oneWake()
	r := newRig(t, mic, newServer())
	r.p.Cfg.FollowUp.ListenSecs = 1.5
	ind := &recIndicator{}
	r.p.Indicator = ind
	answer(r, mic)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.FollowUpEnd != EndTimeout {
		t.Fatalf("turn %+v", tr)
	}
	if got := followUpWindows(ind); !slices.Equal(got, []time.Duration{1500 * time.Millisecond}) {
		t.Fatalf("follow-up windows %v, want [1.5s]", got)
	}
}

func TestClampFollowUpListen(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{8, 3}, {3.5, 3}, {3, 3}, {2.5, 2.5}, {0, 0}} {
		if got := ClampFollowUpListen(c.in); got != c.want {
			t.Errorf("ClampFollowUpListen(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func followUpWindows(ind *recIndicator) []time.Duration {
	ind.mu.Lock()
	defer ind.mu.Unlock()
	var w []time.Duration
	for _, c := range ind.cues {
		if c.Cue == CueFollowUp {
			w = append(w, c.Window)
		}
	}
	return w
}
