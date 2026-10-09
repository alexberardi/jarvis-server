package cc

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// Voice-route traces carry legacy-named per-step spans (latency_logger), read back through
// the admin trace route unchanged in shape.

// traceIDs waits for n stored traces of kind (they're written off the response path) and
// returns their ids, oldest first.
func (ve *voiceEnv) traceIDs(kind string, n int) []string {
	ve.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, err := ve.d.Read.Query(`SELECT id FROM cc_request_traces WHERE request_type = ? ORDER BY rowid`, kind)
		if err != nil {
			ve.t.Fatal(err)
		}
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) >= n {
			return ids
		}
		if time.Now().After(deadline) {
			ve.t.Fatalf("%d %s traces, want %d", len(ids), kind, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// traceSpans fetches a trace over the admin route and returns its spans by name (in stored
// order) plus the name list.
func (ve *voiceEnv) traceSpans(id string) ([]string, map[string]map[string]any) {
	ve.t.Helper()
	d := ve.do("GET", "/api/v0/admin/traces/"+id, nil, adminH()).want(200).json()
	for _, k := range []string{"id", "conversation_id", "request_type", "source", "status", "total_duration_ms", "spans", "error_message", "created_at"} {
		if _, ok := d[k]; !ok {
			ve.t.Fatalf("trace key %s missing: %v", k, d)
		}
	}
	var names []string
	by := map[string]map[string]any{}
	prevStart := -1.0
	for _, s := range d["spans"].([]any) {
		sp := s.(map[string]any)
		for _, k := range []string{"name", "service", "start_ms", "end_ms", "duration_ms", "status", "metadata"} {
			if _, ok := sp[k]; !ok {
				ve.t.Fatalf("span key %s missing: %v", k, sp)
			}
		}
		start, end, dur := sp["start_ms"].(float64), sp["end_ms"].(float64), sp["duration_ms"].(float64)
		if start < prevStart || end < start || dur < 0 || dur > end-start+0.11 || dur < end-start-0.11 {
			ve.t.Fatalf("span timing %v (prev start %v)", sp, prevStart)
		}
		prevStart = start
		name := sp["name"].(string)
		names = append(names, name)
		by[name] = sp
	}
	return names, by
}

// within fails unless inner lies inside outer.
func within(t *testing.T, by map[string]map[string]any, inner, outer string) {
	t.Helper()
	i, o := by[inner], by[outer]
	if i == nil || o == nil {
		t.Fatalf("missing %s or %s", inner, outer)
	}
	if i["start_ms"].(float64) < o["start_ms"].(float64) || i["end_ms"].(float64) > o["end_ms"].(float64) {
		t.Fatalf("%s %v not inside %s %v", inner, i, outer, o)
	}
}

func TestVoiceStreamTraceSpans(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c1", weatherTool)
	ve.eng.mu.Lock()
	ve.eng.delay = 20 * time.Millisecond
	ve.eng.mu.Unlock()
	ve.eng.say("It's three o'clock.")
	ve.turn("/api/v0/voice/command/stream", "c1", "what time is it", nil).want(200)

	// Warmup: legacy's span plus jarvisd's inference inside it.
	names, by := ve.traceSpans(ve.traceIDs("warmup", 1)[0])
	if got := strings.Join(names, ","); got != "auth_complete,warmup_conversation_with_tools,warmup_inference" {
		t.Fatalf("warmup spans %s", got)
	}
	within(t, by, "warmup_inference", "warmup_conversation_with_tools")
	if by["warmup_inference"]["service"] != "llm_proxy" {
		t.Fatalf("warmup_inference %v", by["warmup_inference"])
	}

	names, by = ve.traceSpans(ve.traceIDs("voice_command_stream", 1)[0])
	want := "auth_complete,process_voice_command_with_tools,cache_lookups,speaker_resolve,tool_execution_loop," +
		"llm_call_iter_1,audio_stream,tts_first_chunk,tts_stream_total,first_audio_byte"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("stream spans\n got %s\nwant %s", got, want)
	}
	within(t, by, "tool_execution_loop", "process_voice_command_with_tools")
	within(t, by, "llm_call_iter_1", "tool_execution_loop")
	within(t, by, "tts_first_chunk", "audio_stream")
	within(t, by, "tts_stream_total", "audio_stream")
	llmCall := by["llm_call_iter_1"]
	if llmCall["service"] != "llm_proxy" || llmCall["duration_ms"].(float64) < 20 {
		t.Fatalf("llm_call_iter_1 %v", llmCall)
	}
	if m := llmCall["metadata"].(map[string]any); m["prompt_tokens"] != 1.0 || m["completion_tokens"] != 1.0 || m["finish_reason"] != "stop" ||
		!strings.Contains(fmt.Sprint(m["output_preview"]), "three o'clock") {
		t.Fatalf("llm metadata %v", m)
	}
	if tts := by["tts_stream_total"]; tts["service"] != "tts" ||
		fmt.Sprint(tts["metadata"]) != "map[audio_bytes:21 text_chars:19]" {
		t.Fatalf("tts_stream_total %v", tts)
	}
	// The model-time split the benchmark needs: server overhead around the LLM call.
	if by["process_voice_command_with_tools"]["duration_ms"].(float64) < llmCall["duration_ms"].(float64) {
		t.Fatal("turn shorter than its LLM call")
	}
}

// A10 rehearsal: every turn failed in the LLM (stop_reason "error", nothing spoken) yet the
// Traces page and the dashboard showed each one green.
func TestVoiceTraceMarksLLMFailure(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c9", weatherTool)
	ve.eng.push(engineReply{status: 500, content: "Jinja Exception: System message must be at the beginning."})
	res := ve.turn("/api/v0/voice/command/stream", "c9", "what time is it", nil).want(202).json()
	if res["stop_reason"] != "error" {
		t.Fatalf("stop_reason %v", res["stop_reason"])
	}
	id := ve.traceIDs("voice_command_stream", 1)[0]
	d := ve.do("GET", "/api/v0/admin/traces/"+id, nil, adminH()).want(200).json()
	if d["status"] != "error" || !strings.Contains(fmt.Sprint(d["error_message"]), "System message must be at the beginning") {
		t.Fatalf("trace status %v error %v", d["status"], d["error_message"])
	}
	_, by := ve.traceSpans(id)
	if by["tool_execution_loop"]["status"] != "error" {
		t.Fatalf("tool_execution_loop %v", by["tool_execution_loop"])
	}
}

func TestVoiceBlockingTraceServerTool(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.stt.recognition = true
	alex := int64(7)
	ve.stt.speaker = &alex
	ve.start("c5", "[]")
	ve.transcribe("c5", true)
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_s1", "type": "function",
		"function": map[string]any{"name": "identify_speaker", "arguments": `{}`}}}})
	ve.eng.say("You're Alex.")
	ve.turn("/api/v0/voice/command", "c5", "who am i", nil).want(200)

	names, by := ve.traceSpans(ve.traceIDs("stt", 1)[0])
	if strings.Join(names, ",") != "stt_transcribe" || by["stt_transcribe"]["service"] != "whisper" {
		t.Fatalf("stt spans %v", by)
	}
	if m := by["stt_transcribe"]["metadata"].(map[string]any); m["audio_bytes"].(float64) <= 0 || m["speaker_audio_bytes"].(float64) <= 0 {
		t.Fatalf("stt metadata %v", m)
	}

	names, by = ve.traceSpans(ve.traceIDs("voice_command", 1)[0])
	want := "auth_complete,cache_get_tools,process_voice_command_with_tools,cache_lookups,speaker_resolve," +
		"tool_execution_loop,llm_call_iter_1,tool_exec_iter_1,server_tool_identify_speaker,llm_call_iter_2,build_response"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("blocking spans\n got %s\nwant %s", got, want)
	}
	if m := by["llm_call_iter_1"]["metadata"].(map[string]any); m["finish_reason"] != "tool_calls" {
		t.Fatalf("iter 1 metadata %v", m)
	}
	if m := by["tool_exec_iter_1"]["metadata"].(map[string]any); fmt.Sprint(m["tools"]) != "[identify_speaker]" {
		t.Fatalf("tool_exec metadata %v", m)
	}
	within(t, by, "server_tool_identify_speaker", "tool_exec_iter_1")
	within(t, by, "llm_call_iter_2", "tool_execution_loop")
}

