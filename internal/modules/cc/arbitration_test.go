package cc

import (
	"context"
	"sync"
	"testing"
	"time"
)

// arbClock is a hand-driven clock and timer for the wake arbiter: no test sleeps.
type arbClock struct {
	mu     sync.Mutex
	t      time.Time
	timers []chan time.Time
	asked  chan struct{} // one send per timer asked for
}

func newArbClock() *arbClock {
	return &arbClock{t: time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC), asked: make(chan struct{}, 16)}
}

func (c *arbClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *arbClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func (c *arbClock) after(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.timers = append(c.timers, ch)
	c.mu.Unlock()
	c.asked <- struct{}{}
	return ch
}

// fire expires every timer asked for so far.
func (c *arbClock) fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.timers {
		ch <- c.t
	}
	c.timers = nil
}

const testWindow = 1500 * time.Millisecond

func newTestArbiter() (*wakeArbiter, *arbClock) {
	c := newArbClock()
	return newWakeArbiter(c.now, c.after), c
}

func f64(v float64) *float64 { return &v }

// bidAsync runs a bid that is expected to wait, returning its verdict on a channel once the
// arbiter has asked for its timer.
func bidAsync(t *testing.T, a *wakeArbiter, c *arbClock, b wakeBid) <-chan arbVerdict {
	t.Helper()
	out := make(chan arbVerdict, 1)
	go func() { v, _ := a.bid(context.Background(), b, testWindow); out <- v }()
	select {
	case <-c.asked:
	case v := <-out:
		t.Fatalf("bid %s did not wait: %+v", b.ConversationID, v)
	case <-time.After(5 * time.Second):
		t.Fatalf("bid %s neither waited nor returned", b.ConversationID)
	}
	return out
}

func recv(t *testing.T, ch <-chan arbVerdict) arbVerdict {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no verdict")
	}
	return arbVerdict{}
}

func mustBid(t *testing.T, a *wakeArbiter, b wakeBid) arbVerdict {
	t.Helper()
	v, err := a.bid(context.Background(), b, testWindow)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWakeArbiterSingleNodeNeverWaits(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "pi", "c1")
	c.advance(2 * time.Second)
	v := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c1", Transcript: "what time is it"})
	if v.StandDown || v.Waited != 0 {
		t.Fatalf("a lone wake must answer at once: %+v", v)
	}
	if len(c.asked) != 0 {
		t.Fatal("a lone wake asked for a timer")
	}
}

func TestWakeArbiterTwoNodesOneWake(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "desktop", "c-desk")
	c.advance(400 * time.Millisecond)
	a.started("hh", "pi", "c-pi")
	c.advance(2 * time.Second) // recording + STT
	first := bidAsync(t, a, c, wakeBid{Household: "hh", Node: "desktop", ConversationID: "c-desk", Transcript: "What time is it?"})
	c.advance(300 * time.Millisecond)
	second := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c-pi", Transcript: "what time is it"})
	v1 := recv(t, first)
	if v1.StandDown || !second.StandDown {
		t.Fatalf("want the first to answer and the second to stand down: first=%+v second=%+v", v1, second)
	}
	if second.Winner.Node != "desktop" || v1.Members != 2 {
		t.Fatalf("winner/members: %+v / %+v", second, v1)
	}
}

func TestWakeArbiterWindowExpires(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "desktop", "c-desk")
	a.started("hh", "pi", "c-pi") // woke, but its command is late
	c.advance(2 * time.Second)
	first := bidAsync(t, a, c, wakeBid{Household: "hh", Node: "desktop", ConversationID: "c-desk", Transcript: "what time is it"})
	c.advance(testWindow)
	c.fire()
	if v := recv(t, first); v.StandDown {
		t.Fatalf("the only bidder when the window closed must answer: %+v", v)
	}
	// The late one is still the same wake (they woke together): it stands down.
	c.advance(time.Second)
	if v := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c-pi", Transcript: "what time is it"}); !v.StandDown {
		t.Fatalf("a late bid for a decided wake must stand down: %+v", v)
	}
}

