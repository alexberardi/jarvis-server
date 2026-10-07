package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The HTTP API on the llm listener (7704): the frozen legacy contract (contract/llm_test.go,
// docs/llm/02), as thin adapters over Service.

// writeJSON writes v without encoding/json's HTML escaping (FastAPI doesn't escape <>&).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// writeAPIError writes {"detail":{"error":{"type","message","code",...}}}.
func writeAPIError(w http.ResponseWriter, ae *APIError) {
	e := map[string]any{"type": ae.Type, "message": ae.Message, "code": ae.Code}
	for k, v := range ae.Extra {
		e[k] = v
	}
	writeJSON(w, ae.Status, map[string]any{"detail": map[string]any{"error": e}})
}

// app is the proxy's require_app_auth, checked in-process.
func (m *Module) app(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key, ok := authn.AppCreds(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "Missing app credentials")
			return
		}
		if m.Auth == nil {
			httpx.Error(w, http.StatusBadGateway, "Auth service unavailable")
			return
		}
		app, valid, err := m.Auth.ValidateApp(r.Context(), id, key)
		if err != nil {
			m.log().Error("llm: app auth unavailable", "err", err)
			httpx.Error(w, http.StatusBadGateway, "Auth service unavailable")
			return
		}
		if !valid {
			httpx.Error(w, http.StatusUnauthorized, "Invalid app credentials")
			return
		}
		h(w, r.WithContext(authn.WithApp(r.Context(), app)))
	}
}

// --- chat ---

type chatMessageJSON struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls"`
	ToolCallID *string    `json:"tool_call_id"`
}

