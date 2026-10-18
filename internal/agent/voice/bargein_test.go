package voice

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"
)

// feedAll runs a sequence of (rms, score) chunks through a fresh detector and returns the
// index of the chunk that fired, or -1. Skipped chunks count toward the index.
func feedAll(b *bargeIn, rms []float64, score []float32) int {
	for i := range rms {
		if b.skipChunk() {
			continue
		}
		if b.feed(rms[i], score[i]) {
			return i
		}
	}
	return -1
}

// quietReply is the skip and baseline windows (18 chunks) at TTS-through-mic level rms, with
// the detector quiet.
func quietReply(rms float64) ([]float64, []float32) {
	n := 6 + 12
	r, s := make([]float64, n), make([]float32, n)
	for i := range r {
		r[i], s[i] = rms, 0.01
	}
	return r, s
}

func TestBargeInWindowsMatchPython(t *testing.T) {
	b := newBargeIn(DefaultBargeIn())
	// 0.5 s skip, 1 s baseline, a 1 s trailing energy window, all in 80 ms chunks.
	if b.skipN != 6 || b.baselineN != 12 || b.recent.n != 12 || b.cfg.ConfirmChunks != 2 ||
		b.cfg.Threshold != 0.04 || b.cfg.EnergyFloor != 500 {
		t.Fatalf("barge-in %+v", b)
	}
}

