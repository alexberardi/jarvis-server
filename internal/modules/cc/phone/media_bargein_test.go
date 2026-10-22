package phone

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
)

// Barge-in over a neutral Call (docs/decisions/2026-10-21-phone-barge-in.md): what any
// provider, SIP included, gets from the loop when the far end talks over the agent.

// bargeInCall answers a call, lets the disclosure play and leaves the first reply queued and
// unplayed (the agent is talking).
func bargeInCall(t *testing.T, e *env, heard []string, replies []string) (*fakeCall, string) {
	t.Helper()
	line := e.neutralLine()
	e.stt.heard = heard
	e.llm.replies = replies
	id := e.confirmed(t)
	c := nextCall(t, line)
	c.answer()
	waitFor(t, "disclosure", func() bool { return len(c.played()) == 1 })
	e.waitState(t, id, StateInCall)
	c.advance()
	c.say()
	waitFor(t, "first reply", func() bool { return len(c.played()) == 2 })
	return c, id
}

func lastUserMessage(e *env) string {
	e.llm.mu.Lock()
	defer e.llm.mu.Unlock()
	msgs := e.llm.streams[len(e.llm.streams)-1]
	return *msgs[len(msgs)-1].Content.Text
}

func TestBargeInFlushesTheReplyAndAnswersTheCaller(t *testing.T) {
	e := newEnv(t)
	c, id := bargeInCall(t, e,
		[]string{"Tony's, how can I help?", "Sorry, we're closed today."},
		[]string{"Hi, I'd like to order a large cheese pie for pickup at six tonight, please. ", "Oh, no problem, thanks anyway. "})

	// The caller talks over the reply: it is flushed and their words get the next turn.
	c.sayAt(4000, 15)
	waitFor(t, "reply to the interruption", func() bool { return len(c.played()) == 3 })
	if c.flushed() != 1 {
		t.Fatalf("flushes: %d", c.flushed())
	}
	if p := c.played()[2]; p.cue != "t2" {
		t.Fatalf("reply: %+v", p)
	}
	if got := lastUserMessage(e); !strings.HasPrefix(got, live.InterruptedNote+" Sorry, we're closed today.") {
		t.Fatalf("the model isn't told it was cut off: %q", got)
	}
	var turns []map[string]any
	waitFor(t, "second turn record", func() bool {
		return json.Unmarshal([]byte(e.session(id).TranscriptJSON), &turns) == nil && len(turns) == 2
	})
	if ev := turns[1]["events"].([]any); len(ev) == 0 || ev[0] != "barge_in" {
		t.Fatalf("turn 2 events: %v", ev)
	}
	if ev := turns[0]["events"].([]any); len(ev) != 0 {
		t.Fatalf("turn 1 events: %v", ev)
	}
}

// Our own audio coming back (echo, TTS bleed) is quieter than the far end's speech: it neither
// flushes the reply nor starts a turn.
func TestEchoDuringPlaybackIsNotABargeIn(t *testing.T) {
	e := newEnv(t)
	c, _ := bargeInCall(t, e,
		[]string{"Tony's, how can I help?", "echo"},
		[]string{"Hi, I'd like to order a large cheese pie for pickup. ", "Should never be said. "})
	c.sayAt(600, 100) // ~420 RMS: above the normal VAD threshold, below barge-in's
	c.sayAt(4000, 3)  // a click
	time.Sleep(300 * time.Millisecond)
	if c.flushed() != 0 || len(c.played()) != 2 {
		t.Fatalf("echo barged in: flushes=%d plays=%+v", c.flushed(), c.played())
	}
}

// The disclosure is a hard gate: speech over it never flushes it. It is still heard, and
// answered after the disclosure.
func TestNoBargeInDuringTheDisclosure(t *testing.T) {
	e := newEnv(t)
	line := e.neutralLine()
	e.stt.heard = []string{"Hello? Who is this?"}
	e.llm.replies = []string{"Hi, I'm calling to order a pizza. "}
	id := e.confirmed(t)
	c := nextCall(t, line)
	c.answer()
	waitFor(t, "disclosure", func() bool { return len(c.played()) == 1 })
	e.waitState(t, id, StateInCall)
	c.sayAt(4000, 15)
	waitFor(t, "reply", func() bool { return len(c.played()) == 2 })
	if c.flushed() != 0 {
		t.Fatal("the disclosure was flushed")
	}
	if got := lastUserMessage(e); strings.Contains(got, live.InterruptedNote) {
		t.Fatalf("nothing was cut off: %q", got)
	}
}

// On Twilio a barge-in is a clear message; the reply to the interruption follows it.
func TestTwilioBargeInClears(t *testing.T) {
	e := newEnv(t)
	e.stt.heard = []string{"Tony's, how can I help?", "Sorry, we're closed today."}
	e.llm.replies = []string{"Hi, I'd like to order a large cheese pie for pickup. ", "Oh, thanks anyway. "}
	id := e.confirmed(t)
	sim := e.answer(t, id)
	sim.untilMark("t0")
	e.waitState(t, id, StateInCall)
	sim.ack("t0")
	sim.speak()
	sim.untilMark("t1")
	if sim.clears != 0 {
		t.Fatal("cleared before anyone talked over the agent")
	}
	for i := 0; i < 15; i++ { // t1 is not acked: the agent is still talking
		sim.send(map[string]any{"event": "media", "media": map[string]any{"payload": frame(4000)}})
	}
	for i := 0; i < 45; i++ {
		sim.send(map[string]any{"event": "media", "media": map[string]any{"payload": frame(0)}})
	}
	if sim.untilMark("t2") == 0 || sim.clears != 1 {
		t.Fatalf("clears before the next reply: %d", sim.clears)
	}
}
