package voice

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestRecordWithoutAPipelineIsUnavailable(t *testing.T) {
	if _, err := NewMic().Record(context.Background(), Take{Secs: 1}); !errors.Is(err, ErrMicUnavailable) {
		t.Fatalf("err = %v, want ErrMicUnavailable", err)
	}
	var none *Mic
	if _, err := none.Record(context.Background(), Take{Secs: 1}); !errors.Is(err, ErrMicUnavailable) {
		t.Fatalf("nil Mic: err = %v", err)
	}
}

// waitAttached waits for Run to offer the microphone.
func waitAttached(t *testing.T, m *Mic) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for m.attached.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the pipeline never attached its Mic")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTakePausesTheWakeWordAndListeningResumesAfter(t *testing.T) {
	mic := script()
	srv := newServer()
	r := newRig(t, mic, srv)
	r.p.Mic = NewMic()
	const preroll, reading = 3, 13 // 13 chunks: the 1 s take, and no loud audio left after it
	// The user reads loudly (the fake detector's wake level) once the cue has been said: had the
	// wake loop scored it, a turn would start.
	srv.onSpeak = func(string) { mic.push(rep(noise, preroll), rep(wake, reading)) }
	stop := r.run(t)
	defer stop()
	waitAttached(t, r.p.Mic)

	take := Take{Cue: "Read the prompt on your screen, starting now.", Preroll: preroll * ChunkSamples * time.Second / CaptureRate, Secs: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.det.mu.Lock()
	resets := r.det.resets
	r.det.mu.Unlock()
	pcm, err := r.p.Mic.Record(ctx, take)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != CaptureRate {
		t.Fatalf("recorded %d samples, want %d (1 s at 48 kHz)", len(pcm), CaptureRate)
	}
	if !slices.Equal(pcm[:ChunkSamples], wake[:ChunkSamples]) {
		t.Fatal("the take is not the audio after the preroll")
	}
	if got := srv.callList(); !slices.Equal(got, []string{"speak"}) {
		t.Fatalf("calls during the take = %v, want only the cue", got)
	}
	if srv.said[0] != take.Cue {
		t.Fatalf("said %q", srv.said)
	}

	// The wake loop starts again on new audio: what was read during the take is not scored.
	r.waitResets(t, resets+1)

	// Listening resumes: the wake word now starts a turn.
	srv.onSpeak = nil
	mic.push(rep(wake, 2), rep(speech, 5))
	if tr := r.turn(t); tr.Transcript != srv.text {
		t.Fatalf("turn after the take = %+v", tr)
	}
}

func TestTakeFailsWhenTheMicrophoneFails(t *testing.T) {
	mic := script()
	srv := newServer()
	r := newRig(t, mic, srv)
	r.p.Mic = NewMic()
	srv.onSpeak = func(string) { mic.mu.Lock(); mic.err = errors.New("unplugged"); mic.mu.Unlock() }
	stop := r.run(t)
	waitAttached(t, r.p.Mic)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.p.Mic.Record(ctx, Take{Cue: "go", Secs: 1}); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the microphone's failure", err)
	}
	if err := stop(); err == nil {
		t.Fatal("Run did not report the microphone")
	}
}

// Say speaks between turns (a timer's expiry) with the wake word paused, records nothing, and
// listening resumes on fresh audio.
func TestSayBetweenTurns(t *testing.T) {
	mic := script()
	srv := newServer()
	r := newRig(t, mic, srv)
	r.p.Mic = NewMic()
	stop := r.run(t)
	defer stop()
	if err := NewMic().Say(context.Background(), "x"); !errors.Is(err, ErrMicUnavailable) {
		t.Fatalf("say without a pipeline = %v", err)
	}
	waitAttached(t, r.p.Mic)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.p.Mic.Say(ctx, "Your pasta timer is done!"); err != nil {
		t.Fatal(err)
	}
	if got := srv.callList(); !slices.Equal(got, []string{"speak"}) || srv.said[0] != "Your pasta timer is done!" {
		t.Fatalf("calls = %v, said %q", got, srv.said)
	}
	mic.push(rep(wake, 2), rep(speech, 5))
	if tr := r.turn(t); tr.Transcript != srv.text {
		t.Fatalf("turn after saying = %+v", tr)
	}
}
