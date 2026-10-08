package cc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// A10b (rc1 rehearsal): the SDK marks date parameters with "format": "date-time", which CC
// reads to know where to resolve date keys. Sent to llama-server, the marker became a grammar
// that only admits ISO timestamps (Qwen 3.5's XML tool-call format constrains every argument
// to its schema), so the model could not write "today": it guessed a date instead, the node
// found no forecast for it, and the chat ended in "Too many tool iterations".
func TestLLMToolsDropDateTimeFormat(t *testing.T) {
	v, err := pyjson.Loads(`{"type":"function","function":{"name":"get_weather","description":"Weather.",` +
		`"parameters":{"type":"object","properties":{` +
		`"resolved_datetimes":{"type":"array","items":{"type":"string","format":"date-time"},"description":"Date keys"},` +
		`"when":{"type":"string","format":"date-time"},` +
		`"email":{"type":"string","format":"email"}},"required":["resolved_datetimes"]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	tool := v.(*pyjson.Object)
	before := prompts.CompactASCII(tool)

	out := llmTools([]prompts.Tool{tool})
	if len(out) != 1 {
		t.Fatalf("got %d tools", len(out))
	}
	got := string(out[0].Raw)
	want := `{"type":"function","function":{"name":"get_weather","description":"Weather.",` +
		`"parameters":{"type":"object","properties":{` +
		`"resolved_datetimes":{"type":"array","items":{"type":"string"},"description":"Date keys"},` +
		`"when":{"type":"string"},` +
		`"email":{"type":"string","format":"email"}},"required":["resolved_datetimes"]}}}`
	if got != want {
		t.Fatalf("engine tool:\n got %s\nwant %s", got, want)
	}

	// The cached schema keeps the marker: date injection and validation still find the param.
	if after := prompts.CompactASCII(tool); after != before {
		t.Fatalf("llmTools changed the cached tool:\n%s\n%s", before, after)
	}
	props := dates.SchemaProperties([]*pyjson.Object{tool}, "get_weather")
	rd, _ := props.Get("resolved_datetimes")
	if !dates.IsDatetimeArray(rd) {
		t.Fatal("cached schema lost its date-time marker")
	}
	if strings.Contains(got, "date-time") {
		t.Fatal("marker still sent to the engine")
	}
}

// A10b R9: the automation path (signal reactions) sends the engine the same stripped copy and
// resolves the chosen call's date parameters as the voice engine does: a key the model wrote is
// resolved, and an empty date takes the keys named in the rule's instruction (else today).
func TestPickActionStripsMarkerAndResolvesDates(t *testing.T) {
	var reply, sent string
	se := newSigEnv(t, func(m *Module) {
		m.LLM = fakeLLMFunc(func(req map[string]any) (string, string) {
			sent = req["tools"].(string)
			return "set_reminder", reply
		})
		m.HouseholdClock = fixedTZ("America/New_York")
	})
	now := time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)
	se.m.now = func() time.Time { return now }
	v, err := pyjson.Loads(`{"type":"function","function":{"name":"set_reminder","description":"Remind.",` +
		`"parameters":{"type":"object","properties":{"text":{"type":"string"},` +
		`"when":{"type":"string","format":"date-time"}},"required":["text"]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	tools := []prompts.Tool{v.(*pyjson.Object)}
	dctx := dates.New(now, "America/New_York")
	want := func(key string) string {
		r, _ := dctx.Resolve([]string{key})
		if len(r) == 0 {
			t.Fatalf("no resolution for %s", key)
		}
		return r[0]
	}

	for _, c := range []struct{ instruction, reply, when string }{
		{"Remind me to water the plants", `{"text": "water the plants", "when": "tomorrow"}`, want("tomorrow")},
		{"Remind me tomorrow to water the plants", `{"text": "water the plants"}`, want("tomorrow")},
		{"Remind me to water the plants", `{"text": "water the plants"}`, want("today")},
	} {
		reply = c.reply
		name, args, err := se.m.pickAction(context.Background(), sigHH, "I leave home", c.instruction, pyjson.NewObject(), tools)
		if err != nil || name != "set_reminder" {
			t.Fatal(name, err)
		}
		if args["when"] != c.when || args["text"] != "water the plants" {
			t.Fatalf("%q / %s: args %v, want when %s", c.instruction, c.reply, args, c.when)
		}
		if strings.Contains(sent, "date-time") || !strings.Contains(sent, `"when":{"type":"string"}`) {
			t.Fatalf("engine tools: %s", sent)
		}
	}
}
