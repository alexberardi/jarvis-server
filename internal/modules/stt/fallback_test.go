package stt

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
)

// remoteAt is an engine=remote stt label at base with the given fallback setting.
func remoteAt(base, mode string, timeout time.Duration) STTEngine {
	return ResolveFunc(func(context.Context, string) (Endpoint, error) {
		return Endpoint{BaseURL: base, Remote: true, API: APIOpenAI, APIKey: testKey, Model: "gpt-transcribe",
			Timeout: timeout, Fallback: mode}, nil
	})
}

func (f *fakeOpenAI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeWhisper) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.forms)
}

// cpuWhisper is the fallback engine: the env's fake whisper-server, counting resolves.
func cpuWhisper(e *env, resolves *atomic.Int32) STTEngine {
	return ResolveFunc(func(_ context.Context, label string) (Endpoint, error) {
		resolves.Add(1)
		if label != "stt" {
			return Endpoint{}, errors.New("wrong label " + label)
		}
		return Endpoint{BaseURL: e.w.srv.URL}, nil
	})
}

// fallbackEnv is a module whose stt label is remote at the fake (fallback=mode) with the env's
// fake whisper-server as the CPU fallback.
func fallbackEnv(t *testing.T, f *fakeOpenAI, mode string) (*env, *atomic.Int32) {
	var resolves atomic.Int32
	e := setup(t, func(m *Module) { m.Engine = remoteAt(f.srv.URL+"/v1", mode, 50*time.Millisecond) })
	e.m.FallbackEngine = cpuWhisper(e, &resolves)
	return e, &resolves
}

// A transport error, a timeout, 429, 5xx and 401/403 send the same clip to the local CPU
// whisper; the result says so and is whisper-server's own (two segments).
func TestFallbackOnProviderFailure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		delay  time.Duration
		down   bool
	}{
		{"timeout", 200, 2 * time.Second, false},
		{"connection refused", 0, 0, true},
		{"429", 429, 0, false},
		{"500", 500, 0, false},
		{"503", 503, 0, false},
		{"401", 401, 0, false},
		{"403", 403, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeOpenAI(t)
			f.status, f.response, f.delay = c.status, `{"error":"nope"}`, c.delay
			e, _ := fallbackEnv(t, f, fallback.ModeCPU)
			if c.down {
				dead := httptest.NewServer(nil)
				dead.Close()
				e.m.Engine = remoteAt(dead.URL+"/v1", fallback.ModeCPU, 50*time.Millisecond)
			}
			res, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != "turn off  the lights" || len(res.Segments) != 2 || res.Engine != fallback.PathLocalCPU {
				t.Fatalf("result %+v", res)
			}
			if e.w.count() != 1 {
				t.Fatalf("whisper-server asked %d times", e.w.count())
			}
			st := e.m.FallbackStatus()
			if st.Fallbacks != 1 || st.LastPath != fallback.PathLocalCPU || st.LastRemoteError == "" ||
				strings.Contains(st.LastRemoteError, testKey) || st.BackoffUntil.IsZero() || st.Mode != fallback.ModeCPU {
				t.Fatalf("status %+v", st)
			}
		})
	}
}

// A 400, 413 or 422 is about the clip: the local engine would be asked the same thing.
func TestNoFallbackOnRequestErrors(t *testing.T) {
	for _, code := range []int{400, 413, 422} {
		f := newFakeOpenAI(t)
		f.status, f.response = code, `bad clip`
		e, resolves := fallbackEnv(t, f, fallback.ModeCPU)
		_, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
		var ee *EngineError
		if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "Remote transcription failed") {
			t.Fatalf("%d: %v", code, err)
		}
		if e.w.count() != 0 || resolves.Load() != 0 {
			t.Fatalf("%d fell back", code)
		}
		if st := e.m.FallbackStatus(); !st.BackoffUntil.IsZero() || st.Fallbacks != 0 {
			t.Fatalf("%d opened the backoff: %+v", code, st)
		}
	}
}

