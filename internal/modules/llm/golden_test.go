package llm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

func queueJob() queue.Job { return queue.Job{} }

func wireFixture(t *testing.T) *pyjson.Object {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "fixtures", "golden", "llm", "wire.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*pyjson.Object)
}

func og(v any, k string) any {
	x, _ := v.(*pyjson.Object).Get(k)
	return x
}

// G10: upstream llama-server SSE → Jarvis frames, byte for byte as the legacy model service
// wrote them. The one intended difference: usage is always the three counts (legacy passed
// upstream's dict through, or {} when the engine sent none).
func TestStreamTranslationGolden(t *testing.T) {
	for _, row := range og(wireFixture(t), "stream").([]any) {
		name := og(row, "name").(string)
		var got []string
		res, err := parseStream(strings.NewReader(og(row, "upstream").(string)), func(d string) bool {
			got = append(got, frameJSON(Frame{Delta: d}))
			return true
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got = append(got, frameJSON(Frame{Done: true, Content: res.Content, Usage: res.Usage, ToolCalls: res.ToolCalls, FinishReason: res.FinishReason}))
		want := og(row, "frames").([]any)
		if len(got) != len(want) {
			t.Fatalf("%s: %d frames, want %d\n got %q\nwant %v", name, len(got), len(want), got, want)
		}
		for i := range got {
			w := want[i].(string)
			if i == len(got)-1 {
				w = normalizeUsage(t, w)
			}
			if got[i] != w {
				t.Errorf("%s frame %d:\n got %s\nwant %s", name, i, got[i], w)
			}
		}
	}
}

func normalizeUsage(t *testing.T, frame string) string {
	t.Helper()
	v, err := pyjson.Loads(frame)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*pyjson.Object)
	u, _ := o.Get("usage")
	n := pyjson.NewObject()
	for _, k := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		val, ok := u.(*pyjson.Object).Get(k)
		if !ok {
			val = 0
		}
		n.Set(k, val)
	}
	o.Set("usage", n)
	return pyjson.Dumps(o, true)
}

// G11: error_type_for_status, and the response envelope's defaults (tool call id/arguments,
// finish_reason) as create_openai_response filled them.
func TestEnvelopeGolden(t *testing.T) {
	g := wireFixture(t)
	et := og(g, "error_types").(*pyjson.Object)
	for _, k := range et.Keys() {
		var status int
		for _, c := range k {
			status = status*10 + int(c-'0')
		}
		if got, _ := et.Get(k); ErrorTypeForStatus(status) != got.(string) {
			t.Errorf("ErrorTypeForStatus(%d) = %s, want %s", status, ErrorTypeForStatus(status), got)
		}
	}
	for _, row := range og(g, "envelopes").([]any) {
		in, out := og(row, "input"), og(row, "output")
		ch := og(out, "choices").([]any)[0]
		var calls []wireToolCall
		if tcs, ok := in.(*pyjson.Object).Get("tool_calls"); ok {
			for _, tc := range tcs.([]any) {
				var w wireToolCall
				if id, ok := tc.(*pyjson.Object).Get("id"); ok {
					s := id.(string)
					w.ID = &s
				}
				fn := og(tc, "function")
				w.Function.Name = og(fn, "name").(string)
				if a, ok := fn.(*pyjson.Object).Get("arguments"); ok {
					w.Function.Arguments = []byte(pyjson.Compact(a))
				}
				calls = append(calls, w)
			}
		}
		fr, _ := in.(*pyjson.Object).Get("finish_reason")
		finish, _ := fr.(string)
		if finish == "" {
			finish = "stop"
			if len(calls) > 0 {
				finish = "tool_calls"
			}
		}
		if finish != og(ch, "finish_reason") {
			t.Errorf("finish_reason %s, want %v", finish, og(ch, "finish_reason"))
		}
		wantCalls, _ := og(og(ch, "message"), "tool_calls").([]any)
		for i, w := range calls {
			got := w.toolCall()
			want := wantCalls[i]
			if id := og(want, "id").(string); id == "<generated>" {
				if !strings.HasPrefix(got.ID, "call_") || len(got.ID) != 17 {
					t.Errorf("generated id %q", got.ID)
				}
			} else if got.ID != id {
				t.Errorf("id %q, want %q", got.ID, id)
			}
			if got.Function.Arguments != og(og(want, "function"), "arguments") || got.Type != "function" {
				t.Errorf("call %+v, want %v", got, pyjson.Dumps(want, true))
			}
		}
	}
}

// G12: the callback envelope's keys, in order.
func TestCallbackEnvelopeGolden(t *testing.T) {
	keys := og(og(wireFixture(t), "callback"), "keys").([]any)
	raw := string(mustMarshal(buildEnvelope(JobResult{JobID: "j1"})))
	v, _ := pyjson.Loads(raw)
	got := v.(*pyjson.Object).Keys()
	if len(got) != len(keys) {
		t.Fatalf("keys %v, want %v", got, keys)
	}
	for i := range got {
		if got[i] != keys[i] {
			t.Fatalf("keys %v, want %v", got, keys)
		}
	}
}
