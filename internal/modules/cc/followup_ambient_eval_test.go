package cc

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Ambient room speech in the follow-up window (go-pi-node.md §3.10): the cases in
// testdata/ambient_followups.json, each played as the first follow-up of its context's
// exchange through the real turn pipeline against a real model, with the gate on and the
// case's voice evidence. Same-voice cases carry no acoustic doubt, so they measure the model
// alone: the user talking to someone else in the room, a phone call, a remark.
//
//	JARVIS_AMBIENT_EVAL_LLM=http://127.0.0.1:28115 [JARVIS_AMBIENT_EVAL_OUT=/tmp/ambient.jsonl] \
//	  [JARVIS_AMBIENT_EVAL_ONLY=same_to_other] \
//	  go test -run TestFollowUpAmbientEvalLive -v -timeout 3h ./internal/modules/cc/
//
// The engine is an OpenAI-compatible llama-server running the household's model with
// jarvisd's pinned template (Qwen3.5-9B Q4_K_M).

type ambientCorpus struct {
	Contexts map[string][2]string `json:"contexts"`
	Cases    []ambientCase        `json:"cases"`
}

type ambientCase struct {
	Ctx   string `json:"ctx"`
	Want  string `json:"want"` // accept, reject, either
	Cat   string `json:"cat"`
	Voice string `json:"voice"` // same, other, far
	Text  string `json:"text"`
}

// ambientTools adds the contexts' lights, music and reminders to evalTools.
var ambientTools = strings.TrimSuffix(evalTools, "]") + `,` +
	`{"type":"function","function":{"name":"control_lights","description":"Turn lights on or off, or dim them, in a room.","parameters":{"type":"object","properties":{"room":{"type":"string"},"state":{"type":"string","enum":["on","off","dim"]}},"required":["room","state"]}}},` +
	`{"type":"function","function":{"name":"play_music","description":"Play, pause, skip or change the volume of music.","parameters":{"type":"object","properties":{"action":{"type":"string","enum":["play","pause","next","volume_up","volume_down"]},"query":{"type":"string"}},"required":["action"]}}},` +
	`{"type":"function","function":{"name":"set_reminder","description":"Set a reminder.","parameters":{"type":"object","properties":{"text":{"type":"string"},"when":{"type":"string"}},"required":["text","when"]}}}` +
	`]`

func loadAmbientCorpus(t *testing.T) ambientCorpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/ambient_followups.json")
	if err != nil {
		t.Fatal(err)
	}
	var c ambientCorpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The corpus itself stays well-formed: every case has a context, a verdict and a voice.
func TestAmbientCorpusIsWellFormed(t *testing.T) {
	c := loadAmbientCorpus(t)
	seen := map[string]bool{}
	for i, x := range c.Cases {
		if _, ok := c.Contexts[x.Ctx]; !ok {
			t.Errorf("case %d (%q): unknown context %q", i, x.Text, x.Ctx)
		}
		if x.Want != "accept" && x.Want != "reject" && x.Want != "either" {
			t.Errorf("case %d (%q): want %q", i, x.Text, x.Want)
		}
		if x.Voice != "same" && x.Voice != "other" && x.Voice != "far" {
			t.Errorf("case %d (%q): voice %q", i, x.Text, x.Voice)
		}
		k := x.Ctx + "|" + x.Text
		if seen[k] {
			t.Errorf("case %d: duplicate %q", i, x.Text)
		}
		seen[k] = true
	}
	if !json.Valid([]byte(ambientTools)) {
		t.Fatal("ambientTools is not valid JSON")
	}
}

// The evidence a voice label stands for: measured values from the acoustic evaluation
// (same speaker near ≈ 0.65, another voice ≈ 0.1; across the room ≈ −14 dB).
func ambientEvidence(voice string) (sim, level float64) {
	switch voice {
	case "other":
		return 0.10, 0
	case "far":
		return 0.10, -14
	}
	return 0.65, 0
}

type ambientOutcome struct {
	rejected, acoustic bool
	doubleChecked      bool // the not_for_me double-check pass ran
	reply              string
	calls              int
	ms                 int64
}