// fallback=off fails as before, without touching the local engine.
func TestFallbackOff(t *testing.T) {
	f := newFakeOpenAI(t)
	f.status, f.response = 503, `overloaded`
	e, resolves := fallbackEnv(t, f, fallback.ModeOff)
	_, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
	var ee *EngineError
	if !errors.As(err, &ee) || ee.Msg != "Remote transcription failed: 503: overloaded" {
		t.Fatalf("%v", err)
	}
	if e.w.count() != 0 || resolves.Load() != 0 {
		t.Fatal("fallback=off fell back")
	}
	// The next request asks the remote again: no backoff without a fallback.
	e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
	if f.count() != 2 {
		t.Fatalf("remote asked %d times", f.count())
	}
}

// fallback=cpu with no local model is the remote error, saying why there was no fallback.
func TestFallbackWithoutLocalModel(t *testing.T) {
	f := newFakeOpenAI(t)
	f.status, f.response = 503, `overloaded`
	e, _ := fallbackEnv(t, f, fallback.ModeCPU)
	e.m.FallbackEngine = ResolveFunc(func(context.Context, string) (Endpoint, error) {
		return Endpoint{}, errors.New("not configured")
	})
	_, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
	var ee *EngineError
	if !errors.As(err, &ee) {
		t.Fatalf("%T %v", err, err)
	}
	if !strings.HasPrefix(ee.Msg, "Remote transcription failed: 503: overloaded") ||
		!strings.Contains(ee.Msg, "local CPU fallback unavailable: not configured") {
		t.Fatalf("message %q", ee.Msg)
	}
	// Inside the backoff, with no fallback to go to, the remote is still asked.
	if _, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{}); err == nil || f.count() != 2 {
		t.Fatalf("remote asked %d times: %v", f.count(), err)
	}
}

// After a failure, requests inside the window go straight to the CPU (the provider isn't
// asked); after it, the remote is probed again and a success makes it the path again.
func TestFallbackBackoff(t *testing.T) {
	f := newFakeOpenAI(t)
	f.status, f.response = 503, `overloaded`
	e, _ := fallbackEnv(t, f, fallback.ModeCPU)
	now := time.Unix(1_000_000, 0)
	e.m.now = func() time.Time { return now }
	wav := sineWAV(16000, 1, 330)

	if res, err := e.m.Transcribe(e.ctx, wav, TranscribeOptions{}); err != nil || res.Engine != fallback.PathLocalCPU {
		t.Fatalf("%+v %v", res, err)
	}
	now = now.Add(10 * time.Second)
	if res, err := e.m.Transcribe(e.ctx, wav, TranscribeOptions{}); err != nil || res.Engine != fallback.PathLocalCPU {
		t.Fatalf("%+v %v", res, err)
	}
	if f.count() != 1 || e.w.count() != 2 {
		t.Fatalf("inside the window: remote %d, cpu %d", f.count(), e.w.count())
	}

	// The provider recovers; after the window it is asked again and serves.
	f.mu.Lock()
	f.status, f.response = 0, ""
	f.mu.Unlock()
	now = now.Add(fallback.DefaultBase)
	res, err := e.m.Transcribe(e.ctx, wav, TranscribeOptions{})
	if err != nil || res.Engine != fallback.PathRemote || f.count() != 2 || e.w.count() != 2 {
		t.Fatalf("%+v %v remote %d cpu %d", res, err, f.count(), e.w.count())
	}
	st := e.m.FallbackStatus()
	if st.Fallbacks != 2 || st.LastPath != fallback.PathRemote || !st.BackoffUntil.IsZero() ||
		!strings.Contains(st.LastRemoteError, "503") {
		t.Fatalf("status %+v", st)
	}
}

// A local stt label never consults the fallback.
func TestLocalLabelReportsLocal(t *testing.T) {
	e := setup(t)
	res, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
	if err != nil || res.Engine != "local" {
		t.Fatalf("%+v %v", res, err)
	}
}
