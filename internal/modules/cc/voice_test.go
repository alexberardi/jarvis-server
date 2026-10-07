package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// weatherTool is a client tool with a date-time array param and keywords.
const weatherTool = `[{"type":"function","function":{"name":"get_weather","description":"Get the weather forecast.",` +
	`"parameters":{"type":"object","properties":{"city":{"type":"string","description":"City"},` +
	`"resolved_datetimes":{"type":"array","items":{"type":"string","format":"date-time"},"description":"Dates"}},` +
	`"required":["resolved_datetimes"]}},"keywords":["weather","forecast"],"allow_direct_answer":false}]`

func (ve *voiceEnv) transcribe(cid string, speakerAudio bool) map[string]any {
	ve.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "cmd.wav")
	_, _ = fw.Write(stt.EncodeWAV(make([]float32, 16000)))
	if speakerAudio {
		sw, _ := mw.CreateFormFile("speaker_audio", "spk.wav")
		_, _ = sw.Write(stt.EncodeWAV(make([]float32, 48000)))
	}
	_ = mw.WriteField("conversation_id", cid)
	_ = mw.WriteField("language", "en")
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ve.srv.URL+"/api/v0/media/whisper/transcribe", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", ve.node.id+":"+ve.node.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		ve.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 200 {
		ve.t.Fatalf("transcribe %d: %v", res.StatusCode, out)
	}
	return out
}

func (ve *voiceEnv) transcripts() []map[string]any {
	ve.t.Helper()
	rows, err := ve.d.Read.Query(`SELECT user_id, conversation_id, user_message, COALESCE(assistant_message,''), COALESCE(tool_calls_json,'') FROM cc_conversation_transcripts ORDER BY id`)
	if err != nil {
		ve.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var uid int64
		var cid, um, am, tc string
		if err := rows.Scan(&uid, &cid, &um, &am, &tc); err != nil {
			ve.t.Fatal(err)
		}
		out = append(out, map[string]any{"user_id": uid, "conversation_id": cid, "user": um, "assistant": am, "tool_calls": tc})
	}
	return out
}

func TestVoiceStreamProseTurn(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	r := ve.start("c1", weatherTool)
	if r["status"] != "success" || r["conversation_id"] != "c1" {
		t.Fatalf("start: %v", r)
	}
	if _, has := r["home_context"]; !has || r["home_context"] != nil {
		t.Fatalf("home_context should be null without household.location: %v", r)
	}
	ve.eng.say("It's three o'clock. Anything else?")
	res := ve.turn("/api/v0/voice/command/stream", "c1", "what time is it", map[string]any{"turn_source": "wake", "wake_confidence": 0.9})
	res.want(200)
	if ct := res.header.Get("Content-Type"); ct != "audio/raw" {
		t.Fatalf("content type %q", ct)
	}
	if res.header.Get("X-Audio-Sample-Rate") != "24000" || res.header.Get("X-Audio-Channels") != "1" || res.header.Get("X-Audio-Sample-Width") != "2" {
		t.Fatalf("audio headers %v", res.header)
	}
	if got, _ := url.QueryUnescape(res.header.Get("X-Assistant-Message")); got != "It's three o'clock. Anything else?" {
		t.Fatalf("X-Assistant-Message %q", res.header.Get("X-Assistant-Message"))
	}
	if strings.Contains(res.header.Get("X-Assistant-Message"), " ") || !strings.Contains(res.header.Get("X-Assistant-Message"), "It%27s") {
		t.Fatalf("X-Assistant-Message must be quote(safe=''): %q", res.header.Get("X-Assistant-Message"))
	}
	if string(res.body) != "<It's three o'clock. Anything else?>" {
		t.Fatalf("PCM body %q", res.body)
	}
	// The loop request: shape A on the live label.
	req := ve.eng.last()
	if req["temperature"] != 0.4 || req["max_tokens"] != 256.0 {
		t.Fatalf("loop request %v %v", req["temperature"], req["max_tokens"])
	}
	msgs := messagesOf(req)
	if len(msgs) != 3 || msgs[1]["content"] != prompts.UnknownSpeakerBlock {
		t.Fatalf("messages: %v", msgs)
	}
	user := msgs[2]["content"].(string)
	if !strings.HasPrefix(user, "what time is it") || !strings.HasSuffix(user, "\n/no_think") {
		t.Fatalf("user message %q", user)
	}
	// History committed: system, user, assistant.
	conv := ve.m.convs.get("c1")
	if n := len(conv.messages); n != 4 { // system + speaker block + user + assistant
		t.Fatalf("history len %d", n)
	}

	// A follow-up on the blocking route (the node's follow-up contract): JSON 200.
	ve.eng.say("You're welcome! <exchange_complete/>")
	b := ve.turn("/api/v0/voice/command", "c1", "thanks", map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1}).want(200).json()
	if b["stop_reason"] != "complete" || b["assistant_message"] != "You're welcome!" || b["end_of_exchange"] != true {
		t.Fatalf("blocking body %v", b)
	}
	for _, k := range []string{"commands", "request_information", "stop_reason", "tool_calls", "validation_request", "assistant_message", "reasoning", "end_of_exchange"} {
		if _, ok := b[k]; !ok {
			t.Fatalf("VoiceCommandResponse key %s missing: %v", k, b)
		}
	}
	// The second turn's prompt carries the first exchange and only one speaker block.
	msgs = messagesOf(ve.eng.last())
	speakerBlocks := 0
	for _, m := range msgs {
		if m["content"] == prompts.UnknownSpeakerBlock {
			speakerBlocks++
		}
	}
	if speakerBlocks != 1 {
		t.Fatalf("speaker blocks accumulated: %d", speakerBlocks)
	}
}

