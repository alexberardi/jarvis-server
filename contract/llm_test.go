//go:build contract

package contract

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// jarvis-llm-proxy-api (PLAN Phase 0 item 4: LLM stream frames; §3.2 enqueue/callback). The
// ground-truth consumer is command-center's core/llm_proxy_client.py. Against a real model the
// tests assert framing and key sets only, never content: small max_tokens, thinking off.
//
// The streaming endpoint is NOT OpenAI chunk format. Frames are `data: <json>\n\n` with
//   {"delta": "<tok>"}                                                  per token
//   {"done": true, "content", "usage", "tool_calls", "finish_reason"}   final
//   {"error": "<msg>"}  /  {"cancelled": true}                          terminal alternatives
// and there is no `data: [DONE]` sentinel.

// needLLM skips unless the proxy reports a loaded live model.
func needLLM(t *testing.T) *Target {
	t.Helper()
	tg := T(t)
	tg.Need(t, LLM)
	r := tg.Get(t, LLM, "/health")
	if r.Status != http.StatusOK {
		t.Skipf("llm-proxy /health is %d (no model loaded?): %s", r.Status, r.Body)
	}
	var h struct {
		Status string `json:"status"`
	}
	r.Decode(&h)
	if h.Status != "healthy" {
		t.Skipf("llm-proxy status %q: live model not ready", h.Status)
	}
	return tg
}

// skipIfNotLoaded skips on the model service's 503 model_not_loaded.
func skipIfNotLoaded(t *testing.T, r *Resp) {
	t.Helper()
	if r.Status == http.StatusServiceUnavailable {
		t.Skipf("model not loaded: %s", r.Body)
	}
}

func chatReq(prompt string, extra map[string]any) map[string]any {
	req := map[string]any{
		"model":            "live",
		"messages":         []map[string]any{{"role": "user", "content": prompt}},
		"temperature":      0,
		"max_tokens":       8,
		"reasoning_budget": 0, // thinking off: CC's background jobs do the same
	}
	for k, v := range extra {
		req[k] = v
	}
	return req
}

// openAIError is the proxy's error body: FastAPI HTTPException with a nested OpenAI error.
func openAIError(typ string, code Matcher) Matcher {
	return Obj{"detail": Obj{"error": Obj{"type": Eq(typ), "message": NonEmptyString, "code": code}}}
}

// chatCompletion is the non-stream ChatCompletionResponse. Every optional field is serialised
// (pydantic response_model), so absent values are null rather than missing.
func chatCompletion(model string, dateKeys Matcher) Matcher {
	return Obj{
		"id":      Regexp(`^chatcmpl-[0-9a-f]{8}$`),
		"object":  Eq("chat.completion"),
		"created": Int,
		"model":   Eq(model),
		"choices": All(NonEmptyArrayOf(Obj{
			"index": Eq(0),
			"message": Obj{
				"role":         Eq("assistant"),
				"content":      NullOr(String),
				"tool_calls":   Null,
				"tool_call_id": Null,
			},
			"finish_reason": OneOf("stop", "length"),
		}), arrayLen(1)),
		"usage":     Obj{"prompt_tokens": Int, "completion_tokens": Int, "total_tokens": Int},
		"date_keys": dateKeys,
	}
}

func arrayLen(n int) Matcher {
	return MatchFunc(func(path string, v any) []string {
		if a, ok := v.([]any); !ok || len(a) != n {
			return mismatch(path, fmt.Sprintf("array of length %d", n), v)
		}
		return nil
	})
}

