//go:build contract

package contract

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// The command-center voice hot path (docs/cc/01 §2, §3.2–§3.8), shape-only against the
// target's real model like TestCCNodeLLMChat: /conversation/start, /voice/command/stream,
// /voice/command, /voice/command/continue[/stream], /voice/acknowledge, /wake-response and
// /conversation/end, all node-authenticated. Model output is never asserted, only which of the
// documented response forms came back and its exact shape. The continue routes are driven down
// their deterministic no-LLM path (every tool result carries a message). Then the admin trace
// list/detail (doc 05 §3.10) reads back the traces those turns wrote.

// voiceCommandResponse is VoiceCommandResponse as pydantic dumps it: every key, nulls included.
var voiceCommandResponse = Obj{
	"commands": ArrayOf(Obj{
		"success": Bool, "command_name": NullOr(String), "parameters": NullOr(Object),
		"errors": NullOr(Obj{
			"type": String, "message": String, "missing_parameters": NullOr(ArrayOf(String)),
			"suggestions": NullOr(ArrayOf(String)), "clarification_question": NullOr(String),
		}),
	}),
	"request_information": NullOr(Obj{"voice_command": String, "conversation_id": NullOr(String)}),
	"stop_reason":         NullOr(OneOf("complete", "tool_calls", "validation_required", "error", "not_for_me")),
	"tool_calls": NullOr(ArrayOf(Obj{
		"id": String, "type": Eq("function"), "function": Object, "failure_message": NullOr(String),
	})),
	"validation_request": NullOr(Obj{
		"question": String, "parameter_name": String, "options": NullOr(ArrayOf(String)), "tool_call_id": NullOr(String),
	}),
	"assistant_message": NullOr(String),
	"reasoning":         NullOr(String),
	"end_of_exchange":   NullOr(Bool),
}

// The acknowledgment pools (services/acknowledgment_service.py), matched first-hit in order.
var (
	ackWeather = []string{"Checking the forecast.", "Let me check the weather.", "One moment, pulling up the forecast."}
	ackSearch  = []string{"Let me look into that.", "Good question, give me a moment.", "Looking into it.", "Let me find out."}
	ackGeneric = []string{"Let me look into that.", "Working on it.", "One moment.", "Give me a second.", "On it.", "Let me check."}
)

// ccVoiceAudio checks a 200 audio/raw voice answer: X-Audio-* agree with jarvis-tts
// /audio/format (D40 01.Q8), X-Assistant-Message is present (wantMsg: "" means it must be
// empty, "*" means any non-empty value that URL-decodes), and the body is whole PCM frames.
func ccVoiceAudio(t *testing.T, r *RawResp, wantMsg string) {
	t.Helper()
	rate, channels, width := ccMediaTTSFormat(t)
	ccExpectPCMHeaders(r, rate, channels, width)
	if !r.HasHeader("X-Assistant-Message") {
		r.Fatalf("missing X-Assistant-Message")
	}
	msg := r.HeaderVal("X-Assistant-Message")
	switch wantMsg {
	case "":
		if msg != "" {
			r.Fatalf("X-Assistant-Message: want empty, got %q", msg)
		}
	case "*":
		if dec, err := url.PathUnescape(msg); msg == "" || err != nil || dec == "" {
			r.Fatalf("X-Assistant-Message: want URL-quoted text, got %q (%v)", msg, err)
		}
	}
	ccExpectPCMBody(r, channels*width)
}

// ccVoiceTurn accepts either documented answer of /voice/command/stream: 200 audio/raw with
// the quoted assistant message, or 202 with a VoiceCommandResponse.
func ccVoiceTurn(t *testing.T, r *RawResp) {
	t.Helper()
	switch r.Status {
	case http.StatusOK:
		ccVoiceAudio(t, r, "*")
	case http.StatusAccepted:
		r.ExpectJSONContentType().ExpectShape(voiceCommandResponse)
		t.Logf("stream turn answered 202: %s", r.Body)
	default:
		r.Fatalf("want 200 audio/raw or 202 JSON")
	}
}