func TestVoiceClientToolRoundTrip(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c2", weatherTool)
	ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {"city": "Boston"}}</tool_call>`)
	res := ve.turn("/api/v0/voice/command/stream", "c2", "what's the weather tomorrow", nil).want(202).json()
	if res["stop_reason"] != "tool_calls" {
		t.Fatalf("stream 202: %v", res)
	}
	calls := res["tool_calls"].([]any)
	call := calls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if fn["name"] != "get_weather" || !strings.HasPrefix(call["id"].(string), "call_") {
		t.Fatalf("call %v", call)
	}
	// Date injection (03 §3.5): "tomorrow" from the raw transcript, resolved in the node's zone.
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatal(err)
	}
	dts := args["resolved_datetimes"].([]any)
	if len(dts) != 1 || dts[0] != "2026-10-07T04:00:00Z" {
		t.Fatalf("injected dates %v (want tomorrow's New York midnight)", dts)
	}
	if args["city"] != "Boston" {
		t.Fatalf("args %v", args)
	}

	// Continue (blocking, text path) with a message: the fast path, no LLM call (D23).
	before := len(ve.eng.requests())
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c2",
		"tool_results": []any{map[string]any{"tool_call_id": call["id"], "output": map[string]any{"success": true, "message": "Sunny and 70."}}}},
		ve.node.h()).want(200).json()
	if b["assistant_message"] != "Sunny and 70." || b["stop_reason"] != "complete" || len(ve.eng.requests()) != before {
		t.Fatalf("continue fast path %v (llm calls %d→%d)", b, before, len(ve.eng.requests()))
	}
	if ri := b["request_information"].(map[string]any); ri["voice_command"] != "[continuation with tool results]" {
		t.Fatalf("request_information %v", ri)
	}

	// Another tool round, continued on the stream route without a message: one streamed LLM
	// formatting call, spoken sentence by sentence.
	ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {"city": "Denver"}}</tool_call>`)
	res = ve.turn("/api/v0/voice/command/stream", "c2", "and in denver", nil).want(202).json()
	id := res["tool_calls"].([]any)[0].(map[string]any)["id"]
	ve.eng.say("It'll be 70 degrees. Enjoy the sun!")
	s := ve.do("POST", "/api/v0/voice/command/continue/stream", map[string]any{"conversation_id": "c2",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{"temp": 70}}}}, ve.node.h()).want(200)
	if string(s.body) != "<It'll be 70 degrees.><Enjoy the sun!>" || s.header.Get("X-Assistant-Message") != "" {
		t.Fatalf("continue stream audio %q", s.body)
	}
	req := ve.eng.last()
	if req["stream"] != true || req["max_tokens"] != 512.0 || req["temperature"] != 0.7 {
		t.Fatalf("continue stream request %v", req)
	}
	msgs := messagesOf(req)
	if last := msgs[len(msgs)-1]; last["role"] != "system" || !strings.HasPrefix(last["content"].(string), "Respond naturally in plain text") {
		t.Fatalf("plain-text override missing: %v", last)
	}
	for _, m := range msgs {
		if m["role"] == "tool" {
			t.Fatal("text path must not send role=tool")
		}
	}
	// The committed history ends with the answer and has no override.
	conv := ve.m.convs.get("c2")
	if last := conv.messages[len(conv.messages)-1]; last.Role != "assistant" || last.Content != "It'll be 70 degrees. Enjoy the sun!" {
		t.Fatalf("committed %+v", last)
	}
}

