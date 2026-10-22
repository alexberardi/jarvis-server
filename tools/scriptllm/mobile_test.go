package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// flowEnv reads the -e "NAME=value" pairs scripts/e2e's flow_env passes to the mobile flows.
func flowEnv(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/e2e")
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, m := range regexp.MustCompile(`-e "([A-Z_]+)=([^"$]*)"`).FindAllStringSubmatch(string(b), -1) {
		env[m[1]] = m[2]
	}
	return env
}

// The mobile flows' chat turns (docs/decisions/2026-10-18-e2e-mobile-runner.md): each message
// scripts/e2e passes gets its tool call from e2e/llm/mobile.json, and each reply it passes is
// the script's answer to that tool's result: done, or the node offline (as in a sandbox, where
// nothing runs the seeded node).
func TestMobileScriptAnswersTheFlowEnv(t *testing.T) {
	s, err := LoadScript("../../e2e/llm/mobile.json")
	if err != nil {
		t.Fatal(err)
	}
	env := flowEnv(t)
	get := func(k string) string {
		v := env[k]
		if v == "" {
			t.Fatalf("scripts/e2e passes no %s to the flows", k)
		}
		return v
	}
	if r := s.Find(Turn{Role: "user", Text: get("CHAT_MESSAGE")}); r == nil || r.Reply != get("CHAT_REPLY") {
		t.Errorf("CHAT_MESSAGE: %+v", r)
	}
	const offline = `{"success": false, "error": "the node is offline", "offline": true}`
	for _, c := range []struct {
		prefix, tool string
		args         map[string]any
	}{
		{"TIMER", "set_timer", map[string]any{"duration_seconds": 600.0, "label": "tea"}},
		{"LIST", "shopping_list", map[string]any{"action": "add", "items": []any{"oat milk"}}},
		{"DEVICE", "control_device", map[string]any{"device_name": "Kitchen Ceiling", "entity_id": "light.kitchen_ceiling", "action": "turn_on"}},
	} {
		r := s.Find(Turn{Role: "user", Text: get(c.prefix + "_MESSAGE")})
		if r == nil || len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != c.tool {
			t.Errorf("%s_MESSAGE: want one %s call, got %+v", c.prefix, c.tool, r)
			continue
		}
		var args map[string]any
		if err := json.Unmarshal(r.ToolCalls[0].Arguments, &args); err != nil {
			t.Fatal(err)
		}
		if a, b := mustJSON(t, args), mustJSON(t, c.args); a != b {
			t.Errorf("%s args: %s, want %s", c.tool, a, b)
		}
		for result, want := range map[string]string{
			`{"success": true}`:                get(c.prefix + "_REPLY"),
			offline:                            get(c.prefix + "_REPLY_OFFLINE"),
			`{"success": false, "error": "x"}`: "",
		} {
			r := s.Find(Turn{Role: "tool", Tool: c.tool, Text: result})
			switch {
			case r == nil:
				t.Errorf("%s: no rule for the result %s", c.tool, result)
			case want != "" && r.Reply != want:
				t.Errorf("%s after %s: %q, want %q", c.tool, result, r.Reply, want)
			case want == "" && (r.Reply == get(c.prefix+"_REPLY") || r.Reply == get(c.prefix+"_REPLY_OFFLINE")):
				t.Errorf("%s: another failure gets %q, which the flows read as done or offline", c.tool, r.Reply)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
