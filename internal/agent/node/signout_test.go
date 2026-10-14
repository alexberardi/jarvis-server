package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeJarvisd answers discovery and the node routes; beat decides each heartbeat's status.
type fakeJarvisd struct {
	srv      *httptest.Server
	mu       sync.Mutex
	beat     func(n int) int
	beats    int
	beatSeen chan int
	signOuts int
}

func newFakeJarvisd(t *testing.T, beat func(n int) int) *fakeJarvisd {
	f := &fakeJarvisd{beat: beat, beatSeen: make(chan int, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]string{
				{"name": SvcCommandCenter, "url": f.srv.URL},
				{"name": SvcMQTT, "url": "mqtt://127.0.0.1:1"}, // nothing listens; paho retries quietly
			}})
		case "/api/v0/node/mqtt-credentials":
			_, _ = w.Write([]byte(`{"username":null,"password":null}`))
		case "/api/v0/admin/nodes/heartbeat":
			f.mu.Lock()
			f.beats++
			n := f.beats
			f.mu.Unlock()
			st := f.beat(n)
			w.WriteHeader(st)
			if st == 401 {
				_, _ = w.Write([]byte(`{"detail":"Invalid API Key"}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}
			f.beatSeen <- n
		case "/api/v0/node/sign-out":
			f.mu.Lock()
			f.signOuts++
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"message":"Signed out"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJarvisd) runner() *Runner {
	return &Runner{Creds: Creds{ConfigURL: f.srv.URL, NodeID: "n1", NodeKey: "k"}, Tools: Multi{}, Log: quiet, Heartbeat: time.Hour}
}

func run(r *Runner) (context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	return cancel, done
}

func TestRunStopsWhenTheFirstHeartbeatIsRefused(t *testing.T) {
	f := newFakeJarvisd(t, func(int) int { return 401 })
	cancel, done := run(f.runner())
	defer cancel()
	if err := <-done; !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Run = %v, want ErrSignedOut", err)
	}
}

// The "signed-out" nudge makes Run check its key at once; a refused key ends Run.
func TestSignedOutNudgeChecksTheKey(t *testing.T) {
	f := newFakeJarvisd(t, func(n int) int {
		if n == 1 {
			return 200
		}
		return 401
	})
	r := f.runner()
	cancel, done := run(r)
	defer cancel()
	<-f.beatSeen
	r.recheck()
	if err := <-done; !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Run = %v, want ErrSignedOut", err)
	}
}

// A nudge while the key still works (a spoofed message) changes nothing.
func TestSignedOutNudgeWithAWorkingKeyKeepsRunning(t *testing.T) {
	f := newFakeJarvisd(t, func(int) int { return 200 })
	r := f.runner()
	cancel, done := run(r)
	<-f.beatSeen
	r.recheck()
	<-f.beatSeen
	select {
	case err := <-done:
		t.Fatalf("Run returned %v after a nudge with a working key", err)
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A server error is not a sign-out: the credentials stay.
func TestServerErrorsAreNotASignOut(t *testing.T) {
	f := newFakeJarvisd(t, func(n int) int {
		if n == 1 {
			return 200
		}
		return 503
	})
	r := f.runner()
	cancel, done := run(r)
	<-f.beatSeen
	r.recheck()
	<-f.beatSeen
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v after a 503", err)
	}
}

func TestSignOutTellsJarvisd(t *testing.T) {
	f := newFakeJarvisd(t, func(int) int { return 200 })
	r := f.runner()
	cancel, done := run(r)
	defer cancel()
	<-f.beatSeen
	r.SignOut()
	if err := <-done; !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Run = %v, want ErrSignedOut", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.signOuts != 1 {
		t.Fatalf("sign-out calls = %d, want 1", f.signOuts)
	}
}

// Before jarvisd is reached, Sign out still works here.
func TestSignOutBeforeDiscovery(t *testing.T) {
	r := &Runner{Creds: Creds{ConfigURL: "http://127.0.0.1:1", NodeID: "n1", NodeKey: "k"}, Tools: Multi{}, Log: quiet}
	r.SignOut()
	cancel, done := run(r)
	defer cancel()
	if err := <-done; !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Run = %v, want ErrSignedOut", err)
	}
}

func TestRevoked(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&HTTPError{Status: 401}, true}, {&HTTPError{Status: 403}, true}, {&HTTPError{Status: 404}, false},
		{&HTTPError{Status: 503}, false}, {errors.New("dial tcp: refused"), false}, {nil, false},
	} {
		if got := Revoked(c.err); got != c.want {
			t.Errorf("Revoked(%v) = %v", c.err, got)
		}
	}
}

// OnConnected (the voice pipeline) runs until its ctx ends; a sign-out must end it, not wait on
// it forever (found running a real agent: it stayed up after "signed out").
func TestSignOutEndsOnConnected(t *testing.T) {
	f := newFakeJarvisd(t, func(n int) int {
		if n == 1 {
			return 200
		}
		return 401
	})
	r := f.runner()
	ended := make(chan struct{})
	r.OnConnected = func(ctx context.Context, _ *CC) {
		<-ctx.Done()
		close(ended)
	}
	cancel, done := run(r)
	defer cancel()
	<-f.beatSeen
	r.recheck()
	if err := <-done; !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Run = %v, want ErrSignedOut", err)
	}
	<-ended
}