func TestVoiceTextContinueIsOneFormattingCall(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c3", weatherTool)
	ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {}}</tool_call>`)
	id := ve.turn("/api/v0/voice/command/stream", "c3", "weather please", nil).want(202).json()["tool_calls"].([]any)[0].(map[string]any)["id"]
	ve.eng.say(`<think>hmm</think>Seventy and sunny.`)
	before := len(ve.eng.requests())
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c3",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{"temp": 70}}}}, ve.node.h()).want(200).json()
	if b["assistant_message"] != "Seventy and sunny." || len(ve.eng.requests()) != before+1 {
		t.Fatalf("text continue %v", b)
	}
	req := ve.eng.last()
	if req["max_tokens"] != 256.0 || req["temperature"] != 0.7 {
		t.Fatalf("formatting call shape %v", req)
	}
	msgs := messagesOf(req)
	last := msgs[len(msgs)-1]["content"].(string)
	if !strings.HasPrefix(last, `The user asked: "weather please`) || !strings.HasSuffix(last, `{"temp": 70}`+"\n/no_think") {
		t.Fatalf("formatting message %q", last)
	}
}

func TestVoiceNativeContinueReentersLoop(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("c4", weatherTool)
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_n1", "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Austin"}`}}}})
	r := ve.turn("/api/v0/voice/command/stream", "c4", "weather in austin", nil).want(202).json()
	if r["stop_reason"] != "tool_calls" {
		t.Fatalf("%v", r)
	}
	if req := ve.eng.last(); req["tool_choice"] != "auto" || len(req["tools"].([]any)) == 0 {
		t.Fatalf("native request %v", req["tool_choice"])
	}
	ve.eng.say("It's 80 in Austin.")
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c4",
		"tool_results": []any{map[string]any{"tool_call_id": "call_n1", "output": map[string]any{"temp": 80}}}}, ve.node.h()).want(200).json()
	if b["assistant_message"] != "It's 80 in Austin." {
		t.Fatalf("native continue %v", b)
	}
	msgs := messagesOf(ve.eng.last())
	var sawCall, sawTool bool
	for _, m := range msgs {
		if m["role"] == "assistant" && m["tool_calls"] != nil {
			sawCall = true
		}
		if m["role"] == "tool" && m["tool_call_id"] == "call_n1" && m["content"] == `{"temp": 80}` {
			sawTool = true
		}
	}
	if !sawCall || !sawTool {
		t.Fatalf("native continue must re-enter the loop with the tool history: %v", msgs)
	}
}

func TestVoiceServerToolNative(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.stt.recognition = true
	alex := int64(7)
	ve.stt.speaker = &alex
	ve.start("c5", "[]")
	ve.transcribe("c5", false)
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_s1", "type": "function",
		"function": map[string]any{"name": "identify_speaker", "arguments": `{}`}}}})
	ve.eng.say("You're Alex.")
	b := ve.turn("/api/v0/voice/command", "c5", "who am i", nil).want(200).json()
	if b["assistant_message"] != "You're Alex." {
		t.Fatalf("%v", b)
	}
	msgs := messagesOf(ve.eng.last())
	found := false
	for _, m := range msgs {
		if m["role"] == "tool" && m["content"] == `{"speaker_name": "alex"}` {
			found = true
		}
		if m["role"] == "system" && strings.HasPrefix(m["content"].(string), "You are speaking with alex.") {
			found = found || false
		}
	}
	if !found {
		t.Fatalf("server tool result not in the loop: %v", msgs)
	}
	if msgs[1]["content"] != "You are speaking with alex." {
		t.Fatalf("speaker block %v", msgs[1])
	}
}