func TestWakeArbiterWindowEdges(t *testing.T) {
	for _, tc := range []struct {
		gap    time.Duration
		merged bool
	}{{1400 * time.Millisecond, true}, {1600 * time.Millisecond, false}} {
		a, c := newTestArbiter()
		a.started("hh", "desktop", "c-desk")
		c.advance(2 * time.Second)
		v1 := mustBid(t, a, wakeBid{Household: "hh", Node: "desktop", ConversationID: "c-desk", Transcript: "turn on the lights"})
		if v1.StandDown {
			t.Fatalf("gap %v: first stood down", tc.gap)
		}
		c.advance(tc.gap)
		v2 := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c-pi", Transcript: "turn on the lights"})
		if v2.StandDown != tc.merged {
			t.Fatalf("gap %v: second stood down=%v, want %v", tc.gap, v2.StandDown, tc.merged)
		}
	}
}

func TestWakeArbiterPickOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		first, sec wakeBid
		want       string
	}{
		{"busy beats score", wakeBid{Node: "pi", Score: f64(0.99)}, wakeBid{Node: "desktop", Busy: true, Score: f64(0.5)}, "desktop"},
		{"score beats level", wakeBid{Node: "pi", Score: f64(0.6), Level: f64(-20)}, wakeBid{Node: "desktop", Score: f64(0.9), Level: f64(-40)}, "desktop"},
		{"level when scores tie", wakeBid{Node: "pi", Score: f64(0.9), Level: f64(-40)}, wakeBid{Node: "desktop", Score: f64(0.9), Level: f64(-25)}, "desktop"},
		{"level when one has no score", wakeBid{Node: "pi", Level: f64(-40)}, wakeBid{Node: "desktop", Score: f64(0.9), Level: f64(-25)}, "desktop"},
		{"arrival last", wakeBid{Node: "pi"}, wakeBid{Node: "desktop"}, "pi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c := newTestArbiter()
			a.started("hh", tc.first.Node, "c-"+tc.first.Node)
			a.started("hh", tc.sec.Node, "c-"+tc.sec.Node)
			c.advance(2 * time.Second)
			fb, sb := tc.first, tc.sec
			fb.Household, fb.ConversationID, fb.Transcript = "hh", "c-"+fb.Node, "what's the weather"
			sb.Household, sb.ConversationID, sb.Transcript = "hh", "c-"+sb.Node, "what's the weather"
			first := bidAsync(t, a, c, fb)
			c.advance(200 * time.Millisecond)
			v2 := mustBid(t, a, sb)
			v1 := recv(t, first)
			winner := v1.Winner.Node
			if v2.Winner.Node != winner {
				t.Fatalf("the two disagree on the winner: %+v / %+v", v1, v2)
			}
			if winner != tc.want {
				t.Fatalf("winner %s, want %s", winner, tc.want)
			}
			if (v1.StandDown == (fb.Node == tc.want)) || (v2.StandDown == (sb.Node == tc.want)) {
				t.Fatalf("stand-down flags wrong: %+v / %+v", v1, v2)
			}
		})
	}
}

func TestWakeArbiterBusyDecidesAtOnce(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "desktop", "c-desk")
	a.started("hh", "pi", "c-pi")
	c.advance(2 * time.Second)
	v := mustBid(t, a, wakeBid{Household: "hh", Node: "desktop", ConversationID: "c-desk", Transcript: "i'm done", Busy: true})
	if v.StandDown || len(c.asked) != 0 {
		t.Fatalf("a node mid-activity must answer without waiting: %+v", v)
	}
	if v := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c-pi", Transcript: "I'm done.", Score: f64(1)}); !v.StandDown {
		t.Fatalf("the other node must stand down: %+v", v)
	}
}

func TestWakeArbiterKeepsHouseholdsApart(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh1", "pi1", "c1")
	a.started("hh2", "pi2", "c2")
	c.advance(2 * time.Second)
	v1 := mustBid(t, a, wakeBid{Household: "hh1", Node: "pi1", ConversationID: "c1", Transcript: "what time is it"})
	v2 := mustBid(t, a, wakeBid{Household: "hh2", Node: "pi2", ConversationID: "c2", Transcript: "what time is it"})
	if v1.StandDown || v2.StandDown || len(c.asked) != 0 {
		t.Fatalf("different households are never merged nor held: %+v %+v", v1, v2)
	}
}

