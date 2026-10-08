package cc

import (
	"strings"
	"testing"

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
