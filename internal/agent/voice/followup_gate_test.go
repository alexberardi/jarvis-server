package voice

import (
	"context"
	"math"
	"slices"
	"sync"
	"testing"
)

// The node's half of command-center's follow-up gate (go-pi-node.md §3.10): the level it
// reports, and what it does with a rejected answer (keep_listening). As in followup_test.go,
// the user is scripted per listening event; nothing races a timer.

func TestSpeechLevel(t *testing.T) {
	cases := []struct {
		name string
		pcm  [][]int16
		th   float64
		want float64 // dBFS; NaN = unknown
	}{
		{"steady speech", rep(speech, 10), 200, 20 * math.Log10(2000.0/32768)},
		{"silence around speech is ignored", slices.Concat(rep(noise, 20), rep(speech, 5), rep(noise, 20)), 200, 20 * math.Log10(2000.0/32768)},
		{"a loud spike doesn't set it", slices.Concat(rep(square(1000), 8), rep(square(20000), 1)), 200, 20 * math.Log10(1000.0/32768)},
		{"too little speech", slices.Concat(rep(noise, 20), rep(speech, 2)), 200, math.NaN()},
		{"below the threshold", rep(square(150), 20), 200, math.NaN()},
	}
	for _, c := range cases {
		db, ok := SpeechLevel(slices.Concat(c.pcm...), c.th)
		if math.IsNaN(c.want) {
			if ok {
				t.Errorf("%s: level %.2f, want unknown", c.name, db)
			}
			continue
		}
		if !ok || math.Abs(db-c.want) > 0.01 {
			t.Errorf("%s: level %.2f (%v), want %.2f", c.name, db, ok, c.want)
		}
	}
}

func TestFollowUpReportsItsLevelAgainstTheWake(t *testing.T) {
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"And tomorrow?", "Thanks."}, pcmOf(50)
	srv.followUp = func(StreamRequest) (*Control, error) { return complete("Sunny."), nil }
	mic := oneWake() // the command is square(2000)
	r := newRig(t, mic, srv)
	answer(r, mic, rep(square(500), 12), rep(speech, 12))
	stop := r.run(t)
	r.turn(t)
	_ = stop()
	if len(srv.commands) != 2 {
		t.Fatalf("%d follow-ups sent", len(srv.commands))
	}
	quiet := 20 * math.Log10(500.0/2000)
	for i, want := range []float64{math.Round(quiet*10) / 10, 0} {
		got := srv.commands[i].LevelVsWake
		if got == nil || *got != want {
			t.Fatalf("follow-up %d: level_vs_wake_db %v, want %.1f", i+1, got, want)
		}
	}
}

func TestRejectedFollowUpKeepsTheWindowOpen(t *testing.T) {
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"And the score is three to one.", "Is it going to rain?"}, pcmOf(50)
	srv.followUp = func(req StreamRequest) (*Control, error) {
		if req.VoiceCommand == "And the score is three to one." {
			return &Control{StopReason: "not_for_me", KeepListening: true}, nil
		}
		return complete("No rain today."), nil
	}
	mic := oneWake()
	r := newRig(t, mic, srv)
	l := answer(r, mic, utterance, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()

	if len(tr.FollowUps) != 2 || tr.FollowUpEnd != EndTimeout {
		t.Fatalf("window %s, follow-ups %+v", tr.FollowUpEnd, tr.FollowUps)
	}
	if f := tr.FollowUps[0]; f.Outcome != OutcomeIgnored || f.Message != "" || f.ReplySecs != 0 {
		t.Fatalf("rejected answer: %+v", f)
	}
	if f := tr.FollowUps[1]; f.Outcome != OutcomeSpoken || f.Message != "No rain today." {
		t.Fatalf("next answer: %+v", f)
	}
	// Nothing was said for the rejected answer, and it didn't use up an iteration.
	if got := fromStream(srv.callList()); !slices.Equal(got,
		[]string{"stream", "transcribe", "command", "transcribe", "command", "speak", "end"}) {
		t.Fatalf("calls %v", got)
	}
	for i, c := range srv.commands {
		if c.FollowUpIteration == nil || *c.FollowUpIteration != 1 {
			t.Fatalf("command %d iteration %v, want 1", i, c.FollowUpIteration)
		}
	}
	if got := l.list(); !slices.Equal(got, reps(3, "follow_up")) {
		t.Fatalf("listened %v", got)
	}
}

func TestNotForMeWithoutKeepListeningClosesTheWindow(t *testing.T) {
	// A server without the gate (or past its bound) closes the window, as before.
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"Pass the remote."}, pcmOf(50)
	srv.followUp = func(StreamRequest) (*Control, error) { return &Control{StopReason: "not_for_me"}, nil }
	mic := oneWake()
	r := newRig(t, mic, srv)
	l := answer(r, mic, utterance, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.FollowUpEnd != EndNotForMe || len(tr.FollowUps) != 1 || len(l.list()) != 1 {
		t.Fatalf("window %s, follow-ups %+v, listens %v", tr.FollowUpEnd, tr.FollowUps, l.list())
	}
}