func TestVoiceUnknownSpeakerRefusals(t *testing.T) {
	// D21 + M14: identify_speaker with recognition off says why.
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("c6", "[]")
	ve.transcribe("c6", false) // recognition off: the speaker pass reports "off"
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_u1", "type": "function",
		"function": map[string]any{"name": "identify_speaker", "arguments": `{}`}}}})
	ve.eng.say("I can't tell.")
	ve.turn("/api/v0/voice/command", "c6", "who am i", nil).want(200)
	var tool map[string]any
	for _, m := range messagesOf(ve.eng.last()) {
		if m["role"] == "tool" {
			_ = json.Unmarshal([]byte(m["content"].(string)), &tool)
		}
	}
	if tool["speaker_name"] != nil || !strings.Contains(tool["message"].(string), "Speaker recognition is off") {
		t.Fatalf("M14 refusal %v", tool)
	}
	// Recognition on but no match: the enrollment hint, and per-user tools see no speaker.
	sp := servertools.Speaker{}
	if sp.Known() || sp.Refusal() != "I'm not sure who's speaking." {
		t.Fatal("unknown speaker refusal")
	}
	if (servertools.Speaker{RecognitionOff: true}).Refusal() == sp.Refusal() {
		t.Fatal("M14 wording")
	}
}

func TestVoiceNotForMeAndNoise(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c7", "[]")
	ve.eng.say("<not_for_me/>")
	r := ve.turn("/api/v0/voice/command/stream", "c7", "so then she said the meeting moved", nil).want(202).json()
	if r["stop_reason"] != "not_for_me" || r["assistant_message"] != "" {
		t.Fatalf("not_for_me %v", r)
	}
	before := len(ve.eng.requests())
	r = ve.turn("/api/v0/voice/command/stream", "c7", "[laughter]", nil).want(202).json()
	if r["stop_reason"] != "not_for_me" || len(ve.eng.requests()) != before {
		t.Fatalf("STT noise must never reach the LLM: %v", r)
	}
}

func TestVoiceWakeVerificationEnforce(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.set(settingWakeMode, "enforce", settings.Scope{HouseholdID: voiceHH})
	ve.stt.wakeText = "the weather"
	ve.start("c8", "[]")
	ve.transcribe("c8", true)
	before := len(ve.eng.requests())
	r := ve.turn("/api/v0/voice/command/stream", "c8", "turn off the lights", map[string]any{"turn_source": "wake"}).want(202).json()
	if r["stop_reason"] != "not_for_me" || len(ve.eng.requests()) != before {
		t.Fatalf("enforce + unverified wake must be silent: %v", r)
	}
	// A verified clip goes through.
	ve.stt.wakeText = "hey jervis"
	ve.start("c9", "[]")
	ve.transcribe("c9", true)
	ve.eng.say("Done.")
	ve.turn("/api/v0/voice/command/stream", "c9", "what time is it", map[string]any{"turn_source": "wake"}).want(200)
}

