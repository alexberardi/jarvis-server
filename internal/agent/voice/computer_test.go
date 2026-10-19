package voice

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
)

// fakeComputer is a computer.Activity that also logs its calls, in order, into the fake
// server's call list, so a test sees the pause land before anything reaches the server.
type fakeComputer struct {
	computer.Activity
	srv   *fakeServer
	mu    sync.Mutex
	calls []string
}

func (c *fakeComputer) add(s string) {
	c.mu.Lock()
	c.calls = append(c.calls, s)
	c.mu.Unlock()
	c.srv.log("computer:" + s)
}

func (c *fakeComputer) Pause()     { c.add("pause"); c.Activity.Pause() }
func (c *fakeComputer) Stop() bool { c.add("stop"); return c.Activity.Stop() }
func (c *fakeComputer) Set(k computer.Kind, text string) {
	c.add("set:" + string(k))
	c.Activity.Set(k, text)
}

func (c *fakeComputer) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// computerRig is one wake-word turn whose command transcribes as text, with the computer
// doing k ("Ordering the blue mug"). The turn's state is checked inside the server's reply.
func computerRig(t *testing.T, k computer.Kind, text string) (*rig, *fakeComputer) {
	t.Helper()
	srv := newServer()
	srv.text = text
	c := &fakeComputer{srv: srv}
	c.Activity.Set(k, "Ordering the blue mug")
	r := newRig(t, oneWake(), srv)
	r.p.Computer = c
	r.p.Cfg.FollowUp.ListenSecs = 0
	return r, c
}

// The wake word pauses a computer task before the chime, STT or the server see anything.
func TestWakeWordPausesControl(t *testing.T) {
	for _, k := range []computer.Kind{computer.Controlling, computer.Recording} {
		t.Run(string(k), func(t *testing.T) {
			r, c := computerRig(t, k, "What time is it?")
			var during computer.Kind
			reply := r.srv.reply
			r.srv.reply = func(ctx context.Context) (Reply, error) {
				during, _ = c.Status()
				return reply(ctx)
			}
			stop := r.run(t)
			r.turn(t)
			_ = stop()
			if during != computer.Paused {
				t.Fatalf("during the turn the computer was %q, want paused", during)
			}
			calls := r.srv.callList()
			if len(calls) == 0 || calls[0] != "computer:pause" {
				t.Fatalf("calls %v, want the pause first", calls)
			}
		})
	}
}

// "Stop" ends the task locally, before the server answers, and the turn still goes through.
func TestStopPhraseStops(t *testing.T) {
	r, c := computerRig(t, computer.Controlling, "Jarvis, stop.")
	stopped := 0
	c.OnStop(func() { stopped++ })
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if stopped != 1 {
		t.Fatalf("the run was stopped %d times, want 1", stopped)
	}
	if k, _ := c.Status(); k != computer.Idle {
		t.Fatalf("after the turn the computer is %q, want idle", k)
	}
	calls := r.srv.callList()
	if i, j := slices.Index(calls, "computer:stop"), slices.Index(calls, "stream"); i < 0 || j < 0 || i > j {
		t.Fatalf("calls %v, want the local stop before the stream", calls)
	}
	if r.srv.streamIn.VoiceCommand != "Jarvis, stop." || tr.Outcome != OutcomeSpoken {
		t.Fatalf("server got %q (outcome %q), want the stop sent as a normal turn", r.srv.streamIn.VoiceCommand, tr.Outcome)
	}
	if got, want := c.list(), []string{"pause", "stop"}; !slices.Equal(got, want) {
		t.Fatalf("computer calls %v, want %v", got, want)
	}
}

// Any other command resumes the task, as it was, once the turn is over; an idle computer is
// left alone.
func TestOtherTurnResumes(t *testing.T) {
	for _, tc := range []struct {
		kind computer.Kind
		text string
		want []string
	}{
		{computer.Controlling, "What's the weather?", []string{"pause", "set:controlling"}},
		{computer.Recording, "Keep going.", []string{"pause", "set:recording"}},
		{computer.Controlling, "Don't stop.", []string{"pause", "set:controlling"}},
		{computer.Idle, "Stop.", nil},
	} {
		t.Run(tc.text, func(t *testing.T) {
			r, c := computerRig(t, tc.kind, tc.text)
			stop := r.run(t)
			r.turn(t)
			_ = stop()
			if got := c.list(); !slices.Equal(got, tc.want) {
				t.Fatalf("computer calls %v, want %v", got, tc.want)
			}
			k, text := c.Status()
			if k != tc.kind || (k != computer.Idle && text != "Ordering the blue mug") {
				t.Fatalf("after the turn the computer is %q %q, want %q as before", k, text, tc.kind)
			}
		})
	}
}

// A task the tray stopped during the turn stays stopped.
func TestTurnDoesNotResumeAStoppedTask(t *testing.T) {
	r, c := computerRig(t, computer.Controlling, "What's the weather?")
	reply := r.srv.reply
	r.srv.reply = func(ctx context.Context) (Reply, error) {
		c.Activity.Stop() // the tray, the CLI, the server
		return reply(ctx)
	}
	stop := r.run(t)
	r.turn(t)
	_ = stop()
	if k, _ := c.Status(); k != computer.Idle {
		t.Fatalf("after the turn the computer is %q, want idle", k)
	}
}

func TestIsStopPhrase(t *testing.T) {
	for s, want := range map[string]bool{
		"stop": true, "Stop.": true, "Jarvis, stop!": true, "stop it": true, "Stop that.": true,
		"cancel": true, "Cancel that, please.": true, "That's enough.": true, "That’s enough": true,
		"abort": true, "quit": true, "Never mind.": true, "nevermind": true, "OK stop now": true,
		"don't stop": false, "keep going": false, "stop the music": false, "wait": false,
		"no": false, "hold on": false, "what's the weather": false, "": false,
	} {
		if got := IsStopPhrase(s); got != want {
			t.Errorf("IsStopPhrase(%q) = %v, want %v", s, got, want)
		}
	}
}