func TestIgnoredAnswersAreBoundedOnTheNode(t *testing.T) {
	srv := newServer()
	srv.texts = []string{"one", "two", "three", "four"}
	srv.followUp = func(StreamRequest) (*Control, error) {
		return &Control{StopReason: "not_for_me", KeepListening: true}, nil
	}
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Cfg.FollowUp.MaxIgnored = 3
	answer(r, mic, utterance, utterance, utterance, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.FollowUpEnd != EndIgnored || len(tr.FollowUps) != 3 || len(srv.commands) != 3 {
		t.Fatalf("window %s after %d follow-ups, %d sent", tr.FollowUpEnd, len(tr.FollowUps), len(srv.commands))
	}
}

func TestFollowUpAnswerSurvivesAPauseMidSentence(t *testing.T) {
	// "Can you also add eggs, ... and some bread": a 1 s pause is part of the answer, not its
	// end (the old 0.5 s cut it in two).
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"Can you also add eggs and some bread?"}, pcmOf(50)
	srv.followUp = func(StreamRequest) (*Control, error) { return complete("Added."), nil }
	mic := oneWake()
	r := newRig(t, mic, srv)
	paused := slices.Concat(rep(speech, 12), rep(noise, frames(1.0)), rep(speech, 12))
	answer(r, mic, paused)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if len(tr.FollowUps) != 1 || len(srv.commands) != 1 {
		t.Fatalf("the paused answer was split: %+v", tr.FollowUps)
	}
	if got, min := tr.FollowUps[0].RecordedSecs, float64(24+frames(1.0))*chunkSecs; got < min {
		t.Fatalf("recorded %.2f s, want at least %.2f s (both halves)", got, min)
	}
}

// An ignored answer doesn't buy the room a new window: the window keeps what was left of it,
// but at least IgnoredGraceSecs.
func TestIgnoredAnswerDoesNotRestartTheWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		speech int     // chunks of the ignored answer
		listen float64 // the window (0: the default); long enough to leave a rest after the answer
		want   func(cfg FollowUpConfig, opened []int, end int) (got, want int)
	}{
		{"the rest of the window", 30, 8, func(cfg FollowUpConfig, opened []int, end int) (int, int) {
			// The whole window, the ignored answer included, is ~8 s of audio, not 8 + 8.
			return end - opened[0], frames(cfg.ListenSecs)
		}},
		{"at least the grace", 100, 0, func(cfg FollowUpConfig, opened []int, end int) (int, int) {
			return end - opened[1], frames(cfg.IgnoredGraceSecs)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer()
			srv.texts = []string{"And the score is three to one."}
			srv.followUp = func(StreamRequest) (*Control, error) {
				return &Control{StopReason: "not_for_me", KeepListening: true}, nil
			}
			mic := oneWake()
			r := newRig(t, mic, srv)
			if tc.listen > 0 {
				r.p.Cfg.FollowUp.ListenSecs = tc.listen
			}
			l := answer(r, mic, rep(speech, tc.speech))
			var mu sync.Mutex
			var opened []int
			inner := r.p.listening
			r.p.listening = func(stage string) {
				mu.Lock()
				opened = append(opened, mic.readCount())
				mu.Unlock()
				inner(stage)
			}
			var end int
			r.p.OnTurn = func(tr Turn) { end = mic.readCount(); r.turns <- tr }
			stop := r.run(t)
			tr := r.turn(t)
			_ = stop()
			if tr.FollowUpEnd != EndTimeout || len(tr.FollowUps) != 1 || tr.FollowUps[0].Outcome != OutcomeIgnored {
				t.Fatalf("window %s, follow-ups %+v", tr.FollowUpEnd, tr.FollowUps)
			}
			if got := l.list(); len(got) != 2 {
				t.Fatalf("listened %v", got)
			}
			mu.Lock()
			defer mu.Unlock()
			got, want := tc.want(r.p.Cfg.FollowUp, opened, end)
			// The mic is read ahead of the pipeline by up to its queue.
			if got < want-chunkQueue-4 || got > want+chunkQueue+4 {
				t.Fatalf("listened %d chunks, want ~%d", got, want)
			}
		})
	}
}

func TestAnswerAfterAnIgnoredOneIsNeverAnEcho(t *testing.T) {
	// Nothing was said since the ignored answer, so the mic can't be hearing our reply: the
	// next answer is sent even when it repeats the reply's words.
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"Pass the remote.", "Here is sad music."}, pcmOf(50)
	srv.reply = func(context.Context) (Reply, error) { return audioReply(24000, "Here is sad music.", pcmOf(100)), nil }
	srv.followUp = func(req StreamRequest) (*Control, error) {
		if req.VoiceCommand == "Pass the remote." {
			return &Control{StopReason: "not_for_me", KeepListening: true}, nil
		}
		return complete("Playing it again."), nil
	}
	mic := oneWake()
	r := newRig(t, mic, srv)
	answer(r, mic, utterance, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if len(tr.FollowUps) != 2 || tr.FollowUps[0].Outcome != OutcomeIgnored || tr.FollowUps[1].Outcome != OutcomeSpoken {
		t.Fatalf("window %s, follow-ups %+v", tr.FollowUpEnd, tr.FollowUps)
	}
}