func TestLLMChatCompletion(t *testing.T) {
	tg := needLLM(t)
	app := SharedApp(t)

	t.Run("live", func(t *testing.T) {
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions", chatReq("Reply with the word OK.", nil), app.H())
		skipIfNotLoaded(t, r.Resp)
		r.Expect(http.StatusOK, chatCompletion("live", Null))
	})

	t.Run("unknown model is forced to live", func(t *testing.T) {
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("Reply with the word OK.", map[string]any{"model": "gpt-4o"}), app.H())
		skipIfNotLoaded(t, r.Resp)
		r.Expect(http.StatusOK, chatCompletion("live", Null))
	})

	t.Run("background", func(t *testing.T) {
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("Reply with the word OK.", map[string]any{"model": "background"}), app.H())
		skipIfNotLoaded(t, r.Resp)
		r.Expect(http.StatusOK, chatCompletion("background", Null))
	})

	// date_keys is the Jarvis extension: present (an array, possibly empty) only when
	// include_date_context is true, which CC always sends; null otherwise.
	t.Run("date_keys", func(t *testing.T) {
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("Remind me tomorrow morning to call mom.", map[string]any{"include_date_context": true}), app.H())
		skipIfNotLoaded(t, r.Resp)
		r.Expect(http.StatusOK, chatCompletion("live", NonEmptyArrayOf(String)))
		var out struct {
			DateKeys []string `json:"date_keys"`
		}
		r.Decode(&out)
		if !contains(out.DateKeys, "tomorrow_morning") && !contains(out.DateKeys, "tomorrow") {
			r.Fatalf("date_keys %v: want the tomorrow/morning keys", out.DateKeys)
		}

		tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("Reply with the word OK.", map[string]any{"include_date_context": true}), app.H()).
			Expect(http.StatusOK, chatCompletion("live", Eq([]any{})))
		tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("Remind me tomorrow morning.", map[string]any{"include_date_context": false}), app.H()).
			Expect(http.StatusOK, chatCompletion("live", Null))
	})

	t.Run("validation", func(t *testing.T) {
		tg.Post(t, LLM, "/v1/chat/completions", map[string]any{"model": "live"}, app.H()).
			Expect(http.StatusUnprocessableEntity, ValidationError("body", "messages"))
		tg.Post(t, LLM, "/v1/chat/completions",
			map[string]any{"messages": []map[string]any{{"role": "user", "content": "hi"}}}, app.H()).
			Expect(http.StatusUnprocessableEntity, ValidationError("body", "model"))
	})
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// streamFinal is the terminal frame of a successful stream.
var streamFinal = Obj{
	"done":    Eq(true),
	"content": String,
	// LEGACY-BUG: the in-process GGUF backend sends "usage": {} (llama-cpp-python has no
	// usage in stream mode); the REST backend sends real counts. Go should always send
	// {prompt_tokens, completion_tokens, total_tokens}.
	"usage":         Object,
	"tool_calls":    Null, // LEGACY: tools are dropped on the streaming path (llm-proxy CLAUDE.md)
	"finish_reason": OneOf("stop", "length"),
}

var streamDelta = Obj{"delta": NonEmptyString}

