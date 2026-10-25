package node

import (
	"testing"
	"time"
)

// A server on this computer is often started after the agent, so its retries stay seconds
// apart; one on the network keeps the long backoff (#585).
func TestBackoffCapDependsOnWhereTheServerIs(t *testing.T) {
	for _, c := range []struct {
		url  string
		want time.Duration
	}{
		{"http://127.0.0.1:7700", localRetryCap},
		{"http://localhost:7700", localRetryCap},
		{"http://LocalHost:7700", localRetryCap},
		{"http://[::1]:7700", localRetryCap},
		{"http://127.0.0.1", localRetryCap},
		{"http://192.168.1.10:7700", remoteRetryCap},
		{"https://jarvis.example:7700", remoteRetryCap},
		{"http://jarvis.local", remoteRetryCap},
		{"", remoteRetryCap},
		{"not a url", remoteRetryCap},
	} {
		if got := maxBackoff(c.url); got != c.want {
			t.Errorf("maxBackoff(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// The waits double from a second, and a local server's are never more than five seconds apart:
// starting it after the agent is picked up within seconds instead of up to a minute.
func TestBackoffSchedule(t *testing.T) {
	local := &Runner{Creds: Creds{ConfigURL: "http://127.0.0.1:7700"}}
	var got []time.Duration
	for n := 1; n <= 6; n++ {
		got = append(got, local.backoffAt(n))
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("local schedule %v, want %v", got, want)
		}
	}
	remote := &Runner{Creds: Creds{ConfigURL: "http://192.168.1.10:7700"}}
	if d := remote.backoffAt(6); d != 32*time.Second {
		t.Errorf("remote try 6 waits %v, want 32s", d)
	}
	if d := remote.backoffAt(9); d != time.Minute {
		t.Errorf("remote try 9 waits %v, want a minute", d)
	}
	if d := remote.backoffAt(40); d != time.Minute { // no overflow after a long outage
		t.Errorf("remote try 40 waits %v, want a minute", d)
	}
}

// The agent may be started before its server: Run says what it waits for (the status icon's
// "Waiting for the Jarvis server at …"), isn't Reached yet, and RetryNow makes it look again at
// once rather than after the backoff (the menu's "Retry now").
func TestRunSaysItIsWaitingAndRetriesWhenAsked(t *testing.T) {
	// Nothing listens here, so every try fails: all this test watches is the waiting itself.
	const url = "http://127.0.0.1:9"
	r := &Runner{Creds: Creds{ConfigURL: url, NodeID: "n1", NodeKey: "k"}, Tools: Multi{}, Log: quiet, Heartbeat: time.Hour}
	r.backoffFor = func(int) time.Duration { return time.Hour } // only RetryNow can wake it
	type waited struct {
		url  string
		err  error
		next time.Duration
	}
	waits := make(chan waited, 8)
	r.OnWaiting = func(url string, err error, next time.Duration) { waits <- waited{url, err, next} }
	cancel, done := run(r)
	defer cancel()

	next := func(what string) waited {
		t.Helper()
		select {
		case w := <-waits:
			return w
		case <-time.After(10 * time.Second):
			t.Fatal(what)
		}
		return waited{}
	}
	w := next("Run never said it was waiting for the server")
	if w.url != url || w.err == nil || w.next != time.Hour {
		t.Fatalf("OnWaiting(%q, %v, %v)", w.url, w.err, w.next)
	}
	if r.Reached() {
		t.Fatal("Reached() while nothing answers at the config URL")
	}
	// Without RetryNow the next try would be an hour away; it comes at once.
	r.RetryNow()
	next("RetryNow didn't make Run try the server again")
	cancel()
	<-done
}

// Once the server is started, the waiting Run picks it up: Reached, OnReached and a heartbeat.
func TestRunConnectsWhenTheServerComesUp(t *testing.T) {
	f := newFakeJarvisd(t, func(int) int { return 200 })
	f.down.Store(true) // the server isn't up yet
	r := f.runner()
	r.backoffFor = func(int) time.Duration { return 50 * time.Millisecond }
	waiting := make(chan struct{}, 8)
	reached := make(chan string, 1)
	r.OnWaiting = func(string, error, time.Duration) {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	r.OnReached = func(url string) { reached <- url }
	cancel, done := run(r)
	defer cancel()
	select {
	case <-waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("Run never said it was waiting for the server")
	}
	if r.Reached() {
		t.Fatal("Reached() while the server was down")
	}
	f.down.Store(false) // the user starts the server
	select {
	case u := <-reached:
		if u != f.srv.URL {
			t.Fatalf("OnReached(%q), want %q", u, f.srv.URL)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run didn't pick up the server when it came up")
	}
	if !r.Reached() {
		t.Fatal("not Reached() after the server answered")
	}
	select { // the first heartbeat goes out: the node is really up
	case <-f.beatSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("no heartbeat after the server came up")
	}
	cancel()
	<-done
	if r.Reached() {
		t.Fatal("still Reached() after Run returned")
	}
}

// RetryNow outside the wait (before Run, and once connected) is a no-op that doesn't block.
func TestRetryNowOutsideTheWait(t *testing.T) {
	r := &Runner{}
	r.RetryNow()
	r.RetryNow() // the channel holds one; the second is dropped
	f := newFakeJarvisd(t, func(int) int { return 200 })
	r2 := f.runner()
	cancel, done := run(r2)
	select {
	case <-f.beatSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("no heartbeat")
	}
	r2.RetryNow() // already connected: nothing to wake
	cancel()
	<-done
}