func TestWakeArbiterKeepsDifferentRequestsApart(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "kitchen", "c-k")
	a.started("hh", "office", "c-o")
	c.advance(2 * time.Second)
	first := bidAsync(t, a, c, wakeBid{Household: "hh", Node: "kitchen", ConversationID: "c-k", Transcript: "set a timer for ten minutes"})
	v2 := mustBid(t, a, wakeBid{Household: "hh", Node: "office", ConversationID: "c-o", Transcript: "what's on my calendar today"})
	v1 := recv(t, first) // the office turning in released the kitchen's wait
	if v1.StandDown || v2.StandDown {
		t.Fatalf("two people asking two things must both be answered: %+v %+v", v1, v2)
	}
}

func TestWakeArbiterSameNodeNeverMerged(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "pi", "c1")
	c.advance(time.Second)
	mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c1", Transcript: "what time is it"})
	a.started("hh", "pi", "c2")
	c.advance(200 * time.Millisecond)
	if v := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c2", Transcript: "what time is it"}); v.StandDown {
		t.Fatalf("a node asked twice answers twice: %+v", v)
	}
}

func TestWakeArbiterFollowUpOfOwnConversationWins(t *testing.T) {
	a, c := newTestArbiter()
	// The desktop is listening for a follow-up; the Pi wakes on "Hey Jarvis, stop" too.
	a.started("hh", "pi", "c-pi")
	c.advance(2 * time.Second)
	v := mustBid(t, a, wakeBid{Household: "hh", Node: "desktop", ConversationID: "c-desk", Transcript: "stop", Busy: true})
	if v.StandDown {
		t.Fatalf("the conversation's own node must answer its follow-up: %+v", v)
	}
	c.advance(300 * time.Millisecond)
	if v := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c-pi", Transcript: "Stop.", Score: f64(1)}); !v.StandDown || v.Winner.Node != "desktop" {
		t.Fatalf("the other node must stand down for it: %+v", v)
	}
}

func TestWakeArbiterOffWhenWindowZero(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "pi", "c1")
	a.started("hh", "desk", "c2")
	c.advance(time.Second)
	for _, b := range []wakeBid{{Household: "hh", Node: "pi", ConversationID: "c1", Transcript: "hi"}, {Household: "hh", Node: "desk", ConversationID: "c2", Transcript: "hi"}} {
		if v, _ := a.bid(context.Background(), b, 0); v.StandDown {
			t.Fatalf("window 0 turns arbitration off: %+v", v)
		}
	}
}

func TestWakeArbiterCancelledWaitLeaves(t *testing.T) {
	a, c := newTestArbiter()
	a.started("hh", "desktop", "c-desk")
	a.started("hh", "pi", "c-pi")
	c.advance(2 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := a.bid(ctx, wakeBid{Household: "hh", Node: "desktop", ConversationID: "c-desk", Transcript: "what time is it"}, testWindow)
		errc <- err
	}()
	<-c.asked
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("a cancelled wait must return the context's error")
	}
	// The node that left is not a winner the other defers to.
	if v := mustBid(t, a, wakeBid{Household: "hh", Node: "pi", ConversationID: "c-pi", Transcript: "what time is it"}); v.StandDown {
		t.Fatalf("the remaining node must answer: %+v", v)
	}
}

func TestSameRequest(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"What time is it?", "what time is it", true},
		{"Hey Jarvis, what time is it", "what time is it.", true},
		{"turn on the kitchen lights", "turn on the kitchen light", true},
		{"set a timer for 10 minutes", "set a timer for ten minutes", true},
		{"set a timer for ten minutes", "what's on my calendar today", false},
		{"turn on the lights", "turn off the lights", false},
		{"", "", false},
	} {
		if got := sameRequest(tc.a, tc.b); got != tc.want {
			t.Errorf("sameRequest(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
