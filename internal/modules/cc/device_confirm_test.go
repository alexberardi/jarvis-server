package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

func deviceRound(utterance string, calls ...string) ([]chatMsg, []toolResult) {
	msg := chatMsg{Role: "assistant"}
	var res []toolResult
	for i, args := range calls {
		id := "call_" + string(rune('a'+i))
		msg.ToolCalls = append(msg.ToolCalls, parse.ToolCall{ID: id, Type: "function",
			Function: parse.FunctionCall{Name: "control_device", Arguments: args}})
		res = append(res, toolResult{ToolCallID: id, Output: `{"success": true}`})
	}
	return []chatMsg{{Role: "user", Content: utterance}, msg}, res
}

func TestDeviceConfirmations(t *testing.T) {
	for _, c := range []struct {
		utterance string
		calls     []string
		want      string // "" = the model answers
	}{
		{"Turn on the living room lights", []string{`{"action":"turn_on","device_name":"Living room Lights"}`}, "Turned on the living room lights."},
		{"unlock the front door", []string{`{"action":"unlock","device_name":"Front Door Lock"}`}, "Unlocked the front door lock."},
		{"dim the lamp to 40", []string{`{"action":"set_brightness","device_name":"bedroom lamp","value":40}`}, "Set the bedroom lamp to 40 percent."},
		{"make the playroom lights blue", []string{`{"action":"set_color","device_name":"Play room lights","color":"Blue"}`}, "Turned the play room lights blue."},
		{"set the thermostat to 70", []string{`{"action":"set_temperature","device_name":"thermostat","value":"70"}`}, "Set the thermostat to 70 degrees."},
		{"turn off the kitchen and playroom lights", []string{`{"action":"turn_off","device_name":"Kitchen Light"}`, `{"action":"turn_off","device_name":"Play room lights"}`},
			"Turned off the kitchen light. Turned off the play room lights."},
		// The model answers: a question rides along, an rgb colour, an unknown value.
		{"turn on the lights and what's the time?", []string{`{"action":"turn_on","device_name":"lights"}`}, ""},
		{"make it red", []string{`{"action":"set_color","device_name":"lamp","color":"255,0,0"}`}, ""},
		{"set the heat to warm", []string{`{"action":"set_temperature","device_name":"thermostat","value":"warm"}`}, ""},
	} {
		msgs, res := deviceRound(c.utterance, c.calls...)
		got, ok := deviceConfirmation(msgs, res)
		if (c.want == "") == ok || got != c.want {
			t.Errorf("%q: got %q (%v), want %q", c.utterance, got, ok, c.want)
		}
	}
	// A failure is the model's to explain.
	msgs, res := deviceRound("turn on the lights", `{"action":"turn_on","device_name":"lights"}`)
	res[0].Output = `{"success": false, "error": "device offline"}`
	if _, ok := deviceConfirmation(msgs, res); ok {
		t.Fatal("confirmed a failed command")
	}
}

// End to end on the voice route: a working light command is confirmed without a second LLM
// call, ends the exchange, and the history keeps what was said.
func TestDeviceCommandIsConfirmedWithoutASecondModelCall(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("c-dev", weatherTool)
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_d1", "type": "function",
		"function": map[string]any{"name": "control_device", "arguments": `{"action":"turn_on","device_name":"Living room Lights"}`}}}})
	ve.turn("/api/v0/voice/command/stream", "c-dev", "turn on the living room lights", nil).want(202)
	before := len(ve.eng.requests())
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c-dev",
		"tool_results": []any{map[string]any{"tool_call_id": "call_d1", "output": map[string]any{"success": true}}}}, ve.node.h()).want(200).json()
	if b["assistant_message"] != "Turned on the living room lights." || b["end_of_exchange"] != true || b["stop_reason"] != "complete" {
		t.Fatalf("continue %v", b)
	}
	if n := len(ve.eng.requests()); n != before {
		t.Fatalf("%d LLM calls for a confirmed device command", n-before)
	}
	conv := ve.m.convs.get("c-dev")
	if last := conv.messages[len(conv.messages)-1]; last.Role != "assistant" || last.Content != "Turned on the living room lights." {
		t.Fatalf("history ends %+v", last)
	}
}