func TestVoiceTranscriptsD19D50(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	alex := int64(7)
	ve.stt.speaker = &alex

	// Recognition off (the default): nobody is identified, nothing is logged (D50).
	ve.start("t1", weatherTool)
	ve.transcribe("t1", false)
	ve.eng.say("Sure thing.")
	ve.turn("/api/v0/voice/command/stream", "t1", "tell me something", nil).want(200)
	if n := len(ve.transcripts()); n != 0 {
		t.Fatalf("recognition off logged %d transcripts", n)
	}

	// Recognition on, confident speaker: the stream turn is logged (D19 fixes §8.1).
	ve.stt.recognition = true
	ve.start("t2", weatherTool)
	ve.transcribe("t2", false)
	ve.eng.say("It's noon.")
	ve.turn("/api/v0/voice/command/stream", "t2", "what time is it", nil).want(200)
	rows := ve.transcripts()
	if len(rows) != 1 || rows[0]["user_id"] != int64(7) || rows[0]["user"] != "what time is it" || rows[0]["assistant"] != "It's noon." {
		t.Fatalf("transcripts %v", rows)
	}
	// A tool exchange is logged once, when its continue completes, with the calls.
	ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {}}</tool_call>`)
	id := ve.turn("/api/v0/voice/command/stream", "t2", "weather", nil).want(202).json()["tool_calls"].([]any)[0].(map[string]any)["id"]
	if len(ve.transcripts()) != 1 {
		t.Fatal("tool_calls turn logged before its continue")
	}
	ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "t2",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{"message": "Rainy."}}}}, ve.node.h()).want(200)
	rows = ve.transcripts()
	if len(rows) != 2 || rows[1]["user"] != "weather" || rows[1]["assistant"] != "Rainy." || !strings.Contains(rows[1]["tool_calls"].(string), `"name": "get_weather"`) {
		t.Fatalf("tool exchange transcript %v", rows)
	}
	// The speaker persists within the conversation (D3): a turn without a fresh match keeps it.
	ve.eng.say("Bye.")
	ve.turn("/api/v0/voice/command", "t2", "goodbye", nil).want(200)
	if rows = ve.transcripts(); len(rows) != 3 || rows[2]["user_id"] != int64(7) {
		t.Fatalf("follow-up transcript %v", rows)
	}
	// A new conversation never inherits it (D3).
	ve.start("t3", weatherTool)
	ve.eng.say("Hi.")
	ve.turn("/api/v0/voice/command", "t3", "hello", nil).want(200)
	if len(ve.transcripts()) != 3 {
		t.Fatal("a new conversation inherited the previous speaker")
	}
	// memory.extraction_enabled off: nothing is logged.
	ve.set(settingExtractionEnabled, false, settings.Scope{HouseholdID: voiceHH})
	ve.transcribe("t3", false)
	ve.eng.say("Hi again.")
	ve.turn("/api/v0/voice/command", "t3", "hello again", nil).want(200)
	if len(ve.transcripts()) != 3 {
		t.Fatal("extraction off still logged")
	}
}

func TestVoiceConversationLifecycle(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	// Unknown conversation: 400 on stream, 422 on blocking (frozen node contract).
	ve.turn("/api/v0/voice/command/stream", "nope", "hi", nil).detail(400, notInitialized)
	ve.turn("/api/v0/voice/command", "nope", "hi", nil).detail(422, notInitialized)
	ve.do("POST", "/api/v0/voice/command/continue/stream", map[string]any{"conversation_id": "nope", "tool_results": []any{}}, ve.node.h()).want(202)

	// Sliding idle TTL (D40 01.Q2).
	ve.start("l1", "[]")
	for i := 0; i < 3; i++ {
		ve.advance(9 * time.Minute)
		ve.eng.say("ok.")
		ve.turn("/api/v0/voice/command", "l1", "hi", nil).want(200)
	}
	ve.advance(11 * time.Minute)
	ve.turn("/api/v0/voice/command", "l1", "hi", nil).want(422)

	// /conversation/end evicts.
	ve.start("l2", "[]")
	ve.do("POST", "/api/v0/conversation/end", map[string]any{"conversation_id": "l2"}, ve.node.h()).want(200)
	ve.turn("/api/v0/voice/command/stream", "l2", "hi", nil).want(400)

	// Cap.
	ve.m.convs.limit = 2
	ve.start("l3", "[]")
	ve.advance(time.Second)
	ve.start("l4", "[]")
	ve.advance(time.Second)
	ve.start("l5", "[]")
	if ve.m.convs.get("l3") != nil || ve.m.convs.len() != 2 {
		t.Fatal("cap must evict the least recently used conversation")
	}
	// The sweeper drops idle ones.
	ve.advance(20 * time.Minute)
	if n := ve.m.convs.sweep(); n != 2 {
		t.Fatalf("swept %d", n)
	}
}

func TestVoiceStartErrors(t *testing.T) {
	ve := newVoiceEnv(t, "Qwen25MediumUntrained") // a dropped provider: hard error (D11)
	r := ve.do("POST", "/api/v0/conversation/start", `{"conversation_id":"x"}`, ve.node.h()).want(500).json()
	if !strings.HasPrefix(r["detail"].(string), "Failed to start conversation: unknown prompt provider") {
		t.Fatalf("%v", r)
	}
	r = ve.do("POST", "/api/v0/conversation/start", `{"client_tools":5}`, ve.node.h()).want(400).json()
	if r["error"] != "validation_error" {
		t.Fatalf("%v", r)
	}
}

func TestVoiceAcknowledgeAndWakeResponse(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	r := ve.do("POST", "/api/v0/voice/acknowledge", map[string]any{"voice_command": "show me the weather"}, ve.node.h()).want(200).json()
	if s, _ := r["text"].(string); s == "" {
		t.Fatalf("ack %v", r)
	}
	ve.eng.say("<think>\n\n</think>Right here.")
	r = ve.do("POST", "/api/v0/wake-response", nil, ve.node.h()).want(200).json()
	if r["text"] != "Right here." {
		t.Fatalf("wake-response %v", r)
	}
	req := ve.eng.last()
	if req["max_tokens"] != 12.0 || req["temperature"] != 1.1 {
		t.Fatalf("wake-response request %v", req)
	}
	ve.eng.say("")
	if r = ve.do("POST", "/api/v0/wake-response", nil, ve.node.h()).want(200).json(); r["text"] != "Yes?" {
		t.Fatalf("fallback %v", r)
	}
}

func TestVoiceForceToolsGuardRetries(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("f1", weatherTool)
	// An action-shaped, keyword-matching utterance answered in prose is retried with the
	// [MUST_CALL_RETRY] nag; the model then calls the tool.
	ve.eng.say("It will be sunny tomorrow.")
	ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {}}</tool_call>`)
	r := ve.turn("/api/v0/voice/command/stream", "f1", "check the weather forecast", nil).want(202).json()
	if r["stop_reason"] != "tool_calls" {
		t.Fatalf("%v", r)
	}
	msgs := messagesOf(ve.eng.last())
	last := msgs[len(msgs)-1]
	if last["role"] != "system" || !strings.HasPrefix(last["content"].(string), "[MUST_CALL_RETRY] You MUST call a tool.") {
		t.Fatalf("nag %v", last)
	}
	// The nag never survives into the next turn's history.
	ve.eng.say("ok.")
	ve.turn("/api/v0/voice/command", "f1", "what time is it", nil).want(200)
	for _, m := range messagesOf(ve.eng.last()) {
		if strings.HasPrefix(m["content"].(string), "[MUST_CALL_RETRY]") {
			t.Fatal("stale nag kept")
		}
	}
}

