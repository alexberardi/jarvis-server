package cc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// The Qwen providers' must-call retry ([MUST_CALL_RETRY], engine step i). Its job: when the user
// clearly asks for an action and a small model answers in prose without calling the tool, nudge
// it once. It must never nag after a tool already ran this turn, never more than once a turn,
// and never on a closing remark ("thanks").

func weatherCall(id string) engineReply {
	return engineReply{toolCalls: []map[string]any{{"id": id, "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": `{"city": "Boston"}`}}}}
}

// forceNags counts the requests that were a must-call retry (their last message is the nag).
func forceNags(reqs []map[string]any) int {
	n := 0
	for _, r := range reqs {
		msgs := messagesOf(r)
		if len(msgs) == 0 {
			continue
		}
		last := msgs[len(msgs)-1]
		if c, _ := last["content"].(string); last["role"] == "system" && strings.HasPrefix(c, nagMustCall) {
			n++
		}
	}
	return n
}

// clearScript drops what is left of the fake engine's script (its last reply would otherwise
// repeat into the next turn).
func clearScript(ce *chatEnv) {
	ce.eng.mu.Lock()
	ce.eng.script = nil
	ce.eng.mu.Unlock()
}

func forceEnv(t *testing.T) (*chatEnv, string) {
	t.Helper()
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	ce.pub.toolReply = `{"output": {"success": true, "message": "Sunny, 72 degrees in Boston."}}`
	cid := ce.warm("tok-7")
	if conv := ce.m.convs.get(cid); conv == nil || !conv.forceTools {
		t.Fatal("the provider should force tool calls for this test to mean anything")
	}
	return ce, cid
}

// After the tool ran this turn, a prose reply stands, even one that "claims" an action.
func TestForceRetryNotAfterToolSucceeded(t *testing.T) {
	ce, cid := forceEnv(t)
	before := len(ce.eng.requests())
	ce.eng.push(weatherCall("call_w1"))
	ce.eng.say("Gotcha, I'll check it again. It's sunny and 72 in Boston.")
	ce.eng.say("SHOULD NOT BE ASKED")
	frames := ce.chat("tok-7", map[string]any{"message": "check the weather in boston again", "conversation_id": cid})
	reqs := ce.eng.requests()[before:]
	if n := forceNags(reqs); n != 0 {
		t.Fatalf("%d must-call retries after the tool succeeded", n)
	}
	if len(reqs) != 2 || len(ce.pub.commands("tool_call")) != 1 {
		t.Fatalf("%d LLM requests, %d node calls", len(reqs), len(ce.pub.commands("tool_call")))
	}
	if f := lastFrame(t, frames); !strings.Contains(f["full_text"].(string), "sunny and 72") {
		t.Fatalf("done %v", f)
	}
}

// The original benefit: an action request answered in prose gets exactly one nudge, and the
// model then calls the tool.
func TestForceRetryNudgesOnceThenTool(t *testing.T) {
	ce, cid := forceEnv(t)
	before := len(ce.eng.requests())
	ce.eng.say("It's sunny in Boston.")
	ce.eng.push(weatherCall("call_w2"))
	ce.eng.say("Sunny, 72 degrees in Boston.")
	ce.chat("tok-7", map[string]any{"message": "check the weather in boston", "conversation_id": cid})
	reqs := ce.eng.requests()[before:]
	if n := forceNags(reqs); n != 1 || len(reqs) != 3 || len(ce.pub.commands("tool_call")) != 1 {
		t.Fatalf("%d nags, %d requests, %d node calls", n, len(reqs), len(ce.pub.commands("tool_call")))
	}
}

// A model that keeps answering in prose is nudged once, not twice.
func TestForceRetryAtMostOncePerTurn(t *testing.T) {
	ce, cid := forceEnv(t)
	before := len(ce.eng.requests())
	ce.eng.say("It's sunny in Boston.")
	ce.eng.say("Really, it's sunny in Boston.")
	ce.eng.say("SHOULD NOT BE ASKED")
	frames := ce.chat("tok-7", map[string]any{"message": "check the weather in boston", "conversation_id": cid})
	reqs := ce.eng.requests()[before:]
	if n := forceNags(reqs); n != 1 || len(reqs) != 2 {
		t.Fatalf("%d nags over %d requests", n, len(reqs))
	}
	if f := lastFrame(t, frames); f["full_text"] != "Really, it's sunny in Boston." {
		t.Fatalf("done %v", f)
	}
	// The next turn gets its own nudge.
	clearScript(ce)
	before = len(ce.eng.requests())
	ce.eng.say("Still sunny.")
	ce.eng.say("Still sunny, honestly.")
	ce.chat("tok-7", map[string]any{"message": "check the weather in boston", "conversation_id": cid})
	if n := forceNags(ce.eng.requests()[before:]); n != 1 {
		t.Fatalf("second turn: %d nags", n)
	}
}

