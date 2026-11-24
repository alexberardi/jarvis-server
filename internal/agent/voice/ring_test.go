package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

func peak(pcm []int16) int {
	m := 0
	for _, v := range pcm {
		m = max(m, int(v), -int(v))
	}
	return m
}

func ringRig(t *testing.T) (*rig, func() error) {
	t.Helper()
	r := newRig(t, script(), newServer())
	r.p.Mic = NewMic()
	stop := r.run(t)
	waitAttached(t, r.p.Mic)
	return r, stop
}

// Unanswered, a ring plays its bursts quiet to full and ends at the limit, saying nothing to
// the server; listening resumes after it.
func TestRingRampsThenTimesOut(t *testing.T) {
	r, stop := ringRig(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := r.p.Mic.Ring(ctx, Ring{Limit: 4 * ringCycle, Ramp: 2 * ringCycle})
	if err != nil || a != RingTimeout {
		t.Fatalf("ring = %v, %v; want timeout", a, err)
	}
	outs := r.spk.all()
	if len(outs) != 4 {
		t.Fatalf("%d bursts, want 4", len(outs))
	}
	full := peak(ringBurst)
	if full < 19400 { // 0.6 of 32767, less the fade's rounding
		t.Fatalf("full burst peak %d, want about 60%% of full scale", full)
	}
	for i, want := range []float64{RingFloor, (1 + RingFloor) / 2, 1, 1} {
		got := float64(peak(outs[i].pcm)) / float64(full)
		if math.Abs(got-want) > 0.01 || outs[i].rate != chimeRate || !outs[i].drained {
			t.Fatalf("burst %d: share %.3f (want %.3f), rate %d, drained %v", i, got, want, outs[i].rate, outs[i].drained)
		}
	}
	if calls := r.srv.callList(); len(calls) != 0 {
		t.Fatalf("server calls during the ring: %v", calls)
	}
}

// A control ends the ring in the gap after the burst it arrived during.
func TestRingEndsOnAControl(t *testing.T) {
	for _, want := range []RingAction{RingStop, RingSnooze} {
		r, stop := ringRig(t)
		ctl := make(chan RingAction, 1)
		ctl <- want
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		a, err := r.p.Mic.Ring(ctx, Ring{Control: ctl})
		cancel()
		if err != nil || a != want {
			t.Fatalf("ring = %v, %v; want %v", a, err, want)
		}
		if n := len(r.spk.all()); n != 1 {
			t.Fatalf("%v: %d bursts, want 1", want, n)
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	}
}

// The caller giving up stops the ring, and the wake word works again after it.
func TestRingStopsWhenTheCallerCancels(t *testing.T) {
	r, stop := ringRig(t)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.p.Mic.Ring(ctx, Ring{}); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.spk.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never rang")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	// The pipeline finishes the ring and listens again.
	if err := r.p.Mic.Say(context.Background(), "x"); err != nil { // queued behind the ring
		t.Fatal(err)
	}
	mic := r.p.Capture.(*fakeCapture)
	mic.push(rep(wake, 2), rep(speech, 5))
	if tr := r.turn(t); tr.Transcript != r.srv.text {
		t.Fatalf("turn after the ring = %+v", tr)
	}
}

func TestRingGain(t *testing.T) {
	if g := ringGain(0, 0); g != 1 {
		t.Fatalf("no ramp: %v", g)
	}
	n := int(DefaultRingRamp / ringCycle)
	if g := ringGain(0, DefaultRingRamp); g != RingFloor {
		t.Fatalf("first: %v", g)
	}
	if g := ringGain(n, DefaultRingRamp); g != 1 {
		t.Fatalf("after the ramp: %v", g)
	}
	if ringCycle < 1500*time.Millisecond || ringCycle > 2*time.Second {
		t.Fatalf("cycle %v: a 0.7 s burst and a ~1 s gap", ringCycle)
	}
}

// JARVIS_RING_WAV=out.wav writes the first bursts of a ring with a short ramp, gaps as
// silence, to listen to on a real speaker (quietly: set the volume first).
func TestRingWAV(t *testing.T) {
	path := os.Getenv("JARVIS_RING_WAV")
	if path == "" {
		t.Skip("JARVIS_RING_WAV not set")
	}
	gap := make([]int16, ringGapChunks*ChunkSamples*chimeRate/CaptureRate)
	var pcm []int16
	for i := range 6 {
		pcm = append(append(pcm, scaled(ringBurst, ringGain(i, 4*ringCycle))...), gap...)
	}
	b := make([]byte, 2*len(pcm))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	var out bytes.Buffer
	if err := audio.WritePCM16WAV(&out, b, 1, chimeRate); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