func TestVoiceIterationLimitFallback(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("i1", "[]")
	// request_validation together with another call loops; ten iterations exhaust.
	for i := 0; i < 12; i++ {
		ve.eng.push(engineReply{toolCalls: []map[string]any{
			{"id": "call_a", "type": "function", "function": map[string]any{"name": "request_validation", "arguments": `{"question":"q","parameter_name":"p"}`}},
			{"id": "call_b", "type": "function", "function": map[string]any{"name": "identify_speaker", "arguments": `{}`}},
		}})
	}
	b := ve.turn("/api/v0/voice/command", "i1", "do the thing", nil).want(200).json()
	if b["assistant_message"] != "Sorry, I got stuck on that." {
		t.Fatalf("D40 02.Q9 fallback %v", b)
	}
}

func TestVoiceValidationRequired(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("v1", "[]")
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_v", "type": "function",
		"function": map[string]any{"name": "request_validation", "arguments": `{"question":"Which room?","parameter_name":"room","options":["kitchen","den"]}`}}}})
	r := ve.turn("/api/v0/voice/command/stream", "v1", "turn on the light", nil).want(202).json()
	vr, _ := r["validation_request"].(map[string]any)
	if r["stop_reason"] != "validation_required" || vr["question"] != "Which room?" || r["assistant_message"] != nil {
		t.Fatalf("%v", r)
	}
}