// An offline node's failure is relayed, not forced into another call: the forcing used to
// re-arm on every continue, so a small model looped the node's tool until "Too many tool
// iterations".
func TestForceRetryNotAfterOfflineNode(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	ce.setOnline(false)
	r := ce.do("POST", "/api/v0/mobile/chat/warmup", map[string]any{"node_id": ce.node.id, "household_id": voiceHH,
		"client_tools": json.RawMessage(weatherTool)}, bearer("tok-7")).want(200).json()
	before := len(ce.eng.requests())
	ce.eng.push(weatherCall("call_o1"))
	ce.eng.say("Your kitchen node is offline right now, so I can't check the weather.")
	ce.eng.push(weatherCall("call_o2"))
	frames := ce.chat("tok-7", map[string]any{"message": "check the weather in boston", "conversation_id": r["conversation_id"]})
	reqs := ce.eng.requests()[before:]
	if n := forceNags(reqs); n != 0 || len(reqs) != 2 {
		t.Fatalf("%d nags over %d requests", n, len(reqs))
	}
	if f := lastFrame(t, frames); f["type"] != "done" || !strings.Contains(f["full_text"].(string), "offline") {
		t.Fatalf("done %v", f)
	}
}

// A closing remark is not an action request, even when the reply mentions one.
func TestForceRetryNotOnAcknowledgement(t *testing.T) {
	ce, cid := forceEnv(t)
	for _, msg := range []string{"thanks", "ok thanks!", "got it", "cool, thank you", "no thanks", "never mind"} {
		clearScript(ce)
		before := len(ce.eng.requests())
		ce.eng.say("You're welcome! I checked the weather for you earlier: sunny.")
		ce.chat("tok-7", map[string]any{"message": msg, "conversation_id": cid})
		if n := forceNags(ce.eng.requests()[before:]); n != 0 {
			t.Fatalf("%q: %d nags", msg, n)
		}
	}
	// Accepting an offer still counts: "yes" + a reply claiming an action it didn't take.
	clearScript(ce)
	before := len(ce.eng.requests())
	ce.eng.say("Great, I'll check the weather now.")
	ce.eng.push(weatherCall("call_y1"))
	ce.eng.say("Sunny.")
	ce.chat("tok-7", map[string]any{"message": "yes", "conversation_id": cid})
	if n := forceNags(ce.eng.requests()[before:]); n != 1 {
		t.Fatalf("offer accepted: %d nags", n)
	}
}

// A repeated call the dedupe guard turned back ("answer from the results above") counts as
// using the tool: answering from those results is what it asked for, so no nag follows.
func TestForceRetryNotAfterDedupeNudge(t *testing.T) {
	ce, cid := forceEnv(t)
	ce.eng.push(weatherCall("call_d1"))
	ce.eng.say("Sunny, 72 degrees in Boston.")
	ce.chat("tok-7", map[string]any{"message": "check the weather in boston", "conversation_id": cid})
	clearScript(ce)
	before := len(ce.eng.requests())
	ce.eng.push(weatherCall("call_d2")) // identical within the dedupe window → [TOOL_DEDUPE]
	ce.eng.say("Still sunny and 72 in Boston.")
	ce.eng.say("SHOULD NOT BE ASKED")
	frames := ce.chat("tok-7", map[string]any{"message": "check the weather in boston again", "conversation_id": cid})
	reqs := ce.eng.requests()[before:]
	if len(reqs) < 2 || !strings.Contains(fmtMessages(reqs[1]), nagDedupe) {
		t.Fatal("the repeated call was not turned back by the dedupe nudge")
	}
	if n := forceNags(reqs); n != 0 || len(reqs) != 2 {
		t.Fatalf("%d nags over %d requests", n, len(reqs))
	}
	if f := lastFrame(t, frames); f["full_text"] != "Still sunny and 72 in Boston." {
		t.Fatalf("done %v", f)
	}
}
