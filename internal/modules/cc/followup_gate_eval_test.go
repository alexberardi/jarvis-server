package cc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The semantic half of the follow-up gate's evaluation (go-pi-node.md §3.10): every case the
// acoustic half measured (internal/agent/voice TestGateEval → cases.json) is played as a
// follow-up against a real model, through the real turn pipeline (prompt provider, hints,
// sentinel, double-check), twice: the status quo (gate off) and the gate with that case's
// measured voice similarity and level. The wake exchange is scripted; only the follow-up's
// model calls go to the engine.
//
//	JARVIS_GATE_EVAL=/tmp/gate JARVIS_GATE_EVAL_LLM=http://127.0.0.1:39555 \
//	  go test -run TestFollowUpGateEvalLive -v -timeout 3h ./internal/modules/cc/
//
// The engine is any OpenAI-compatible llama-server running the household's model (the eval
// used Qwen3.5-9B Q4_K_M with jarvisd's pinned template).

type evalCase struct {
	ID          string   `json:"id"`
	Category    string   `json:"category"`
	Context     string   `json:"context"`
	Text        string   `json:"text"`
	LevelDelta  *float64 `json:"level_delta_db"`
	VoiceSim    *float64 `json:"voice_similarity"`
	RecordedSec float64  `json:"recorded_s"`
}

var evalContexts = map[string][2]string{
	"weather":  {"What's the weather like today?", "It's 66 and overcast, with a chance of rain this afternoon."},
	"timer":    {"Set a timer for ten minutes.", "Timer set for ten minutes."},
	"time":     {"What time is it?", "It's 8:15 PM."},
	"shopping": {"Add milk to my shopping list.", "Added milk to your shopping list."},
	"austen":   {"Who wrote Pride and Prejudice?", "Jane Austen wrote Pride and Prejudice, published in 1813."},
	"calendar": {"What's on my calendar tomorrow?", "You have a dentist appointment at 3 PM tomorrow."},
}

// evalTools are a typical node's commands, so tool-shaped follow-ups have something to call.
const evalTools = `[` +
	`{"type":"function","function":{"name":"get_weather","description":"Get the weather forecast for a day.","parameters":{"type":"object","properties":{"day":{"type":"string","description":"today, tomorrow, a weekday or a date"}}}}},` +
	`{"type":"function","function":{"name":"set_timer","description":"Start or change a countdown timer.","parameters":{"type":"object","properties":{"minutes":{"type":"number"},"label":{"type":"string"}},"required":["minutes"]}}},` +
	`{"type":"function","function":{"name":"shopping_list","description":"Add items to, remove items from, or read the shopping list.","parameters":{"type":"object","properties":{"action":{"type":"string","enum":["add","remove","read"]},"items":{"type":"array","items":{"type":"string"}}},"required":["action"]}}},` +
	`{"type":"function","function":{"name":"calendar","description":"Read the user's calendar or add a reminder to an event.","parameters":{"type":"object","properties":{"action":{"type":"string","enum":["read","remind","move"]},"day":{"type":"string"},"detail":{"type":"string"}},"required":["action"]}}}` +
	`]`

func expectedReject(cat string) bool {
	return cat == "other_person" || cat == "tv" || cat == "same_unrelated"
}