func TestVoiceContinueStreamTraceSpans(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c2", weatherTool)
	ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {"city": "Denver"}}</tool_call>`)
	res := ve.turn("/api/v0/voice/command/stream", "c2", "weather in denver", nil).want(202).json()
	id := res["tool_calls"].([]any)[0].(map[string]any)["id"]
	ve.eng.say("It'll be 70 degrees. Enjoy the sun!")
	ve.do("POST", "/api/v0/voice/command/continue/stream", map[string]any{"conversation_id": "c2",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{"temp": 70}}}}, ve.node.h()).want(200)

	// The 202 tool-call turn: no audio spans.
	names, _ := ve.traceSpans(ve.traceIDs("voice_command_stream", 1)[0])
	if strings.Contains(strings.Join(names, ","), "audio") {
		t.Fatalf("202 turn has audio spans: %v", names)
	}

	names, by := ve.traceSpans(ve.traceIDs("voice_command_continue", 1)[0])
	got := strings.Join(names, ",")
	// Two sentences: a tts_first_chunk/tts_stream_total pair each.
	want := "auth_complete,continue_stream_dispatch,audio_stream,llm_stream_first_token,llm_stream_total," +
		"tts_first_chunk,tts_stream_total,first_audio_byte,tts_first_chunk,tts_stream_total"
	if got != want {
		t.Fatalf("continue-stream spans\n got %s\nwant %s", got, want)
	}
	within(t, by, "llm_stream_first_token", "llm_stream_total")
	within(t, by, "llm_stream_total", "audio_stream")
	if m := by["llm_stream_total"]["metadata"].(map[string]any); m["chars"] != 35.0 {
		t.Fatalf("llm_stream_total metadata %v", m)
	}
}

// TestOutputPreviewCaps keeps a trace's view of the model output short: the first
// outputPreviewRunes runes, marked when cut, so a 442-token tool call is visible without the
// trace row growing with every long answer.
func TestOutputPreviewCaps(t *testing.T) {
	if got := outputPreview("  short  "); got != "short" {
		t.Fatalf("short %q", got)
	}
	long := strings.Repeat("é", outputPreviewRunes+10)
	got := outputPreview(long)
	if []rune(got)[outputPreviewRunes] != '…' || len([]rune(got)) != outputPreviewRunes+1 {
		t.Fatalf("long preview has %d runes", len([]rune(got)))
	}
}
