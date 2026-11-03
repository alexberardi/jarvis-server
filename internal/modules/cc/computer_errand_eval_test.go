package cc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// TestComputerErrandLive plays #575's requests against a real model through the real turn
// pipeline, with computer control on and the desktop agent's browser tools offered: "use the
// computer to …" and "download my bank statement" must call run_errand first (not browser tools,
// and not just say it will); a one-shot "open wikipedia.org" stays on browser_open.
//
//	JARVIS_ERRAND_EVAL_LLM=http://127.0.0.1:39555 \
//	  go test -run TestComputerErrandLive -v -timeout 30m ./internal/modules/cc/
//
// The engine is a llama-server running the household's model with jarvisd's pinned template.
func TestComputerErrandLive(t *testing.T) {
	base := os.Getenv("JARVIS_ERRAND_EVAL_LLM")
	if base == "" {
		t.Skip("JARVIS_ERRAND_EVAL_LLM not set")
	}
	for _, c := range []struct {
		name, say, want, not string
	}{
		{"use_the_computer", "use the computer to find today's featured article on wikipedia.org and tell me what its lead image shows",
			"run_errand", ""},
		{"bank_statement", "Download my bank statement for September.", "run_errand", ""},
		{"open_site", "open wikipedia.org", "browser_open", "run_errand"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
			ve.eng.strict.Store(true) // jarvisd's pinned template (ID12)
			ve.eng.ctxLen = 32768
			ve.setComputerControl("on")
			ve.start("c1", evalBrowserTools)
			ve.eng.mu.Lock()
			ve.eng.respond = realEngine(t, base)
			ve.eng.mu.Unlock()
			b := ve.turn("/api/v0/voice/command", "c1", c.say, map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200).json()
			var trail []string
			for round := 0; b["stop_reason"] == "tool_calls" && round < 3; round++ {
				var results []map[string]any
				calls, _ := b["tool_calls"].([]any)
				for _, x := range calls {
					call := x.(map[string]any)
					results = append(results, map[string]any{"tool_call_id": call["id"],
						"output": map[string]any{"success": true, "message": "Done."}})
				}
				b = ve.turn("/api/v0/voice/command/continue", "c1", "", map[string]any{"tool_results": results}).want(200).json()
			}
			// Every call the model made, node and server tools alike, is in the requests' history.
			called := map[string]bool{}
			for _, r := range ve.eng.requests() {
				for _, m := range messagesOf(r) {
					tcs, _ := m["tool_calls"].([]any)
					for _, x := range tcs {
						fn, _ := x.(map[string]any)["function"].(map[string]any)
						name, _ := fn["name"].(string)
						if !called[name] {
							trail = append(trail, name)
						}
						called[name] = true
					}
				}
			}
			t.Logf("%q -> %s | stop=%v reply=%q", c.say, strings.Join(trail, " -> "), b["stop_reason"], b["assistant_message"])
			// The first call is the routing; what the model does after the eval's stub results is not.
			if len(trail) == 0 || trail[0] != c.want {
				t.Errorf("called %v, want %s first", trail, c.want)
			}
			if c.not != "" && called[c.not] {
				t.Errorf("called %v, want no %s", trail, c.not)
			}
		})
	}
}

// evalBrowserTools are the desktop agent's open and browser tools #575's turns chose between.
var evalBrowserTools = func() string {
	tool := func(name, desc string, props map[string]any, req ...string) map[string]any {
		if req == nil {
			req = []string{}
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": req}}}
	}
	str := map[string]any{"type": "string"}
	b, _ := json.Marshal([]any{
		tool("desktop_open", "Open an app on this computer.", map[string]any{"target": str}, "target"),
		tool("browser_open", "Open a web page in a new browser tab. Prefer this over desktop_open for websites.",
			map[string]any{"url": str}, "url"),
		tool("browser_read_page", "Read the text of the current browser tab.", map[string]any{}),
		tool("browser_find", "Find text on the current page.", map[string]any{"text": str}, "text"),
		tool("browser_fill", "Fill a field on the current page.", map[string]any{"field": str, "value": str}, "field", "value"),
	})
	return string(b)
}()