// realEngine forwards a recorded request body to the real engine and returns its answer.
func realEngine(t *testing.T, base string) func(body map[string]any) (engineReply, bool) {
	return func(body map[string]any) (engineReply, bool) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		// An installed jarvisd's LLM API takes an app client (~/.config/jarvis-harness/installed.env).
		if id := os.Getenv("INSTALLED_APP_ID"); id != "" {
			req.Header.Set("X-Jarvis-App-Id", id)
			req.Header.Set("X-Jarvis-App-Key", os.Getenv("INSTALLED_APP_KEY"))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("engine: %v", err)
			return engineReply{status: 502, content: err.Error()}, true
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out struct {
			Choices []struct {
				Message struct {
					Content   string           `json:"content"`
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"message"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
		}
		if resp.StatusCode != 200 || json.Unmarshal(b, &out) != nil || len(out.Choices) == 0 {
			t.Errorf("engine %d: %s", resp.StatusCode, b)
			return engineReply{status: 502, content: string(b)}, true
		}
		c := out.Choices[0]
		return engineReply{content: c.Message.Content, toolCalls: c.Message.ToolCalls, finish: c.Finish}, true
	}
}

type evalOutcome struct {
	rejected bool
	acoustic bool // dropped before the model
	reply    string
	ms       float64
	calls    int
}

func runEvalCase(t *testing.T, base string, c evalCase, gate bool) evalOutcome {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.eng.strict.Store(true) // jarvisd's pinned Qwen3.5 template rejects mid-conversation system messages (ID12)
	ve.eng.ctxLen = 32768
	ve.set(settingFollowUpGate, gate, settings.Scope{})
	ctx := evalContexts[c.Context]
	cid := "c1"
	ve.start(cid, evalTools)
	ve.stt.voices = []stt.Voice{{Vec: []float32{1, 0}, SpeechSecs: 3, ModelID: "m"}}
	ve.transcribe(cid, true)
	scriptWake(t, ve, cid, ctx)

	// The follow-up, live.
	if c.VoiceSim != nil {
		s := *c.VoiceSim
		ve.stt.voices = []stt.Voice{{Vec: []float32{float32(s), float32(math.Sqrt(max(0, 1-s*s)))}, SpeechSecs: 2, ModelID: "m"}}
	}
	ve.transcribe(cid, true)
	before := len(ve.eng.requests())
	ve.eng.mu.Lock()
	ve.eng.respond = realEngine(t, base)
	ve.eng.mu.Unlock()
	extra := map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1}
	if c.LevelDelta != nil {
		extra["level_vs_wake_db"] = *c.LevelDelta
	}
	start := time.Now()
	b := ve.turn("/api/v0/voice/command", cid, c.Text, extra).want(200).json()
	o := evalOutcome{ms: float64(time.Since(start).Milliseconds()), calls: len(ve.eng.requests()) - before}
	// A tool call is the model taking the follow-up as addressed to it.
	for round := 0; b["stop_reason"] == "tool_calls" && round < 3; round++ {
		var results []map[string]any
		calls, _ := b["tool_calls"].([]any)
		for _, x := range calls {
			id, _ := x.(map[string]any)["id"].(string)
			results = append(results, map[string]any{"tool_call_id": id, "output": map[string]any{"success": true, "message": "Done."}})
		}
		b = ve.turn("/api/v0/voice/command/continue", cid, "", map[string]any{"tool_results": results}).want(200).json()
		if o.reply == "" {
			o.reply = "[tool]"
		}
	}
	o.rejected = b["stop_reason"] == "not_for_me"
	o.acoustic = o.rejected && o.calls == 0
	if s, _ := b["assistant_message"].(string); s != "" {
		o.reply = s
	}
	return o
}

func TestFollowUpGateEvalLive(t *testing.T) {
	dir, base := os.Getenv("JARVIS_GATE_EVAL"), os.Getenv("JARVIS_GATE_EVAL_LLM")
	if dir == "" || base == "" {
		t.Skip("JARVIS_GATE_EVAL and JARVIS_GATE_EVAL_LLM not set")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []evalCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if only := os.Getenv("JARVIS_GATE_EVAL_ONLY"); only != "" {
		var keep []evalCase
		for _, c := range cases {
			if strings.Contains(c.ID, only) {
				keep = append(keep, c)
			}
		}
		cases = keep
	}
	type row struct {
		c            evalCase
		before, gate evalOutcome
	}
	var rows []row
	out, _ := os.Create(filepath.Join(dir, "semantic.jsonl"))
	defer out.Close()
	for _, c := range cases {
		r := row{c: c}
		t.Run(c.ID, func(t *testing.T) {
			r.before = runEvalCase(t, base, c, false)
			r.gate = runEvalCase(t, base, c, true)
		})
		rows = append(rows, r)
		line, _ := json.Marshal(map[string]any{"id": c.ID, "category": c.Category, "text": c.Text,
			"voice_similarity": c.VoiceSim, "level_delta_db": c.LevelDelta,
			"before_rejected": r.before.rejected, "before_reply": r.before.reply, "before_ms": r.before.ms, "before_calls": r.before.calls,
			"gate_rejected": r.gate.rejected, "gate_acoustic": r.gate.acoustic, "gate_reply": r.gate.reply, "gate_ms": r.gate.ms, "gate_calls": r.gate.calls})
		fmt.Fprintln(out, string(line))
		t.Logf("%-22s before=%-6v gate=%-6v acoustic=%-5v | %s | before: %q | gate: %q", c.ID, r.before.rejected, r.gate.rejected, r.gate.acoustic, c.Text, r.before.reply, r.gate.reply)
	}
	// Per category: rejections before and with the gate.
	type tally struct{ n, before, gate, acoustic int }
	by := map[string]*tally{}
	for _, r := range rows {
		k := r.c.Category
		if by[k] == nil {
			by[k] = &tally{}
		}
		x := by[k]
		x.n++
		if r.before.rejected {
			x.before++
		}
		if r.gate.rejected {
			x.gate++
		}
		if r.gate.acoustic {
			x.acoustic++
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%-16s %3s %-8s %16s %16s %s\n", "category", "n", "want", "rejected before", "rejected gate", "(acoustic)")
	for _, k := range keys {
		x := by[k]
		want := "accept"
		if expectedReject(k) {
			want = "reject"
		}
		fmt.Fprintf(&b, "%-16s %3d %-8s %16d %16d %d\n", k, x.n, want, x.before, x.gate, x.acoustic)
	}
	t.Log("\n" + b.String())
}