func TestVoiceMediaTTS(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	r := ve.do("POST", "/api/v0/media/tts/speak", map[string]any{"text": "Hello there."}, ve.node.h()).want(200)
	if r.header.Get("Content-Type") != "audio/wav" || !bytes.HasPrefix(r.body, []byte("RIFF")) {
		t.Fatalf("wav %q", r.header)
	}
	s := ve.do("POST", "/api/v0/media/tts/speak/stream", map[string]any{"text": "Hi **there**"}, ve.node.h()).want(200)
	if s.header.Get("X-Audio-Sample-Rate") != "24000" || string(s.body) != "<Hi there>" {
		t.Fatalf("stream %q %q", s.header, s.body)
	}
	ve.do("POST", "/api/v0/media/tts/speak", map[string]any{"text": "😀"}, ve.node.h()).want(400)
}

func TestVoiceTranscribeContract(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	out := ve.transcribe("x", false)
	sp, _ := out["speaker"].(map[string]any)
	if out["text"] != "what time is it" || sp == nil || sp["user_id"] != nil || out["affect"] != nil {
		t.Fatalf("%v", out)
	}
	if _, has := out["affect"]; !has {
		t.Fatal("affect key must be present (D38)")
	}
}

func TestVoiceEnrollmentHandoff(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	tok := "tok-7"
	// M4 without a pending start is refused (D4 06.Q5).
	ve.enrollUpload(7, http.StatusForbidden)
	rid := ve.do("POST", "/api/v0/mobile/voice-profile/start-node-enrollment", map[string]any{"node_id": ve.node.id}, bearer(tok)).want(200).json()["request_id"].(string)
	ve.do("GET", "/api/v0/mobile/voice-profile-results/"+rid, nil, bearer(tok)).detail(202, "pending")
	ve.do("GET", "/api/v0/mobile/voice-profile-results/"+rid, nil, bearer("tok-8")).want(403)
	// Another user of the household can't enroll on that start.
	ve.enrollUpload(8, http.StatusForbidden)
	ok := ve.enrollUpload(7, http.StatusOK)
	if ok["status"] != "enrolled" || ok["household_id"] != voiceHH {
		t.Fatalf("enroll %v", ok)
	}
	// A low-quality take: M4 422, and the node's upload failure reaches mobile as low_quality.
	rid = ve.do("POST", "/api/v0/mobile/voice-profile/start-node-enrollment", map[string]any{"node_id": ve.node.id}, bearer(tok)).want(200).json()["request_id"].(string)
	ve.stt.enrollErr = &stt.LowQualityError{Reason: "too_little_speech", SpeechSeconds: 1.2, MinSpeech: 3}
	lq := ve.enrollUpload(7, http.StatusUnprocessableEntity)
	if lq["error"] != "low_quality" {
		t.Fatalf("%v", lq)
	}
	ve.do("POST", "/api/v0/mobile/voice-profile-results/"+rid, map[string]any{"success": false,
		"error": "upload_failed: 422 Client Error: Unprocessable Entity"}, ve.node.h()).want(200)
	res := ve.do("GET", "/api/v0/mobile/voice-profile-results/"+rid, nil, bearer(tok)).want(200).json()
	if res["success"] != false || res["error"] != "low_quality" {
		t.Fatalf("stt Q1 mapping %v", res)
	}
	ve.do("GET", "/api/v0/mobile/voice-profile-results/"+rid, nil, bearer(tok)).want(202) // consumed
}

func (ve *voiceEnv) enrollUpload(userID int64, status int) map[string]any {
	ve.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "e.wav")
	_, _ = fw.Write(stt.EncodeWAV(make([]float32, 16000*4)))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ve.srv.URL+"/api/v0/media/whisper/voice-profiles/enroll?user_id="+itoaInt(int(userID)), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", ve.node.id+":"+ve.node.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		ve.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != status {
		ve.t.Fatalf("enroll status %d, want %d: %v", res.StatusCode, status, out)
	}
	return out
}

