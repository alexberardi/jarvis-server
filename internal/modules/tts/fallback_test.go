package tts

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
)

// kokoroPCM is what the fake Kokoro renders for text at the default gain (2.0): 100 samples
// of 0.25 per character, scaled.
func kokoroPCM(text string) []byte {
	s := make([]float32, 100*len(text))
	for i := range s {
		s[i] = 0.25
	}
	return pcm16(s, 2.0)
}

// fallbackSetup is a remote module with tts.fallback=cpu and a Kokoro model installed.
func fallbackSetup(t *testing.T, f *fakeProvider, extra map[string]any) *env {
	t.Helper()
	vals := map[string]any{"tts.fallback": "cpu", "tts.remote_timeout_ms": 100}
	for k, v := range extra {
		vals[k] = v
	}
	e := remoteSetup(t, f, vals)
	e.p.set(e.dir, true)
	return e
}

func kokoroCalls(e *env) []string {
	var out []string
	for _, s := range e.l.all() {
		out = append(out, s.calls()...)
	}
	return out
}

// The provider fails on sentence 2: sentences 2..n come from Kokoro, in the same 24 kHz stream.
func TestFallbackMidReply(t *testing.T) {
	f := newFakeProvider(t)
	f.reply = func(i int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if i == 1 {
			http.Error(w, "overloaded", 503)
			return
		}
		_, _ = w.Write(pcmFor(body["input"].(string)))
	}
	e := fallbackSetup(t, f, nil)
	st, err := e.m.Speak(context.Background(), "First one. Second one. Third one.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Format != remoteFormat() {
		t.Fatalf("format %+v", st.Format)
	}
	chunks, err := readAll(t, st)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Join([][]byte{pcmFor("First one."), kokoroPCM("Second one."), kokoroPCM("Third one.")}, nil)
	if !bytes.Equal(bytes.Join(chunks, nil), want) {
		t.Fatalf("audio: %d bytes, want %d", len(bytes.Join(chunks, nil)), len(want))
	}
	if n := len(f.requests()); n != 2 {
		t.Fatalf("%d remote requests, want 2", n)
	}
	if c := kokoroCalls(e); strings.Join(c, "|") != "Second one.|Third one." {
		t.Fatalf("kokoro rendered %q", c)
	}
	s, ok := e.m.FallbackStatus()
	if !ok || s.Mode != fallback.ModeCPU || s.Fallbacks != 1 || s.LastPath != fallback.PathLocalCPU ||
		!strings.Contains(s.LastRemoteError, "503") || s.BackoffUntil.IsZero() || s.Warning != "" {
		t.Fatalf("status %+v %v", s, ok)
	}
}

// Every error class that ends a remote reply falls back; 400/413/422 skip the sentence as before.
func TestFallbackErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     int
		hang     bool
		down     bool
		fallback bool
	}{
		{"401", 401, false, false, true}, {"403", 403, false, false, true}, {"429", 429, false, false, true},
		{"500", 500, false, false, true}, {"timeout", 0, true, false, true}, {"connection refused", 0, false, true, true},
		{"400", 400, false, false, false}, {"413", 413, false, false, false}, {"422", 422, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeProvider(t)
			f.reply = func(i int, w http.ResponseWriter, r *http.Request, body map[string]any) {
				if i == 0 {
					if tc.hang {
						<-r.Context().Done()
						return
					}
					http.Error(w, "nope", tc.code)
					return
				}
				_, _ = w.Write(pcmFor(body["input"].(string)))
			}
			extra := map[string]any{}
			if tc.down {
				dead := httptest.NewServer(nil)
				dead.Close()
				extra["tts.remote_url"] = dead.URL + "/v1"
			}
			e := fallbackSetup(t, f, extra)
			st, err := e.m.Speak(context.Background(), "First. Second.", SpeakOptions{})
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := readAll(t, st)
			if err != nil {
				t.Fatal(err)
			}
			got := bytes.Join(chunks, nil)
			if tc.fallback {
				if !bytes.Equal(got, append(kokoroPCM("First."), kokoroPCM("Second.")...)) {
					t.Fatalf("no fallback: %d bytes, kokoro %q", len(got), kokoroCalls(e))
				}
				return
			}
			if !bytes.Equal(got, pcmFor("Second.")) || len(kokoroCalls(e)) != 0 {
				t.Fatalf("%d fell back: %d bytes, kokoro %q", tc.code, len(got), kokoroCalls(e))
			}
		})
	}
}