func runAmbientCase(t *testing.T, base string, ctx [2]string, c ambientCase) ambientOutcome {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.eng.strict.Store(true) // jarvisd's pinned Qwen3.5 template rejects mid-conversation system messages (ID12)
	ve.eng.ctxLen = 32768
	ve.set(settingFollowUpGate, true, settings.Scope{})
	cid := "c1"
	ve.start(cid, ambientTools)
	ve.stt.voices = []stt.Voice{{Vec: []float32{1, 0}, SpeechSecs: 3, ModelID: "m"}}
	ve.transcribe(cid, true)
	scriptWake(t, ve, cid, ctx)

	sim, level := ambientEvidence(c.Voice)
	ve.stt.voices = []stt.Voice{{Vec: []float32{float32(sim), float32(math.Sqrt(1 - sim*sim))}, SpeechSecs: 2, ModelID: "m"}}
	ve.transcribe(cid, true)
	before := len(ve.eng.requests())
	ve.eng.mu.Lock()
	ve.eng.respond = realEngine(t, base)
	ve.eng.mu.Unlock()
	start := time.Now()
	b := ve.turn("/api/v0/voice/command", cid, c.Text, map[string]any{"turn_source": "follow_up",
		"follow_up_iteration": 1, "level_vs_wake_db": level}).want(200).json()
	o := ambientOutcome{calls: len(ve.eng.requests()) - before}
	for i, r := range ve.eng.requests()[before:] {
		raw, _ := json.Marshal(r)
		if os.Getenv("JARVIS_AMBIENT_EVAL_DUMP") != "" {
			// The model's view of the follow-up: every message but the system prompt.
			for _, m := range messagesOf(r) {
				if m["role"] != "system" {
					t.Logf("request %d %s: %v", i+1, m["role"], m["content"])
				}
			}
		}
		if strings.Contains(string(raw), "NOT_FOR_ME_DOUBLE_CHECK] You dismissed") {
			o.doubleChecked = true
		}
	}
	// A tool call is the model taking the follow-up as addressed to it.
	for round := 0; b["stop_reason"] == "tool_calls" && round < 3; round++ {
		var results []map[string]any
		calls, _ := b["tool_calls"].([]any)
		var names []string
		for _, x := range calls {
			call, _ := x.(map[string]any)
			id, _ := call["id"].(string)
			if fn, ok := call["function"].(map[string]any); ok {
				n, _ := fn["name"].(string)
				names = append(names, n)
			}
			results = append(results, map[string]any{"tool_call_id": id, "output": map[string]any{"success": true, "message": "Done."}})
		}
		if o.reply == "" {
			o.reply = "[" + strings.Join(names, ",") + "] "
		}
		b = ve.turn("/api/v0/voice/command/continue", cid, "", map[string]any{"tool_results": results}).want(200).json()
	}
	o.ms = time.Since(start).Milliseconds()
	o.rejected = b["stop_reason"] == "not_for_me"
	o.acoustic = o.rejected && o.calls == 0
	if s, _ := b["assistant_message"].(string); s != "" {
		o.reply += s
	}
	return o
}

// scriptWake plays the woken exchange with a scripted reply. An action-shaped command
// answered in prose draws the must-call retry, so the reply is scripted for it too; the
// exchange must end with exactly that reply in the history, or every follow-up is judged
// against the wrong context.
func scriptWake(t *testing.T, ve *voiceEnv, cid string, ctx [2]string) {
	t.Helper()
	ve.eng.say(ctx[1])
	ve.eng.say(ctx[1])
	b := ve.turn("/api/v0/voice/command", cid, ctx[0], map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200).json()
	if b["assistant_message"] != ctx[1] {
		t.Fatalf("wake exchange replied %q, want %q", b["assistant_message"], ctx[1])
	}
	ve.eng.mu.Lock()
	ve.eng.script = nil // an unused retry reply must not answer a later call
	ve.eng.mu.Unlock()
}

func TestFollowUpAmbientEvalLive(t *testing.T) {
	base := os.Getenv("JARVIS_AMBIENT_EVAL_LLM")
	if base == "" {
		t.Skip("JARVIS_AMBIENT_EVAL_LLM not set")
	}
	corpus := loadAmbientCorpus(t)
	only := os.Getenv("JARVIS_AMBIENT_EVAL_ONLY")
	var out *os.File
	if p := os.Getenv("JARVIS_AMBIENT_EVAL_OUT"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		out = f
	}
	type tally struct{ n, right, rejected, acoustic int }
	by := map[string]*tally{}
	var wrong []string
	for i, c := range corpus.Cases {
		if only != "" && c.Cat != only && c.Want != only && c.Voice != only && c.Text != only {
			continue
		}
		var o ambientOutcome
		t.Run(fmt.Sprintf("%03d-%s", i, c.Cat), func(t *testing.T) {
			o = runAmbientCase(t, base, corpus.Contexts[c.Ctx], c)
		})
		right := c.Want == "either" || (c.Want == "reject") == o.rejected
		k := c.Want + " " + c.Cat
		if by[k] == nil {
			by[k] = &tally{}
		}
		x := by[k]
		x.n++
		if right {
			x.right++
		}
		if o.rejected {
			x.rejected++
		}
		if o.acoustic {
			x.acoustic++
		}
		verdict := "answered"
		if o.rejected {
			verdict = "not_for_me"
		}
		mark := " "
		if !right {
			mark = "✗"
			wrong = append(wrong, fmt.Sprintf("%s %-6s %-14s %-5s %q → %s %q", mark, c.Want, c.Cat, c.Voice, c.Text, verdict, o.reply))
		}
		t.Logf("%s %-6s %-14s %-5s calls=%d dc=%-5v %5dms %q → %s %q", mark, c.Want, c.Cat, c.Voice, o.calls, o.doubleChecked, o.ms, c.Text, verdict, o.reply)
		if out != nil {
			line, _ := json.Marshal(map[string]any{"i": i, "ctx": c.Ctx, "want": c.Want, "cat": c.Cat, "voice": c.Voice,
				"text": c.Text, "rejected": o.rejected, "acoustic": o.acoustic, "double_checked": o.doubleChecked, "reply": o.reply, "calls": o.calls, "ms": o.ms, "right": right})
			fmt.Fprintln(out, string(line))
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %3s %6s %9s %s\n", "want / category", "n", "right", "rejected", "(acoustic)")
	for _, k := range keys {
		x := by[k]
		fmt.Fprintf(&b, "%-24s %3d %6d %9d %d\n", k, x.n, x.right, x.rejected, x.acoustic)
	}
	b.WriteString("\nwrong:\n" + strings.Join(wrong, "\n"))
	t.Log("\n" + b.String())
}