func TestLLMChatStream(t *testing.T) {
	tg := needLLM(t)
	app := SharedApp(t)

	t.Run("frames", func(t *testing.T) {
		rid := "contract-" + randHex(8)
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("Count from one to five.", map[string]any{"stream": true, "max_tokens": 12, "include_date_context": true}),
			app.H(), H{"X-Request-Id": rid})
		skipIfNotLoaded(t, r.Resp)
		r.ExpectStatus(http.StatusOK).ExpectMediaType("text/event-stream")
		r.ExpectChunked()
		r.ExpectHeaderVal("X-Request-Id", rid)
		r.ExpectHeaderVal("Cache-Control", "no-cache")
		r.ExpectHeaderVal("X-Accel-Buffering", "no")

		frames, err := ParseSSE(r.Body)
		if err != nil {
			r.Fatalf("SSE framing: %v", err)
		}
		if len(frames) < 2 {
			r.Fatalf("want at least one delta and a final frame, got %d frames", len(frames))
		}
		var content strings.Builder
		for i, f := range frames[:len(frames)-1] {
			if errs := MatchFrame(f, fmt.Sprintf("frame[%d]", i), streamDelta); len(errs) > 0 {
				r.Fatalf("delta frame:\n  %s", strings.Join(errs, "\n  "))
			}
			content.WriteString(f.FrameObj()["delta"].(string))
		}
		last := frames[len(frames)-1]
		// The final frame has no date_keys even with include_date_context: the stream path
		// computes them and drops them. CC's stream consumer does not read them.
		if errs := MatchFrame(last, "final", streamFinal); len(errs) > 0 {
			r.Fatalf("final frame:\n  %s", strings.Join(errs, "\n  "))
		}
		if got := last.FrameObj()["content"].(string); got != content.String() {
			r.Fatalf("final content %q != concatenated deltas %q", got, content.String())
		}
		if strings.Contains(string(r.Body), "[DONE]") {
			r.Fatalf("stream carries an OpenAI [DONE] sentinel; it must not")
		}
		t.Logf("final frame: %s", last.Raw)
	})

	t.Run("generated request id", func(t *testing.T) {
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("Say hi.", map[string]any{"stream": true, "max_tokens": 2}), app.H())
		skipIfNotLoaded(t, r.Resp)
		r.ExpectStatus(http.StatusOK)
		if id := r.HeaderVal("X-Request-Id"); !hex32.MatchString(id) {
			r.Fatalf("generated X-Request-Id %q: want 32 hex chars (uuid4().hex)", id)
		}
		if _, err := ParseSSE(r.Body); err != nil {
			r.Fatalf("SSE framing: %v", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		rid := "contract-" + randHex(8)
		s := tg.OpenSSE(t, LLM, "/v1/chat/completions",
			chatReq("Count from 1 to 300, one number per line.", map[string]any{"stream": true, "max_tokens": 600}),
			app.H(), H{"X-Request-Id": rid})
		defer s.Close()
		if s.Res.StatusCode == http.StatusServiceUnavailable {
			t.Skip("model not loaded")
		}
		if s.Res.StatusCode != http.StatusOK {
			t.Fatalf("stream status %d", s.Res.StatusCode)
		}
		first, err := s.Next()
		if err != nil {
			t.Fatalf("first frame: %v", err)
		}
		if errs := MatchFrame(first, "frame[0]", streamDelta); len(errs) > 0 {
			t.Fatalf("first frame: %v", errs)
		}

		tg.Post(t, LLM, "/v1/chat/completions/cancel/"+rid, nil, app.H()).
			Expect(http.StatusOK, Obj{"status": Eq("cancelling"), "request_id": Eq(rid)})

		var last SSEFrame
		n := 1
		for {
			f, err := s.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("frame %d: %v", n, err)
			}
			last = f
			n++
		}
		if errs := MatchFrame(last, "last", Obj{"cancelled": Eq(true)}); len(errs) > 0 {
			t.Fatalf("a cancelled stream ends with {\"cancelled\": true} and no done frame: %v (raw %q)", errs, last.Raw)
		}

		// Once the stream is gone the id is unknown.
		waitGone := time.Now().Add(5 * time.Second)
		for {
			r := tg.Post(t, LLM, "/v1/chat/completions/cancel/"+rid, nil, app.H())
			if r.Status == http.StatusNotFound || time.Now().After(waitGone) {
				r.Expect(http.StatusNotFound, Obj{"detail": Obj{"error": Obj{
					"type":    Eq("not_found"),
					"message": Eq(fmt.Sprintf("No active stream with request id '%s'", rid)),
					"code":    Eq("request_not_found"),
				}}})
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
}

// TestLLMImageToTextModel sends an image to a model with no vision support, the one request
// the target rejects deterministically.
//
// The stream path's own error frame, {"error": "Model service error <status>"} (or
// {"error": "Model service connection error: …"}), cannot be triggered black-box: the model
// service only refuses a stream for an unloaded slot, a non-streaming backend or a bad internal
// token. It is documented in docs/contract/README.md instead.
func TestLLMImageToTextModel(t *testing.T) {
	tg := needLLM(t)
	app := SharedApp(t)
	var ml struct {
		Data []struct {
			SupportsImages *bool `json:"supports_images"`
		} `json:"data"`
	}
	tg.Get(t, LLM, "/v1/models").Decode(&ml)
	for _, m := range ml.Data {
		if m.SupportsImages != nil && *m.SupportsImages {
			t.Skip("target model supports images; no deterministic rejection to freeze")
		}
	}
	// 1x1 transparent PNG.
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	msgs := []map[string]any{{"role": "user", "content": []map[string]any{
		{"type": "text", "text": "What is this?"},
		{"type": "image_url", "image_url": map[string]any{"url": png}},
	}}}
	const unsupported = "Model 'live' does not support images. Use a vision-capable model instead."

	t.Run("non-stream", func(t *testing.T) {
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions", chatReq("", map[string]any{"messages": msgs}), app.H())
		skipIfNotLoaded(t, r.Resp)
		switch r.Status {
		case http.StatusBadRequest:
			// llm-proxy #86 (2026-10-03) propagates the model service's 4xx and its error.
			r.Expect(http.StatusBadRequest, Obj{"detail": Obj{"error": Obj{
				"type": Eq("invalid_request_error"), "message": Eq(unsupported), "code": Null,
			}}})
		default:
			// LEGACY-BUG: before #86 (still what the MBP runs) the gateway flattens the model
			// service's 400 to a 500 internal_server_error and stringifies the upstream body
			// into the message. Go answers 400 invalid_request_error.
			r.Expect(http.StatusInternalServerError, Obj{"detail": Obj{"error": Obj{
				"type":    Eq("internal_server_error"),
				"message": All(Regexp(`^Model service error 400: `), Regexp("does not support images")),
				"code":    Null,
			}}})
		}
	})

	t.Run("stream", func(t *testing.T) {
		// LEGACY-BUG: the streaming path does not check for images at all. It answers a normal
		// 200 stream (the image is dropped and the text is answered), where the non-stream path
		// refuses. Go should refuse both the same way.
		r := tg.SlowJSON(t, LLM, "/v1/chat/completions",
			chatReq("", map[string]any{"messages": msgs, "stream": true, "max_tokens": 2}), app.H())
		skipIfNotLoaded(t, r.Resp)
		if Jarvisd() {
			// D8 (docs/llm/02 §3.9): the image check runs before the stream opens, so the
			// stream refuses with the non-stream path's 400.
			r.Expect(http.StatusBadRequest, Obj{"detail": Obj{"error": Obj{
				"type": Eq("invalid_request_error"), "message": Eq(unsupported), "code": Null,
			}}})
			return
		}
		r.ExpectStatus(http.StatusOK).ExpectMediaType("text/event-stream")
		frames, err := ParseSSE(r.Body)
		if err != nil {
			r.Fatalf("SSE framing: %v", err)
		}
		if errs := MatchFrame(frames[len(frames)-1], "final", streamFinal); len(errs) > 0 {
			r.Fatalf("final frame: %v", errs)
		}
	})
}

func openAIErrorType(status int) string {
	switch {
	case status >= 500:
		return "internal_server_error"
	case status == 404:
		return "not_found_error"
	case status == 429:
		return "rate_limit_error"
	}
	return "invalid_request_error"
}

func TestLLMModels(t *testing.T) {
	tg := T(t)
	tg.Need(t, LLM)
	// No auth on /v1/models or /v1/engine. supports_images/context_length were added after the
	// MBP's running proxy started (llm-proxy #8x), so they may be absent.
	tg.Get(t, LLM, "/v1/models").Expect(http.StatusOK, Obj{
		"object": Eq("list"),
		"data": NonEmptyArrayOf(Obj{
			"id":              NonEmptyString,
			"object":          Eq("model"),
			"created":         Eq(0),
			"owned_by":        Eq("jarvis"),
			"supports_images": Optional(Bool),
			"context_length":  Optional(NullOr(Int)),
		}),
	})
}

// TestLLMEngine freezes GET /v1/engine, which CC reads at warmup for allows_caching.
func TestLLMEngine(t *testing.T) {
	tg := T(t)
	tg.Need(t, LLM)
	tg.Get(t, LLM, "/v1/engine").Expect(http.StatusOK, Obj{
		"inference_engine": NonEmptyString,
		"backend_type":     NonEmptyString,
		"allows_caching":   Bool,
		"description":      String,
	})
}

// TestLLMDateKeyVocabulary freezes GET /v1/adapters/date-keys, which CC fetches into
// node_context["date_keys"] (it reads static_keys). Unauthenticated by design.
func TestLLMDateKeyVocabulary(t *testing.T) {
	tg := T(t)
	tg.Need(t, LLM)
	tg.Get(t, LLM, "/v1/adapters/date-keys").Expect(http.StatusOK, Obj{
		"version":          String,
		"static_keys":      All(NonEmptyArrayOf(Regexp(`^[a-z0-9_]+$`)), containsStr("tomorrow"), containsStr("tomorrow_morning")),
		"dynamic_patterns": Any,
		"patterns":         Any,
		"notes":            Object,
		"adapter_trained":  Bool,
	})
}

func containsStr(s string) Matcher {
	return MatchFunc(func(path string, v any) []string {
		if a, ok := v.([]any); ok {
			for _, e := range a {
				if e == s {
					return nil
				}
			}
		}
		return mismatch(path, "array containing "+show(s), v)
	})
}

func TestLLMEmbeddings(t *testing.T) {
	tg := T(t)
	tg.Need(t, LLM)
	app := SharedApp(t)
	vec := NonEmptyArrayOf(Num)
	resp := func(n int) Matcher {
		return Obj{
			"object": Eq("list"),
			"data":   All(ArrayOf(Obj{"object": Eq("embedding"), "embedding": vec, "index": Int}), arrayLen(n)),
			"model":  NonEmptyString,
			"usage":  Obj{"prompt_tokens": Int, "total_tokens": Int},
		}
	}

	r := tg.SlowJSON(t, LLM, "/v1/embeddings", map[string]any{"input": []string{"turn on the lights", "what time is it"}}, app.H())
	r.Expect(http.StatusOK, resp(2))
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	r.Decode(&out)
	if out.Data[0].Index != 0 || out.Data[1].Index != 1 || len(out.Data[0].Embedding) != len(out.Data[1].Embedding) {
		r.Fatalf("embeddings: want indices 0,1 with equal dimensions")
	}
	t.Logf("embedding dim %d", len(out.Data[0].Embedding))

	tg.SlowJSON(t, LLM, "/v1/embeddings", map[string]any{"input": "a single string"}, app.H()).Expect(http.StatusOK, resp(1))
	tg.Post(t, LLM, "/v1/embeddings", map[string]any{"input": []string{}}, app.H()).Expect(http.StatusOK, Obj{
		"object": Eq("list"), "data": Eq([]any{}), "model": NonEmptyString,
		"usage": Obj{"prompt_tokens": Eq(0), "total_tokens": Eq(0)},
	})
	tg.Post(t, LLM, "/v1/embeddings", map[string]any{}, app.H()).
		Expect(http.StatusUnprocessableEntity, ValidationError("body", "input"))
}

// TestLLMQueueEnqueue freezes POST /internal/queue/enqueue (PLAN §3.2: enqueue and dedup
// semantics are preserved). A real job is accepted; its callback goes to a closed port on the
// target's own loopback, so it fails harmlessly. The callback envelope itself
// ({job_id, job_type, finished_at, status, result, error, timing, metadata}) is not frozen
// here: that needs a listener the target can reach.
func TestLLMQueueEnqueue(t *testing.T) {
	tg := needLLM(t)
	app := SharedApp(t)
	job := func(id string, mut func(map[string]any)) map[string]any {
		j := map[string]any{
			"job_id":           id,
			"job_type":         "chat",
			"created_at":       time.Now().UTC().Format(time.RFC3339),
			"priority":         "normal",
			"trace_id":         id,
			"idempotency_key":  id,
			"job_type_version": "v1",
			"ttl_seconds":      120,
			"metadata":         map[string]any{"type": "contract"},
			"request": map[string]any{
				"model":            "background",
				"messages":         []map[string]any{{"role": "user", "content": "Reply with the word OK."}},
				"sampling":         map[string]any{"temperature": 0, "max_tokens": 1},
				"reasoning_budget": 0,
			},
			"callback": map[string]any{"url": "http://127.0.0.1:9/contract-callback"},
		}
		if mut != nil {
			mut(j)
		}
		return j
	}
	accepted := func(id string, deduped bool) Matcher {
		return Obj{"accepted": Eq(true), "job_id": Eq(id), "deduped": Eq(deduped)}
	}

	id := "contract-" + tg.RunID + "-" + randHex(4)
	tg.Post(t, LLM, "/internal/queue/enqueue", job(id, nil), app.H()).Expect(http.StatusOK, accepted(id, false))
	// Same job_id + idempotency_key within the TTL: deduped, not enqueued again.
	tg.Post(t, LLM, "/internal/queue/enqueue", job(id, nil), app.H()).Expect(http.StatusOK, accepted(id, true))

	invalid := func(code string) Matcher {
		return Obj{"detail": Obj{"error": Obj{"type": Eq("invalid_request_error"), "message": NonEmptyString, "code": Eq(code)}}}
	}
	t.Run("expired", func(t *testing.T) {
		tg.Post(t, LLM, "/internal/queue/enqueue", job("contract-exp-"+randHex(4), func(j map[string]any) {
			j["created_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			j["ttl_seconds"] = 60
		}), app.H()).Expect(http.StatusBadRequest, Obj{"detail": Obj{"error": Obj{
			"type": Eq("invalid_request_error"), "message": Eq("Job already expired"), "code": Eq("expired"),
		}}})
	})
	t.Run("invalid request", func(t *testing.T) {
		tg.Post(t, LLM, "/internal/queue/enqueue", job("contract-bad-"+randHex(4), func(j map[string]any) {
			j["request"] = map[string]any{"model": "background"}
		}), app.H()).Expect(http.StatusBadRequest, invalid("invalid_request"))
	})
	t.Run("json_object without schema", func(t *testing.T) {
		tg.Post(t, LLM, "/internal/queue/enqueue", job("contract-sch-"+randHex(4), func(j map[string]any) {
			j["request"].(map[string]any)["response_format"] = map[string]any{"type": "json_object"}
		}), app.H()).Expect(http.StatusBadRequest, Obj{"detail": Obj{"error": Obj{
			"type": Eq("invalid_request_error"), "message": Eq("json_schema required when response_format.type=json_object"),
			"code": Eq("missing_schema"),
		}}})
	})
	t.Run("missing callback", func(t *testing.T) {
		tg.Post(t, LLM, "/internal/queue/enqueue", job("contract-cb-"+randHex(4), func(j map[string]any) {
			delete(j, "callback")
		}), app.H()).Expect(http.StatusUnprocessableEntity, ValidationError("body", "callback"))
	})
}

// TestLLMAppAuth freezes the proxy's own require_app_auth (auth/app_auth.py) on every
// app-authenticated route CC calls.
func TestLLMAppAuth(t *testing.T) {
	tg := T(t)
	tg.Need(t, LLM)
	app := SharedApp(t)
	bad := H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong-" + randHex(8)}
	for _, path := range []string{
		"/v1/chat/completions",
		"/v1/chat/completions/cancel/contract-x",
		"/v1/embeddings",
		"/internal/queue/enqueue",
	} {
		t.Run(path, func(t *testing.T) {
			tg.Post(t, LLM, path, map[string]any{}).ExpectError(http.StatusUnauthorized, "Missing app credentials")
			tg.Post(t, LLM, path, map[string]any{}, bad).ExpectError(http.StatusUnauthorized, "Invalid app credentials")
		})
	}
}