func TestNodePluginEndpoints(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	h := ve.node.h()
	// /node/llm/chat (D5).
	ve.eng.say("ok")
	r := ve.do("POST", "/api/v0/node/llm/chat", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "say ok"}}}, h).want(200).json()
	if r["content"] != "ok" || ve.eng.last()["temperature"] != 0.0 {
		t.Fatalf("%v", r)
	}
	r = ve.do("POST", "/api/v0/node/llm/chat", map[string]any{"messages": []any{map[string]any{"role": "tool", "content": "x"}}}, h).want(400).json()
	if d := r["details"].([]any)[0].(string); !strings.HasPrefix(d, "body -> messages -> 0 -> role: ") {
		t.Fatalf("%v", r)
	}
	// /generate/date-context: always filled (D40 03.Q11).
	dc := ve.do("GET", "/api/v0/generate/date-context", nil, h).want(200).json()
	tz := dc["timezone"].(map[string]any)
	if tz["user_timezone"] != "UTC" || tz["is_dst"] != false {
		t.Fatalf("%v", tz)
	}
	// /node/inbox-item: household from the node, node_id setdefault.
	r = ve.do("POST", "/api/v0/node/inbox-item", map[string]any{"title": "Hi", "metadata": map[string]any{"k": "v"}}, h).want(200).json()
	if r["sent"] != true || r["id"] == nil || r["withheld_by"] != nil {
		t.Fatalf("%v", r)
	}
	it := ve.notify.items[0]
	if it.HouseholdID != voiceHH || it.Metadata["node_id"] != ve.node.id || it.Category != "general" || it.SourceService != "jarvis-command-center" {
		t.Fatalf("%+v", it)
	}
	ve.do("POST", "/api/v0/node/inbox-item", map[string]any{"title": "x", "metadata": map[string]any{"node_id": "spoof"}}, h).want(200)
	if ve.notify.items[1].Metadata["node_id"] != "spoof" {
		t.Fatal("setdefault: the caller's node_id wins")
	}
	if r = ve.do("POST", "/api/v0/node/inbox-item", map[string]any{"title": "  "}, h).want(200).json(); r["sent"] != false {
		t.Fatalf("%v", r)
	}
	// /node/push-notification: the legacy confirmation card + high-priority push.
	r = ve.do("POST", "/api/v0/node/push-notification", map[string]any{"title": "T", "body": "B"}, h).want(200).json()
	if r["sent"] != true || r["inbox_item_id"] == nil {
		t.Fatalf("%v", r)
	}
	card := ve.notify.items[len(ve.notify.items)-1]
	if card.Category != "confirmation" || card.Metadata["command_name"] != "reminder" || card.Summary != "B" {
		t.Fatalf("%+v", card)
	}
	if p := ve.notify.pushes[len(ve.notify.pushes)-1]; p.Priority != "high" || p.TargetType != "household" {
		t.Fatalf("%+v", p)
	}
	// /node/send-link.
	if r = ve.do("POST", "/api/v0/node/send-link", map[string]any{"user_id": 7, "url": "ftp://x"}, h).want(200).json(); r["sent"] != false {
		t.Fatalf("%v", r)
	}
	r = ve.do("POST", "/api/v0/node/send-link", map[string]any{"user_id": 7, "url": "https://example.com"}, h).want(200).json()
	link := ve.notify.items[len(ve.notify.items)-1]
	if r["sent"] != true || link.Category != "link" || link.Title != "Link from Jarvis" || link.Metadata["type"] != "open_url" || *link.UserID != 7 {
		t.Fatalf("%v %+v", r, link)
	}
}

func TestContinueStreamPushesActions(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("a1", weatherTool)
	ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {}}</tool_call>`)
	id := ve.turn("/api/v0/voice/command/stream", "a1", "weather", nil).want(202).json()["tool_calls"].([]any)[0].(map[string]any)["id"]
	ve.do("POST", "/api/v0/voice/command/continue/stream", map[string]any{"conversation_id": "a1",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{"message": "Draft ready.",
			"context": map[string]any{"actions": []any{map[string]any{"name": "send", "label": "Send"}}, "command_name": "email", "preview": "Hi"}}}}}, ve.node.h()).want(200)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ve.notify.mu.Lock()
		n := len(ve.notify.items)
		ve.notify.mu.Unlock()
		if n == 1 {
			ve.notify.mu.Lock()
			it := ve.notify.items[0]
			ve.notify.mu.Unlock()
			if it.Title != "Confirm: email" || it.Category != "confirmation" {
				t.Fatalf("%+v", it)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actions were not pushed to the inbox")
}

var _ = context.Background
