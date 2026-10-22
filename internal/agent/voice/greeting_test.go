package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// greetServer is command-center with /wake-response: texts in turn, then errors.
type greetServer struct {
	*fakeServer
	gmu   sync.Mutex
	texts []string
	asked int
	err   error
}

func (s *greetServer) WakeResponse(context.Context) (string, error) {
	s.gmu.Lock()
	defer s.gmu.Unlock()
	s.asked++
	if s.err != nil || len(s.texts) == 0 {
		return "", cmpErr(s.err, errors.New("no greeting"))
	}
	t := s.texts[0]
	s.texts = s.texts[1:]
	return t, nil
}

func cmpErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func (s *greetServer) askedN() int { s.gmu.Lock(); defer s.gmu.Unlock(); return s.asked }

// greetingPCM is what TTS renders for text: a level that says which greeting it is, padded
// with the silence TTS puts around speech.
func greetingPCM(level int16) []int16 {
	return slices.Concat(make([]int16, 300), slices.Repeat([]int16{level}, 2400), make([]int16, 300))
}

func pcmBytes(pcm []int16) []byte {
	b := make([]byte, 2*len(pcm))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func newGreetRig(t *testing.T, mic *fakeCapture, texts ...string) (*rig, *greetServer) {
	t.Helper()
	srv := &greetServer{fakeServer: newServer(), texts: texts}
	levels := map[string]int16{"Mhm?": 3000, "Go ahead.": 4000}
	srv.speakBody = func(_ context.Context, text string) io.ReadCloser {
		return io.NopCloser(bytes.NewReader(pcmBytes(greetingPCM(levels[text]))))
	}
	r := newRig(t, mic, srv.fakeServer)
	r.p.Server = srv
	r.p.Cfg.Greeting = true
	r.p.Cfg.Debounce = 0
	return r, srv
}

// waitGreeting waits until a greeting is cached and n /wake-response calls were made.
func waitGreeting(t *testing.T, p *Pipeline, srv *greetServer, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.greet.mu.Lock()
		ready := p.greet.next != nil && !p.greet.busy
		p.greet.mu.Unlock()
		if ready && srv.askedN() == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no greeting cached after %d /wake-response calls (%d made)", n, srv.askedN())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Row 6: the greeting fetched at start (and after each turn) plays on wake instead of the
// chime, trimmed of TTS's silence, and listening starts once it has played.
func TestWakeGreetingIsPrefetchedAndPlayedInsteadOfTheChime(t *testing.T) {
	mic := script(rep(noise, 40))
	r, srv := newGreetRig(t, mic, "Mhm?", "Go ahead.")
	stop := r.run(t)
	waitGreeting(t, r.p, srv, 1)

	mic.push(rep(wake, 3), rep(speech, 12))
	tr := r.turn(t)
	if tr.Outcome != OutcomeSpoken || tr.Transcript != "What time is it?" {
		t.Fatalf("turn %+v", tr)
	}
	// One prefetch per turn: the next greeting is fetched once this turn is over.
	waitGreeting(t, r.p, srv, 2)

	mic.push(rep(noise, 30), rep(wake, 3), rep(speech, 12))
	r.turn(t)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	outs := r.spk.all()
	if len(outs) != 4 {
		t.Fatalf("%d playbacks, want greeting + reply twice", len(outs))
	}
	for i, level := range []int16{3000, 4000} {
		g := outs[2*i]
		if g.rate != 22050 || !g.drained || !slices.Equal(g.pcm, slices.Repeat([]int16{level}, 2400)) {
			t.Fatalf("wake %d: played %d samples at %d Hz (drained %v), want the trimmed greeting", i+1, len(g.pcm), g.rate, g.drained)
		}
	}
	if got := srv.said; !slices.Equal(got, []string{"Mhm?", "Go ahead."}) {
		t.Fatalf("rendered %v", got)
	}
}

// Under a spoken greeting the mic hears a voice, which whisper would write down: with a
// pre-roll on, none of what was heard while it played is kept, and the first chunks after it
// (the greeting's last words, still in the mic's buffers) are zeroed. A chime-only wake keeps
// the pre-roll and the command heard right after the chime.
func TestWakeGreetingIsNotInTheCommand(t *testing.T) {
	for _, greeting := range []bool{true, false} {
		mic := script(rep(noise, 40))
		// The 8 command chunks after the chime must outlast the pipeline taking the pre-roll
		// (a slow CI runner stalled it past 2 ms a chunk), as in TestSpeechUnderTheChimeIsKept.
		mic.pace = 5 * time.Millisecond
		r, srv := newGreetRig(t, mic, "Mhm?")
		r.p.lockstep = false // a real microphone: it keeps capturing while the greeting plays
		r.p.Cfg.Endpointing.PreRollSecs = 1
		r.p.Cfg.Greeting = greeting
		var base int
		open, first := r.p.Playback, true
		r.p.Playback = func(rate, ch int) (device.Playback, error) {
			pb, err := open(rate, ch)
			if first { // the wake ack drains until the mic has captured 5 command chunks
				first = false
				pb = slowDrain{pb, func() {
					for mic.readCount() < base+3+5 {
						time.Sleep(time.Millisecond)
					}
				}}
			}
			return pb, err
		}
		stop := r.run(t)
		if greeting {
			waitGreeting(t, r.p, srv, 1)
		}
		for mic.readCount() < 40 { // the room, so the wake comes after the push
			time.Sleep(time.Millisecond)
		}
		mic.mu.Lock()
		base = mic.reads
		mic.chunks = append(mic.chunks, slices.Concat(rep(wake, 3), rep(speech, 13))...)
		mic.mu.Unlock()
		tr := r.turn(t)
		_ = stop()
		cmd, _ := decodeWAV(t, r.srv.cmdWAV)
		head := cmd[:min(len(cmd), int(tr.PreRollSecs*CaptureRate)+chimeTailChunks*ChunkSamples)]
		if greeting {
			if tr.PreRollSecs != 0 {
				t.Errorf("greeting: pre-roll %.2f s kept from under the greeting", tr.PreRollSecs)
			}
			if slices.ContainsFunc(head, func(v int16) bool { return v != 0 }) {
				t.Errorf("greeting: the first %d samples of the command are not zeroed", len(head))
			}
			continue
		}
		if tr.PreRollSecs == 0 {
			t.Error("chime: no pre-roll kept")
		}
		for i := len(head) - chimeTailChunks*ChunkSamples; i < len(head); i += ChunkSamples {
			if rms := RMS(head[i : i+ChunkSamples]); rms < 1000 {
				t.Errorf("chime: command chunk at %d after the pre-roll muted (RMS %.0f)", i, rms)
			}
		}
	}
}

// A miss (nothing cached: /wake-response or TTS failed) plays the chime, as before.
func TestWakeGreetingMissFallsBackToTheChime(t *testing.T) {
	mic := script(rep(noise, 40))
	r, srv := newGreetRig(t, mic)
	srv.err = errors.New("cc down")
	stop := r.run(t)
	deadline := time.Now().Add(5 * time.Second)
	for srv.askedN() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	mic.push(rep(wake, 3), rep(speech, 12))
	r.turn(t)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	outs := r.spk.all()
	if len(outs) != 2 || outs[0].rate != chimeRate || !slices.Equal(outs[0].pcm, wakeChime) {
		t.Fatalf("playbacks %d, first at %d Hz: want the wake chime", len(outs), outs[0].rate)
	}
}

// wake_ack_audio_enabled off (the chimes) fetches nothing and plays nothing; so does a
// pipeline without Greeting (the desktop's).
func TestNoGreetingWithoutWakeAck(t *testing.T) {
	for _, tc := range []struct {
		name             string
		chimes, greeting bool
		wantChime        bool
	}{{"wake ack off", false, true, false}, {"desktop", true, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			mic := script(rep(noise, 40), rep(wake, 3), rep(speech, 12))
			r, srv := newGreetRig(t, mic, "Mhm?")
			r.p.Cfg.Chimes, r.p.Cfg.Greeting = tc.chimes, tc.greeting
			stop := r.run(t)
			r.turn(t)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			if n := srv.askedN(); n != 0 {
				t.Fatalf("%d /wake-response calls", n)
			}
			outs := r.spk.all()
			chimed := len(outs) == 2 && slices.Equal(outs[0].pcm, wakeChime)
			if chimed != tc.wantChime || (!tc.wantChime && len(outs) != 1) {
				t.Fatalf("%d playbacks, chime %v", len(outs), chimed)
			}
		})
	}
}

func TestTrimSilence(t *testing.T) {
	in := slices.Concat([]int16{0, 100, -150}, []int16{300, 0, -400}, []int16{199, 0})
	if got := trimSilence(in, 1, 200); !slices.Equal(got, []int16{300, 0, -400}) {
		t.Fatalf("mono: %v", got)
	}
	// Stereo trims whole frames.
	st := []int16{0, 0, 0, 500, 7, 7, 0, 0}
	if got := trimSilence(st, 2, 200); !slices.Equal(got, []int16{0, 500}) {
		t.Fatalf("stereo: %v", got)
	}
	if got := trimSilence([]int16{1, 2, 3}, 1, 200); len(got) != 0 {
		t.Fatalf("all silence: %v", got)
	}
}