func TestBargeInNeedsTwoQualifyingChunksInARow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rms   []float64
		score []float32
		want  int
	}{
		{"one chunk", []float64{5000, 50, 50, 50}, []float32{0.9, 0.01, 0.01, 0.01}, -1},
		{"two in a row", []float64{5000, 5000, 50}, []float32{0.9, 0.9, 0.01}, 1},
		{"broken by a quiet score", []float64{5000, 5000, 5000, 5000}, []float32{0.9, 0.01, 0.9, 0.01}, -1},
		// OWW lags the voice: a weak score just after a loud chunk still counts, through the
		// trailing 1 s energy window.
		{"score lags the energy", []float64{5000, 50, 50}, []float32{0.01, 0.05, 0.05}, 2},
		{"score at the threshold is not above it", []float64{5000, 5000, 5000}, []float32{0.04, 0.04, 0.04}, -1},
		{"loud but no wake word", []float64{5000, 5000, 5000}, []float32{0.01, 0.01, 0.01}, -1},
		{"wake word but quiet", []float64{400, 400, 400}, []float32{0.9, 0.9, 0.9}, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := quietReply(50)
			r, s = append(r, tc.rms...), append(s, tc.score...)
			got := feedAll(newBargeIn(DefaultBargeIn()), r, s)
			if tc.want >= 0 {
				tc.want += 18
			}
			if got != tc.want {
				t.Fatalf("fired at %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBargeInNeverFiresDuringSkipOrBaseline(t *testing.T) {
	b := newBargeIn(DefaultBargeIn())
	for i := range 18 {
		if b.skipChunk() {
			if i >= 6 {
				t.Fatalf("chunk %d skipped; the skip is 6 chunks", i)
			}
			continue
		}
		if i < 6 {
			t.Fatalf("chunk %d scored during the skip", i)
		}
		if b.feed(5000, 0.99) {
			t.Fatalf("fired at chunk %d, inside the baseline window", i)
		}
	}
}

// The reply itself saying "Jarvis": the detector spikes but the energy stays at the TTS level
// the baseline learned, so it must not interrupt itself.
func TestBargeInBaselineBlocksTheReplysOwnWakeWord(t *testing.T) {
	r, s := quietReply(3000) // a loud speaker: effective threshold 6000
	r = append(r, 3500, 3500, 3500)
	s = append(s, 0.9, 0.9, 0.9)
	b := newBargeIn(DefaultBargeIn())
	if got := feedAll(b, r, s); got != -1 {
		t.Fatalf("fired at %d on the reply's own level", got)
	}
	if b.effective != 6000 {
		t.Fatalf("effective threshold %v, want p75 3000 × 2", b.effective)
	}
	// Someone talking over it, louder than twice the reply, does.
	if b.feed(7000, 0.9) {
		t.Fatal("fired on the first qualifying chunk")
	}
	if !b.feed(7000, 0.9) {
		t.Fatal("a voice above twice the reply's level did not interrupt")
	}
}

func TestBargeInBaselineIsTheP75(t *testing.T) {
	b := newBargeIn(DefaultBargeIn())
	for range 6 {
		b.skipChunk()
	}
	b.skipChunk() // the skip is over: no effect
	for i := range 12 {
		b.feed(float64(100*(i+1)), 0) // 100..1200: sorted[9] = 1000
	}
	if b.effective != 2000 {
		t.Fatalf("effective %v, want 2000", b.effective)
	}
	q := newBargeIn(DefaultBargeIn())
	for range 6 {
		q.skipChunk()
	}
	for range 12 {
		q.feed(10, 0)
	}
	if q.effective != 500 {
		t.Fatalf("a silent speaker: effective %v, want the 500 floor", q.effective)
	}
}

// --- in the pipeline (lockstep rig) ---

// replyBlock is 80 ms of 24 kHz reply audio.
const replyBlock = 1920

// pacedBody is a long reply that keeps time with the microphone: one 80 ms block per chunk
// the mic has served since the last read. In lockstep the mic only advances when the pipeline
// takes a chunk, so the reply is still playing while the scripted mic is heard, however the
// scheduler runs the goroutines. It stops when the request is cancelled.
type pacedBody struct {
	ctx    context.Context
	mic    *fakeCapture
	blocks int
	last   int
}

func (b *pacedBody) Read(p []byte) (int, error) {
	if b.blocks == 0 {
		return 0, io.EOF
	}
	deadline := time.Now().Add(5 * time.Second) // a hang guard
	for b.mic.readCount() <= b.last {
		if b.ctx.Err() != nil {
			return 0, b.ctx.Err()
		}
		if time.Now().After(deadline) {
			return 0, errors.New("pacedBody: the microphone stopped")
		}
		time.Sleep(time.Millisecond)
	}
	b.last = b.mic.readCount()
	b.blocks--
	n := min(len(p), 2*replyBlock)
	clear(p[:n])
	return n, nil
}
func (b *pacedBody) Close() error { return nil }

// longReply is an 8 s streamed reply; when it starts, the mic hears during.
func longReply(mic *fakeCapture, during ...[][]int16) func(context.Context) (Reply, error) {
	return func(ctx context.Context) (Reply, error) {
		mic.push(during...)
		return Reply{Audio: &Audio{Rate: 24000, Channels: 1, Message: "Once upon a time…",
			Body: &pacedBody{ctx: ctx, mic: mic, blocks: 100}}}, nil
	}
}

// overReply is what the mic hears once the reply starts: room noise through the skip and the
// baseline (and a chunk or two of slack), then n chunks of "hey jarvis".
func overReply(room []int16, n int) [][]int16 { return slices.Concat(rep(room, 25), rep(wake, n)) }

// replyOut is the last thing played at rate.
func (r *rig) replyOut(t *testing.T, rate int) played {
	t.Helper()
	outs := r.spk.all()
	for i := len(outs) - 1; i >= 0; i-- { // the last: the wake chime is 24 kHz too
		if outs[i].rate == rate {
			return outs[i]
		}
	}
	t.Fatalf("nothing played at %d Hz", rate)
	return played{}
}

func TestBargeInStopsTheReplyAndGoesBackToTheWakeWord(t *testing.T) {
	mic := oneWake()
	srv := newServer()
	srv.reply = longReply(mic, overReply(noise, 2))
	r := newRig(t, mic, srv)
	ind := &recIndicator{}
	r.p.Indicator = ind
	l := answer(r, mic)
	stop := r.run(t)
	tr := r.turn(t)
	r.waitResets(t, 3) // the fire, after the reply's monitor, on resuming
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if tr.Outcome != OutcomeInterrupted || tr.Err != nil || tr.FollowUpEnd != EndBargeIn || len(tr.FollowUps) != 0 {
		t.Fatalf("turn %+v", tr)
	}
	out := r.replyOut(t, 24000)
	if frames := len(out.pcm); frames == 0 || frames > 50*replyBlock || out.drained {
		t.Fatalf("played %d of %d frames (drained %v); want stopped early, not drained", frames, 100*replyBlock, out.drained)
	}
	if tr.ReplySecs <= 0 || tr.ReplySecs > 4 {
		t.Fatalf("reply_s %.2f", tr.ReplySecs)
	}
	// No follow-up window, nothing sent: just the conversation's end.
	if got := fromStream(srv.callList()); !slices.Equal(got, []string{"stream", "end"}) {
		t.Fatalf("calls %v", got)
	}
	if got := l.list(); len(got) != 0 {
		t.Fatalf("listened without the wake word: %v", got)
	}
	if got := ind.states(); got[len(got)-1] != CueIdle || slices.Contains(got, CueError) || slices.Contains(got, CueFollowUp) {
		t.Fatalf("cues %v", got)
	}
	select {
	case tr := <-r.turns:
		t.Fatalf("the barge-in's wake word started a turn: %+v", tr)
	default:
	}
}

func TestBargeInStopsAFollowUpReply(t *testing.T) {
	mic := oneWake()
	srv := newServer()
	srv.texts = []string{"Tell me a story."}
	srv.followUp = func(StreamRequest) (*Control, error) { return complete("Once upon a time…"), nil }
	srv.speakBody = func(ctx context.Context, _ string) io.ReadCloser {
		mic.push(overReply(noise, 2))
		return &pacedBody{ctx: ctx, mic: mic, blocks: 100}
	}
	r := newRig(t, mic, srv)
	l := answer(r, mic, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeSpoken || len(tr.FollowUps) != 1 || tr.FollowUpEnd != EndBargeIn {
		t.Fatalf("turn %+v", tr)
	}
	if f := tr.FollowUps[0]; f.Outcome != OutcomeInterrupted || f.Err != nil || f.Message != "Once upon a time…" {
		t.Fatalf("follow-up %+v", f)
	}
	if out := r.replyOut(t, 22050); len(out.pcm) > 50*replyBlock || out.drained {
		t.Fatalf("played %d frames (drained %v)", len(out.pcm), out.drained)
	}
	if got := l.list(); !slices.Equal(got, []string{"follow_up"}) {
		t.Fatalf("listened %v; want no second window", got)
	}
	if got := fromStream(srv.callList()); !slices.Equal(got, []string{"stream", "transcribe", "command", "speak", "end"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestBargeInIgnoresOneChunkAndTheRepliesOwnLevel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during [][]int16
	}{
		{"a single chunk", overReply(noise, 1)},
		// The speaker is loud through the mic (and the fake detector calls it a wake word):
		// the baseline asks for twice its level, which "hey jarvis" here doesn't reach.
		{"at the reply's own level", overReply(square(4000), 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mic := oneWake()
			srv := newServer()
			srv.reply = longReply(mic, tc.during)
			r := newRig(t, mic, srv)
			r.p.Cfg.FollowUp.ListenSecs = 0
			stop := r.run(t)
			tr := r.turn(t)
			_ = stop()
			if tr.Outcome != OutcomeSpoken || tr.Err != nil || tr.FollowUpEnd != EndDisabled {
				t.Fatalf("turn %+v", tr)
			}
			if out := r.replyOut(t, 24000); len(out.pcm) != 100*replyBlock || !out.drained {
				t.Fatalf("played %d of %d frames (drained %v)", len(out.pcm), 100*replyBlock, out.drained)
			}
		})
	}
}
