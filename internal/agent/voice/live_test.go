package voice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// gatedCapture is a scripted mic whose reads wait until open is closed.
type gatedCapture struct {
	*fakeCapture
	open chan struct{}
}

func (g *gatedCapture) Read(buf []int16) error {
	<-g.open
	return g.fakeCapture.Read(buf)
}

func TestMeasureWithNoPipelineIsUnavailable(t *testing.T) {
	_, err := NewLive().Measure(context.Background(), time.Second)
	if !errors.Is(err, ErrNotListening) {
		t.Fatalf("err = %v", err)
	}
}

// waitMeasure retries Measure until Run has attached the pipeline.
func waitMeasure(t *testing.T, l *Live, d time.Duration) ([]float64, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rms, err := l.Measure(context.Background(), d)
		if !errors.Is(err, ErrNotListening) || time.Now().After(deadline) {
			return rms, err
		}
		time.Sleep(time.Millisecond)
	}
}

func waitLiveAttached(t *testing.T, l *Live) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		n := l.attached
		l.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the pipeline never attached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMeasureTakesTheRoomsRMSWithWakePaused(t *testing.T) {
	// The room during the window is loud enough to wake: it is measured, not answered.
	mic := &gatedCapture{fakeCapture: script(rep(wake, 13)), open: make(chan struct{})}
	r := newRig(t, mic, newServer())
	l := NewLive()
	r.p.Live = l
	stop := r.run(t)
	defer stop()
	var rms []float64
	var err error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); rms, err = waitMeasure(t, l, time.Second) }()
	time.Sleep(20 * time.Millisecond) // let the request reach the idle pipeline
	close(mic.open)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(rms) != 13 { // 1 s of 80 ms chunks, rounded
		t.Fatalf("%d chunks", len(rms))
	}
	for _, v := range rms {
		if v != 5000 {
			t.Fatalf("rms = %v", rms)
		}
	}
	r.noTurnBy(t, mic.fakeCapture, 20)
}

func TestMeasureEndsWithItsCaller(t *testing.T) {
	// A caller's deadline ends the wait (a turn in progress, or a mic that has gone quiet).
	mic := &gatedCapture{fakeCapture: script(), open: make(chan struct{})}
	r := newRig(t, mic, newServer())
	l := NewLive()
	r.p.Live = l
	stop := r.run(t)
	defer stop()
	defer close(mic.open)
	waitLiveAttached(t, l)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := l.Measure(ctx, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestTuneAppliesToTheRunningPipelineAndTheNextOne(t *testing.T) {
	l := NewLive()
	l.Tune(func(c *Config) { c.WakeThreshold = 0.99 }) // the fake detector scores a wake 0.95
	mic := script(rep(noise, 5))
	r := newRig(t, mic, newServer())
	r.p.Live = l
	stop := r.run(t)
	mic.push(rep(wake, 3), rep(speech, 12))
	r.noTurnBy(t, mic, 25)
	l.Tune(func(c *Config) { c.WakeThreshold = 0.5 })
	mic.push(rep(noise, 3), rep(wake, 3), rep(speech, 12))
	if tr := r.turn(t); tr.ConversationID == "" {
		t.Fatalf("turn = %+v", tr)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	// A pipeline started later (the mic reopened, voice switched back on) gets every tune.
	r2 := newRig(t, script(), newServer())
	r2.p.Live = l
	stop2 := r2.run(t)
	defer stop2()
	waitMeasure(t, l, 80*time.Millisecond)
	if r2.p.Cfg.WakeThreshold != 0.5 {
		t.Fatalf("threshold = %v", r2.p.Cfg.WakeThreshold)
	}
}
