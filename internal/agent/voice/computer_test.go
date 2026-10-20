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
		// A recording's stop words don't end a task Jarvis is doing (#288): "That's all" is an
		// answer to "Anything else?".
		{computer.Controlling, "That's all.", []string{"pause", "set:controlling"}},
		{computer.Controlling, "That's all, thanks.", []string{"pause", "set:controlling"}},
		{computer.Controlling, "I'm done.", []string{"pause", "set:controlling"}},
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

// #288: "Stop recording." while a recording runs stops it locally at once, and the turn tells
// the server what it was and that it stopped here.
func TestStopRecordingPhraseStops(t *testing.T) {
	for _, said := range []string{"Stop recording.", "I'm done.", "Hey Jarvis, I'm done.", "That's it.", "Done."} {
		t.Run(said, func(t *testing.T) {
			r, c := computerRig(t, computer.Recording, said)
			stop := r.run(t)
			r.turn(t)
			_ = stop()
			if k, _ := c.Status(); k != computer.Idle {
				t.Fatalf("after the turn the computer is %q, want idle", k)
			}
			if got, want := c.list(), []string{"pause", "stop"}; !slices.Equal(got, want) {
				t.Fatalf("computer calls %v, want %v", got, want)
			}
			want := &ComputerActivity{Kind: "recording", Task: "Ordering the blue mug", Stopped: true}
			if got := r.srv.streamIn.Computer; got == nil || *got != *want {
				t.Fatalf("the turn said the computer was %+v, want %+v", got, want)
			}
		})
	}
}

// Every turn says what the computer is doing: a task the wake word held is reported as what it
// was, not as paused; an idle computer sends nothing.
func TestTurnReportsTheComputer(t *testing.T) {
	r, _ := computerRig(t, computer.Recording, "What time is it?")
	stop := r.run(t)
	r.turn(t)
	_ = stop()
	want := ComputerActivity{Kind: "recording", Task: "Ordering the blue mug"}
	if got := r.srv.streamIn.Computer; got == nil || *got != want {
		t.Fatalf("the turn said the computer was %+v, want %+v", got, want)
	}

	r, _ = computerRig(t, computer.Idle, "What time is it?")
	stop = r.run(t)
	r.turn(t)
	_ = stop()
	if got := r.srv.streamIn.Computer; got != nil {
		t.Fatalf("an idle computer sent %+v", got)
	}
}

// #288, as the user met it: "record a macro called test2" starts the recording during the turn,
// and the follow-up "I'm done." stops it locally and still goes to the server, saying so. A
// follow-up that isn't a stop phrase leaves it running and says it's recording.
func TestFollowUpStopsARecordingTheTurnStarted(t *testing.T) {
	for _, tc := range []struct {
		said string
		want ComputerActivity
		kind computer.Kind
	}{
		{"I'm done.", ComputerActivity{Kind: "recording", Task: "Watching what you do", Stopped: true}, computer.Idle},
		{"How long can it be?", ComputerActivity{Kind: "recording", Task: "Watching what you do"}, computer.Recording},
	} {
		t.Run(tc.said, func(t *testing.T) {
			srv := newServer()
			srv.text, srv.texts, srv.speakPCM = "Record a macro called test2.", []string{tc.said}, pcmOf(50)
			c := &fakeComputer{srv: srv}
			reply := srv.reply
			srv.reply = func(ctx context.Context) (Reply, error) {
				c.Activity.Set(computer.Recording, "Watching what you do") // start_recording ran
				return reply(ctx)
			}
			srv.followUp = func(StreamRequest) (*Control, error) { return complete("Okay."), nil }
			mic := oneWake()
			r := newRig(t, mic, srv)
			r.p.Computer = c
			answer(r, mic, utterance)
			stop := r.run(t)
			r.turn(t)
			_ = stop()

			if r.srv.streamIn.Computer != nil {
				t.Fatalf("the wake turn reported %+v; the computer was idle then", r.srv.streamIn.Computer)
			}
			if len(srv.commands) != 1 || srv.commands[0].VoiceCommand != tc.said {
				t.Fatalf("follow-ups sent %+v, want %q", srv.commands, tc.said)
			}
			if got := srv.commands[0].Computer; got == nil || *got != tc.want {
				t.Fatalf("the follow-up said the computer was %+v, want %+v", got, tc.want)
			}
			if k, _ := c.Status(); k != tc.kind {
				t.Fatalf("after the turn the computer is %q, want %q", k, tc.kind)
			}
			calls := srv.callList()
			if tc.kind == computer.Idle {
				if i, j := slices.Index(calls, "computer:stop"), slices.Index(calls, "command"); i < 0 || j < 0 || i > j {
					t.Fatalf("calls %v, want the local stop before the follow-up goes out", calls)
				}
			} else if slices.Contains(calls, "computer:stop") {
				t.Fatalf("calls %v: stopped on %q", calls, tc.said)
			}
		})
	}
}

// A control task the wake word held stays held through the follow-ups: a follow-up "Cancel
// that." is about the conversation (the timer just set), never the task, which resumes after
// the session. Only a recording ends on a follow-up (#288).
func TestFollowUpNeverStopsAControlTask(t *testing.T) {
	for _, said := range []string{"Cancel that.", "Never mind.", "Stop."} {
		t.Run(said, func(t *testing.T) {
			srv := newServer()
			srv.text, srv.texts, srv.speakPCM = "Set a timer for ten minutes.", []string{said}, pcmOf(50)
			c := &fakeComputer{srv: srv}
			c.Activity.Set(computer.Controlling, "Ordering the blue mug")
			srv.followUp = func(StreamRequest) (*Control, error) { return complete("Okay."), nil }
			mic := oneWake()
			r := newRig(t, mic, srv)
			r.p.Computer = c
			answer(r, mic, utterance)
			stop := r.run(t)
			r.turn(t)
			_ = stop()

			if got, want := c.list(), []string{"pause", "set:controlling"}; !slices.Equal(got, want) {
				t.Fatalf("computer calls %v, want %v", got, want)
			}
			want := ComputerActivity{Kind: "controlling", Task: "Ordering the blue mug"}
			if len(srv.commands) != 1 || srv.commands[0].Computer == nil || *srv.commands[0].Computer != want {
				t.Fatalf("follow-ups sent %+v, want computer_activity %+v", srv.commands, want)
			}
		})
	}
}