// fallback=off: the reply ends with the remote error, as before.
func TestFallbackOffEndsReply(t *testing.T) {
	f := newFakeProvider(t)
	f.reply = func(i int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
		http.Error(w, "overloaded", 503)
	}
	e := fallbackSetup(t, f, map[string]any{"tts.fallback": "off"})
	st, _ := e.m.Speak(context.Background(), "First. Second.", SpeakOptions{})
	if _, err := readAll(t, st); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err %v", err)
	}
	if len(kokoroCalls(e)) != 0 || len(f.requests()) != 1 {
		t.Fatalf("kokoro %q, %d requests", kokoroCalls(e), len(f.requests()))
	}
	if s, ok := e.m.FallbackStatus(); !ok || s.Mode != fallback.ModeOff || s.Fallbacks != 0 || !s.BackoffUntil.IsZero() ||
		!strings.Contains(s.LastRemoteError, "503") {
		t.Fatalf("status %+v %v", s, ok)
	}
}

// fallback=cpu with no Kokoro model: the remote error, saying why there was no fallback; the
// status warns.
func TestFallbackWithoutKokoro(t *testing.T) {
	f := newFakeProvider(t)
	f.reply = func(i int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
		http.Error(w, "overloaded", 503)
	}
	e := fallbackSetup(t, f, nil)
	e.p.set("", false)
	if s, _ := e.m.FallbackStatus(); !strings.Contains(s.Warning, "Models page") {
		t.Fatalf("no warning: %+v", s)
	}
	st, _ := e.m.Speak(context.Background(), "First. Second.", SpeakOptions{})
	_, err := readAll(t, st)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "local fallback unavailable") {
		t.Fatalf("err %v", err)
	}
}

// After a failure, replies inside the window are Kokoro's without asking the provider; after
// it the provider is asked again.
func TestFallbackBackoff(t *testing.T) {
	f := newFakeProvider(t)
	var fail atomic.Bool
	fail.Store(true)
	f.reply = func(i int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if fail.Load() {
			http.Error(w, "overloaded", 503)
			return
		}
		_, _ = w.Write(pcmFor(body["input"].(string)))
	}
	e := fallbackSetup(t, f, nil)
	now := time.Unix(1_000_000, 0)
	e.m.fallback.Now = func() time.Time { return now }
	speak := func() []byte {
		t.Helper()
		st, err := e.m.Speak(context.Background(), "Hello there.", SpeakOptions{})
		if err != nil {
			t.Fatal(err)
		}
		chunks, err := readAll(t, st)
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Join(chunks, nil)
	}
	speak()
	now = now.Add(10 * time.Second)
	if got := speak(); !bytes.Equal(got, kokoroPCM("Hello there.")) || len(f.requests()) != 1 {
		t.Fatalf("inside the window: %d bytes, %d requests", len(got), len(f.requests()))
	}
	fail.Store(false)
	now = now.Add(fallback.DefaultBase)
	if got := speak(); !bytes.Equal(got, pcmFor("Hello there.")) || len(f.requests()) != 2 {
		t.Fatalf("after the window: %d bytes, %d requests", len(got), len(f.requests()))
	}
	if s, _ := e.m.FallbackStatus(); s.Fallbacks != 2 || s.LastPath != fallback.PathRemote || !s.BackoffUntil.IsZero() {
		t.Fatalf("status %+v", s)
	}
}

// Kokoro (the default provider) reports no fallback.
func TestNoFallbackStatusForKokoro(t *testing.T) {
	e := setup(t, &Module{})
	if _, ok := e.m.FallbackStatus(); ok {
		t.Fatal("kokoro has a fallback status")
	}
	d, _ := e.m.settings.Definition("tts.fallback")
	if d.Default != fallback.ModeOff || len(d.Options) != 2 || d.Options[1] != fallback.ModeCPU {
		t.Fatalf("tts.fallback %+v", d)
	}
}