type chatChoiceJSON struct {
	Index        int             `json:"index"`
	Message      chatMessageJSON `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

// chatCompletionJSON is ChatCompletionResponse: every optional is serialised (null).
type chatCompletionJSON struct {
	ID       string           `json:"id"`
	Object   string           `json:"object"`
	Created  int64            `json:"created"`
	Model    string           `json:"model"`
	Choices  []chatChoiceJSON `json:"choices"`
	Usage    Usage            `json:"usage"`
	DateKeys []string         `json:"date_keys"`
}

func (m *Module) handleChat(w http.ResponseWriter, r *http.Request) {
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	in, errs := parseChat(body)
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	rid := r.Header.Get("X-Request-Id")
	if rid == "" {
		rid = newHex(32) // uuid4().hex
	}
	in.Req.RequestID = rid
	svc := m.Service()
	if in.Stream {
		m.serveStream(w, r, in.Req, rid)
		return
	}
	resp, err := svc.Chat(r.Context(), in.Req)
	if err != nil {
		writeAPIError(w, asAPIError(err))
		return
	}
	content := resp.Content
	out := chatCompletionJSON{
		ID: "chatcmpl-" + newHex(8), Object: "chat.completion", Created: time.Now().Unix(), Model: in.Req.Label,
		Choices: []chatChoiceJSON{{Index: 0, Message: chatMessageJSON{Role: "assistant", Content: &content, ToolCalls: resp.ToolCalls},
			FinishReason: resp.FinishReason}},
		Usage:    resp.Usage,
		DateKeys: resp.DateKeys,
	}
	w.Header().Set("X-Request-Id", rid)
	writeJSON(w, http.StatusOK, out)
}

// frameJSON is a Jarvis stream frame as Python's json.dumps wrote it: ", "/": " separators
// and ensure_ascii, so a byte-diffing replay stays clean (02 §3.2).
func frameJSON(f Frame) string {
	o := pyjson.NewObject()
	switch {
	case f.Cancelled:
		o.Set("cancelled", true)
	case f.Err != "":
		o.Set("error", f.Err)
	case f.Done:
		o.Set("done", true)
		o.Set("content", f.Content)
		u := pyjson.NewObject()
		u.Set("prompt_tokens", f.Usage.PromptTokens)
		u.Set("completion_tokens", f.Usage.CompletionTokens)
		u.Set("total_tokens", f.Usage.TotalTokens)
		o.Set("usage", u)
		if len(f.ToolCalls) == 0 {
			o.Set("tool_calls", nil)
		} else {
			calls := make([]any, len(f.ToolCalls))
			for i, tc := range f.ToolCalls {
				c := pyjson.NewObject()
				if tc.ID != nil {
					c.Set("id", *tc.ID)
				} else {
					c.Set("id", nil)
				}
				c.Set("type", tc.Type)
				fn := pyjson.NewObject()
				if tc.Name != nil {
					fn.Set("name", *tc.Name)
				} else {
					fn.Set("name", nil)
				}
				fn.Set("arguments", tc.Arguments)
				c.Set("function", fn)
				calls[i] = c
			}
			o.Set("tool_calls", calls)
		}
		o.Set("finish_reason", f.FinishReason)
	default:
		o.Set("delta", f.Delta)
	}
	return pyjson.Dumps(o, true)
}

// serveStream answers stream:true. Failures found before generation (label not ready, images)
// are real error statuses (D8); after the 200 every failure is an {"error"} frame.
func (m *Module) serveStream(w http.ResponseWriter, r *http.Request, req ChatRequest, rid string) {
	frames, err := m.Service().Stream(r.Context(), req)
	if err != nil {
		writeAPIError(w, asAPIError(err))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Request-Id", rid)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	for f := range frames {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", frameJSON(f)); err != nil {
			continue // drain; the stream's context ends with the request
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (m *Module) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("request_id")
	if !m.Service().Cancel(id) {
		writeAPIError(w, &APIError{Status: http.StatusNotFound, Type: "not_found",
			Message: fmt.Sprintf("No active stream with request id '%s'", id), Code: "request_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelling", "request_id": id})
}

// --- embeddings ---

type embeddingJSON struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

func (m *Module) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	c := &checker{}
	loc := []any{"body"}
	var texts []string
	iv, present := body.Get("input")
	switch x := iv.(type) {
	case string:
		texts = []string{x}
	case []any:
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				c.fail("string_type", "Input should be a valid string", e, at(at(loc, "input"), i))
				continue
			}
			texts = append(texts, s)
		}
	default:
		if !present {
			c.fail("missing", "Field required", body, at(loc, "input"))
		} else {
			c.fail("string_type", "Input should be a valid string", iv, at(loc, "input"))
		}
	}
	model := "all-MiniLM-L6-v2"
	if s, ok := c.str(body, "model", false, loc); ok {
		model = s
	}
	if len(c.errs) > 0 {
		httpx.ValidationError(w, c.errs...)
		return
	}
	usage := func(n int) map[string]int { return map[string]int{"prompt_tokens": n, "total_tokens": n} }
	if len(texts) == 0 {
		// The legacy echo of the request's model on empty input (02 §8.4).
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{}, "model": model, "usage": usage(0)})
		return
	}
	res, err := m.Service().Embed(r.Context(), texts)
	if err != nil {
		writeAPIError(w, asAPIError(err))
		return
	}
	chars := 0
	for _, t := range texts {
		chars += utf8.RuneCountInString(t)
	}
	data := make([]embeddingJSON, len(res.Vectors))
	for i, v := range res.Vectors {
		data[i] = embeddingJSON{Object: "embedding", Embedding: v, Index: i}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "model": res.Model, "usage": usage(max(1, chars/4))})
}

// --- discovery ---

type modelJSON struct {
	ID             string `json:"id"`
	Object         string `json:"object"`
	Created        int    `json:"created"`
	OwnedBy        string `json:"owned_by"`
	SupportsImages bool   `json:"supports_images"`
	ContextLength  *int   `json:"context_length"`
}

// handleModels lists one entry per distinct engine behind the chat labels.
func (m *Module) handleModels(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{}
	data := []modelJSON{}
	for _, label := range []string{LabelLive, LabelBackground} {
		ep, _ := m.resolve(r, label)
		if ep.Model == "" {
			continue
		}
		key := ep.BaseURL + "|" + ep.Model
		if seen[key] {
			continue
		}
		seen[key] = true
		var cl *int
		if ep.ContextLength > 0 {
			n := ep.ContextLength
			cl = &n
		}
		data = append(data, modelJSON{ID: ep.Model, Object: "model", OwnedBy: "jarvis", SupportsImages: ep.Vision, ContextLength: cl})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (m *Module) resolve(r *http.Request, label string) (Endpoint, error) {
	svc := m.Service()
	if svc.resolver == nil {
		return Endpoint{}, &NotReadyError{State: StateNotConfigured}
	}
	return svc.resolver.Resolve(r.Context(), label)
}

// handleEngine describes the live label's engine; CC reads allows_caching.
func (m *Module) handleEngine(w http.ResponseWriter, r *http.Request) {
	ep, err := m.resolve(r, LabelLive)
	out := map[string]any{"inference_engine": "llama_cpp", "backend_type": "LLAMA_SERVER", "allows_caching": true,
		"description": "llama-server (local engine)"}
	switch {
	case err != nil && ep.Model == "" && ep.BaseURL == "":
		out = map[string]any{"inference_engine": "none", "backend_type": "NONE", "allows_caching": false,
			"description": "No live model configured"}
	case ep.Remote:
		host := ep.BaseURL
		if u, err := url.Parse(ep.BaseURL); err == nil && u.Host != "" {
			host = u.Host
		}
		out = map[string]any{"inference_engine": "rest", "backend_type": "REST", "allows_caching": true,
			"description": "Remote OpenAI-compatible endpoint (" + host + ")"}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDateKeys is GET /v1/adapters/date-keys: the vocabulary (the path keeps its "adapters"
// name for node-setup; LoRA is cut, so adapter_trained is always false).
func (m *Module) handleDateKeys(w http.ResponseWriter, _ *http.Request) {
	o := pyjson.NewObject()
	o.Set("version", dates.Version)
	keys := make([]any, len(dates.Vocabulary))
	for i, k := range dates.Vocabulary {
		keys[i] = k
	}
	o.Set("static_keys", keys)
	dyn := make([]any, len(dates.DynamicPatterns))
	for i, d := range dates.DynamicPatterns {
		e := pyjson.NewObject()
		e.Set("pattern", d.Pattern)
		e.Set("regex", d.Regex)
		e.Set("description", d.Description)
		dyn[i] = e
	}
	o.Set("dynamic_patterns", dyn)
	kv := func(list []dates.KV) *pyjson.Object {
		x := pyjson.NewObject()
		for _, p := range list {
			x.Set(p.Key, p.Value)
		}
		return x
	}
	o.Set("patterns", kv(dates.TimePatterns))
	o.Set("notes", kv(dates.Notes))
	o.Set("adapter_trained", false)
	writeJSON(w, http.StatusOK, o)
}

// --- queue ---

func (m *Module) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	in, errs := parseEnqueue(body)
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	invalid := func(msg, code string) {
		writeAPIError(w, &APIError{Status: 400, Type: "invalid_request_error", Message: msg, Code: code})
	}
	if strings.ToLower(in.JobType) == "adapter_train" {
		invalid("Unsupported job_type 'adapter_train': LoRA training is not part of jarvisd", "unsupported_job_type")
		return
	}
	req, err := parseQueueRequest(in.Request)
	if err != nil {
		invalid("Invalid request: "+err.Error(), "invalid_request")
		return
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" && len(req.ResponseFormat.JSONSchema) == 0 {
		invalid("json_schema required when response_format.type=json_object", "missing_schema")
		return
	}
	svc := m.Service()
	now := svc.now()
	created := parseCreatedAt(in.CreatedAt, now)
	ttl := time.Duration(in.TTLSeconds) * time.Second
	if created.Add(ttl).Before(now) {
		writeAPIError(w, errExpired())
		return
	}
	cb := in.Callback
	id, deduped, err := svc.Enqueue(r.Context(), Job{
		JobID: in.JobID, IdempotencyKey: in.IdempotencyKey, JobType: in.JobType, TraceID: in.TraceID,
		CreatedAt: created, TTL: ttl, Request: req, Metadata: in.Metadata, Callback: &cb,
	})
	if err != nil {
		ae, isAPI := err.(*APIError)
		if !isAPI {
			m.log().Error("llm: enqueue", "job_id", in.JobID, "err", err)
			ae = &APIError{Status: 500, Type: "internal_server_error", Message: "Failed to enqueue job: " + err.Error(), Code: "enqueue_failed"}
		}
		writeAPIError(w, ae)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "job_id": id, "deduped": deduped})
}