func TestCCVoice(t *testing.T) {
	tg := T(t)
	tg.Need(t, LLM, TTS)
	u := SharedUser(t)
	n := NewCCNode(t, u)
	h := n.APIKeyH()
	cid := "contract-" + tg.RunID + "-conv-" + randHex(4)
	unknown := "contract-" + tg.RunID + "-nosuch-" + randHex(4)
	results := map[string]any{"conversation_id": cid, "tool_results": []map[string]any{{
		"tool_call_id": "contract-call-1", "output": map[string]any{"success": true, "message": "The contract timer is set."},
	}}}

	t.Run("preconditions", func(t *testing.T) {
		turn := map[string]any{"voice_command": "hello", "conversation_id": unknown}
		// The same missing cache entry is a 422 on the blocking route, a 400 on the stream.
		tg.Post(t, CommandCenter, "/api/v0/voice/command", turn, h).
			ExpectError(http.StatusUnprocessableEntity, "Conversation not initialized for tool-based flow")
		tg.Post(t, CommandCenter, "/api/v0/voice/command/stream", turn, h).
			ExpectError(http.StatusBadRequest, "Conversation not initialized for tool-based flow")
		res := map[string]any{"conversation_id": unknown, "tool_results": []map[string]any{{"tool_call_id": "x", "output": "y"}}}
		tg.Post(t, CommandCenter, "/api/v0/voice/command/continue", res, h).
			ExpectError(http.StatusUnprocessableEntity, "Conversation "+unknown+" not found or expired")
		tg.Post(t, CommandCenter, "/api/v0/voice/command/continue/stream", res, h).
			Expect(http.StatusAccepted, Obj{"fallback": Eq("use_blocking_continue")})
	})

	t.Run("validation", func(t *testing.T) {
		tg.Post(t, CommandCenter, "/api/v0/conversation/start", map[string]any{}, h).
			Expect(http.StatusBadRequest, ccValidation("body -> conversation_id: Field required"))
		tg.Post(t, CommandCenter, "/api/v0/voice/command", map[string]any{"conversation_id": cid}, h).
			Expect(http.StatusBadRequest, ccValidation("body -> voice_command: Field required"))
		tg.Post(t, CommandCenter, "/api/v0/voice/command/continue", map[string]any{"conversation_id": cid}, h).
			Expect(http.StatusBadRequest, ccValidation("body -> tool_results: Field required"))
		tg.Post(t, CommandCenter, "/api/v0/voice/acknowledge", map[string]any{}, h).
			Expect(http.StatusBadRequest, ccValidation("body -> voice_command: Field required"))
		tg.Post(t, CommandCenter, "/api/v0/conversation/start", map[string]any{"conversation_id": cid},
			H{"X-API-Key": n.ID + ":wrong-" + randHex(4)}).ExpectError(http.StatusUnauthorized, "Invalid node credentials")
	})

	t.Run("acknowledge", func(t *testing.T) {
		ack := func(cmd string, pool []string) {
			t.Helper()
			tg.Post(t, CommandCenter, "/api/v0/voice/acknowledge", map[string]any{"voice_command": cmd}, h).
				Expect(http.StatusOK, Obj{"text": OneOf(pool...)})
		}
		ack("What's the weather forecast", ackWeather)
		ack("contract qqq", ackGeneric)
		// Legacy matches keywords as substrings ("showtime" hits "how"); M5 adds word
		// boundaries in jarvisd, so the same text falls to the generic pool.
		if Jarvisd() {
			ack("showtime please", ackGeneric)
		} else {
			ack("showtime please", ackSearch)
		}
	})

	t.Run("wake_response", func(t *testing.T) {
		// Always 200 with a usable greeting ("Yes?" on any failure).
		tg.SlowDo(t, CommandCenter, http.MethodPost, "/api/v0/wake-response", "", nil, h).
			Expect(http.StatusOK, Obj{"text": NonEmptyString})
	})

	started := t.Run("start", func(t *testing.T) {
		tg.SlowJSON(t, CommandCenter, "/api/v0/conversation/start", map[string]any{
			"conversation_id": cid,
			// D2: speaker_user_id / speaker_confidence are accepted (and ignored by jarvisd).
			"node_context":       map[string]any{"timezone": "America/New_York", "speaker_user_id": nil},
			"available_commands": []any{},
			"client_tools": []map[string]any{{"type": "function", "function": map[string]any{
				"name": "contract_timer", "description": "Set a contract timer.",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{
					"minutes": map[string]any{"type": "integer"}}, "required": []string{"minutes"}},
			}}},
		}, h).Expect(http.StatusOK, Obj{
			"status": Eq("success"), "conversation_id": Eq(cid),
			"home_context": NullOr(Obj{"location": Any}),
		})
	})
	if !started {
		t.Fatalf("conversation start failed; the turn subtests need it")
	}

	t.Run("stream_turn", func(t *testing.T) {
		ccVoiceTurn(t, tg.SlowJSON(t, CommandCenter, "/api/v0/voice/command/stream", map[string]any{
			"voice_command": "What is two plus two?", "conversation_id": cid,
			"turn_source": "wake", "wake_confidence": 0.9, "pre_wake_speech_seconds": 0.2, "affect": nil,
		}, h))
	})

	t.Run("blocking_turn", func(t *testing.T) {
		v := tg.SlowJSON(t, CommandCenter, "/api/v0/voice/command", map[string]any{
			"voice_command": "And what is three plus three?", "conversation_id": cid,
			"turn_source": "follow_up", "follow_up_iteration": 1, "speaker_user_id": u.ID,
		}, h).Expect(http.StatusOK, voiceCommandResponse).Object()
		info := v["request_information"].(map[string]any)
		if info["conversation_id"] != cid || info["voice_command"] != "And what is three plus three?" {
			t.Fatalf("request_information: %v", info)
		}
	})

	t.Run("continue", func(t *testing.T) {
		// Every result carries a message: the text path speaks it without an LLM call.
		tg.SlowJSON(t, CommandCenter, "/api/v0/voice/command/continue", results, h).Expect(http.StatusOK, Obj{
			"commands":            Eq([]any{}),
			"request_information": Obj{"voice_command": Eq("[continuation with tool results]"), "conversation_id": Eq(cid)},
			"stop_reason":         Eq("complete"),
			"tool_calls":          Eq([]any{}),
			"validation_request":  Null,
			"assistant_message":   NonEmptyString,
			"reasoning":           Null, // the continue route never returns reasoning
			"end_of_exchange":     Bool,
		})
	})

	t.Run("continue_stream", func(t *testing.T) {
		// The same fast path, streamed: 200 audio/raw with an empty X-Assistant-Message.
		ccVoiceAudio(t, tg.SlowJSON(t, CommandCenter, "/api/v0/voice/command/continue/stream", results, h), "")
	})

	t.Run("end", func(t *testing.T) {
		tg.Post(t, CommandCenter, "/api/v0/conversation/end", map[string]any{"conversation_id": cid}, h).
			Expect(http.StatusOK, Obj{"status": Eq("ok"), "conversation_id": Eq(cid)})
		r := tg.SlowJSON(t, CommandCenter, "/api/v0/voice/command/continue", results, h)
		if Jarvisd() {
			// D40 01.Q2: /conversation/end evicts the conversation.
			r.ExpectError(http.StatusUnprocessableEntity, "Conversation "+cid+" not found or expired")
		} else {
			// Legacy /conversation/end only resets speaker stickiness; the cache entry lives
			// on until its TTL, so the conversation still continues.
			r.Expect(http.StatusOK, voiceCommandResponse)
		}
	})

	t.Run("admin_traces", func(t *testing.T) {
		item := Obj{
			"id": UUID, "conversation_id": String, "request_type": String, "source": Eq("node"),
			"node_id": Eq(n.ID), "household_id": Eq(u.HouseholdID), "user_command": NullOr(String),
			"assistant_message": NullOr(String), "status": String, "total_duration_ms": Num,
			// LEGACY-BUG: naive timestamp.
			"span_count": Int, "created_at": TimestampNaive,
		}
		path := "/api/v0/admin/traces?limit=50&node_id=" + url.QueryEscape(n.ID)
		// Legacy persists traces from a background thread; give the writes a moment.
		var list map[string]any
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(500 * time.Millisecond) {
			list = tg.Get(t, CommandCenter, path, CCAdminH()).
				Expect(http.StatusOK, Obj{"traces": ArrayOf(item), "total": Int}).Object()
			if len(list["traces"].([]any)) >= 3 || time.Now().After(deadline) {
				break
			}
		}
		traces := list["traces"].([]any)
		if len(traces) == 0 {
			t.Fatalf("no traces for node %s", n.ID)
		}
		if tot, _ := list["total"].(json.Number).Int64(); int(tot) != len(traces) {
			t.Fatalf("total %d != %d traces on one page", tot, len(traces))
		}
		types := map[string]bool{}
		for _, tr := range traces {
			types[tr.(map[string]any)["request_type"].(string)] = true
		}
		t.Logf("trace request types for the node: %v", types)
		if !types["voice_command"] {
			t.Fatalf("no voice_command trace among %v", types)
		}

		id := traces[0].(map[string]any)["id"].(string)
		tg.Get(t, CommandCenter, "/api/v0/admin/traces/"+id, CCAdminH()).Expect(http.StatusOK, Obj{
			"id": Eq(id), "conversation_id": String, "request_type": String, "source": Eq("node"),
			"node_id": Eq(n.ID), "household_id": Eq(u.HouseholdID), "user_command": NullOr(String),
			"assistant_message": NullOr(String), "status": String, "error_message": NullOr(String),
			"total_duration_ms": Num, "created_at": TimestampNaive,
			"spans": ArrayOf(Open{"name": String, "start_ms": Num, "duration_ms": Num}),
		})
		tg.Get(t, CommandCenter, "/api/v0/admin/traces/00000000-0000-4000-8000-000000000000", CCAdminH()).
			ExpectError(http.StatusNotFound, "Trace not found")
		tg.Get(t, CommandCenter, path, H{"X-API-Key": "wrong-admin-key"}).
			ExpectError(http.StatusUnauthorized, "Invalid Admin API Key")
	})
}
