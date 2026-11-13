package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// A step cut off at iterMaxTokens is asked again once with lengthRetryMaxTokens, so a tool call
// whose arguments are long (a summary to type) isn't lost. Found live 2026-11-13: "summarize the
// Wikipedia article into the text edit window" read the page, then the typing call (the whole
// summary in its text) hit the 256-token cap and the user heard only "I'll type it".
func TestLengthCutRetriesWithRoom(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("lr1", searchTools)
	ve.eng.push(engineReply{content: "The intro gives a solid summary, so I'll type it", finish: "length"})
	ve.eng.push(nativeCall("call_t1", "web_lookup", `{"query":"`+strings.Repeat("long summary ", 200)+`"}`))
	b := ve.turn("/api/v0/voice/command", "lr1", "look up x", nil).want(200).json()
	calls, _ := b["tool_calls"].([]any)
	if b["stop_reason"] != "tool_calls" || len(calls) != 1 {
		t.Fatalf("the retried call was lost: %v", b)
	}
	reqs := ve.eng.requests()
	last := reqs[len(reqs)-1]
	if last["max_tokens"] != float64(lengthRetryMaxTokens) {
		t.Fatalf("retry max_tokens %v, want %d", last["max_tokens"], lengthRetryMaxTokens)
	}
	if prev := reqs[len(reqs)-2]; prev["max_tokens"] != float64(iterMaxTokens) {
		t.Fatalf("first try max_tokens %v", prev["max_tokens"])
	}
}

// Only once per step: a second cut-off answer is used as it is.
func TestLengthCutRetriesOnce(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("lr2", searchTools)
	ve.eng.push(engineReply{content: "Part one", finish: "length"})
	ve.eng.push(engineReply{content: "Part one, longer", finish: "length"})
	ve.eng.push(engineReply{content: "never asked"})
	n := len(ve.eng.requests())
	b := ve.turn("/api/v0/voice/command", "lr2", "tell me a long story", nil).want(200).json()
	if got := len(ve.eng.requests()) - n; got != 2 {
		t.Fatalf("%d model calls, want 2: %v", got, b)
	}
	if msg, _ := b["assistant_message"].(string); !strings.HasPrefix(msg, "Part one, longer") {
		t.Fatalf("reply %q", msg)
	}
}

// A summary starts with room: its answer runs past iterMaxTokens, and the cut first try cost
// ~3 s on every page summary (2026-11-13).
func TestSummaryStartsWithRoom(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("lr3", searchTools)
	ve.eng.say("A summary.")
	ve.turn("/api/v0/voice/command", "lr3", "summarize this page", nil).want(200)
	reqs := ve.eng.requests()
	if got := reqs[len(reqs)-1]["max_tokens"]; got != float64(lengthRetryMaxTokens) {
		t.Fatalf("summary max_tokens %v, want %d", got, lengthRetryMaxTokens)
	}
	ve.start("lr4", searchTools)
	ve.eng.say("It's sunny.")
	ve.turn("/api/v0/voice/command", "lr4", "what's the weather", nil).want(200)
	reqs = ve.eng.requests()
	if got := reqs[len(reqs)-1]["max_tokens"]; got != float64(iterMaxTokens) {
		t.Fatalf("other turn max_tokens %v, want %d", got, iterMaxTokens)
	}
}
