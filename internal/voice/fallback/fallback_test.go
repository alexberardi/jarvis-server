package fallback

import (
	"errors"
	"testing"
	"time"
)

func TestFallsBackOn(t *testing.T) {
	for code, want := range map[int]bool{0: true, 401: true, 403: true, 408: true, 429: true, 500: true, 502: true,
		503: true, 400: false, 413: false, 422: false} {
		if got := FallsBackOn(code); got != want {
			t.Errorf("FallsBackOn(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestGateBacksOffAndResets(t *testing.T) {
	now := time.Unix(1000, 0)
	g := &Gate{Base: 30 * time.Second, Max: 2 * time.Minute, Now: func() time.Time { return now }}
	if !g.TryRemote() {
		t.Fatal("a fresh gate tries the remote")
	}
	g.Failed(errors.New("boom"))
	if g.TryRemote() {
		t.Fatal("inside the backoff the remote is skipped")
	}
	now = now.Add(29 * time.Second)
	if g.TryRemote() {
		t.Fatal("still inside 30 s")
	}
	now = now.Add(time.Second)
	if !g.TryRemote() {
		t.Fatal("after the window the remote is tried again")
	}
	g.Failed(errors.New("boom again"))
	now = now.Add(59 * time.Second)
	if g.TryRemote() {
		t.Fatal("second failure doubles the window to 60 s")
	}
	now = now.Add(time.Second)
	g.Failed(errors.New("3"))
	g.Failed(errors.New("4"))
	if st := g.Status(); !st.BackoffUntil.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("window capped at Max: until %v, now %v", st.BackoffUntil, now)
	}
	now = now.Add(2 * time.Minute)
	g.Succeeded()
	g.Failed(errors.New("again"))
	if st := g.Status(); !st.BackoffUntil.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("a success resets the window: until %v", st.BackoffUntil)
	}
}

func TestGateStatus(t *testing.T) {
	now := time.Unix(1000, 0)
	g := &Gate{Now: func() time.Time { return now }}
	g.Served(PathRemote)
	g.Failed(errors.New("remote 503"))
	g.Served(PathLocalCPU)
	g.Served(PathLocalCPU)
	st := g.Status()
	if st.Fallbacks != 2 || st.LastPath != PathLocalCPU || st.LastRemoteError != "remote 503" ||
		!st.LastRemoteErrorAt.Equal(now) || !st.BackoffUntil.Equal(now.Add(DefaultBase)) {
		t.Fatalf("%+v", st)
	}
	g.Served(PathRemote)
	if st := g.Status(); st.LastPath != PathRemote || st.Fallbacks != 2 {
		t.Fatalf("%+v", st)
	}
}
