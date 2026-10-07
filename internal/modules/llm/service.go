package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/jsonmode"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Service is the llm module's in-process API (02 §11): CC, OCR and phone call it directly;
// the HTTP handlers on the llm listener are thin adapters over it.
type Service struct {
	resolver Resolver
	client   *http.Client
	settings *settings.Service
	log      *slog.Logger
	clockMu  sync.RWMutex
	nowFn    func() time.Time

	// bg caps concurrent background calls (sync, streamed and queued alike): "one background
	// LLM call at a time" by default (03 §11, PLAN §3.2).
	bg chan struct{}

	mu      sync.Mutex
	streams map[string]*streamHandle

	db       *db.DB
	queue    *queue.Queue
	nmu      sync.RWMutex
	handlers map[string]NotifyFunc
}

// ServiceConfig builds a Service.
type ServiceConfig struct {
	Resolver Resolver
	// HTTPClient talks to engines; nil uses a client without an overall timeout (streams are
	// long; non-stream calls are bounded by llm.request_timeout_seconds).
	HTTPClient *http.Client
	// Settings supplies the label defaults; nil uses the definitions' defaults.
	Settings *settings.Service
	Log      *slog.Logger
	// BackgroundParallel caps concurrent background calls; default 1.
	BackgroundParallel int
	// DB and Queue enable Enqueue (the durable job path).
	DB    *db.DB
	Queue *queue.Queue
}

// NewService builds a Service.
func NewService(c ServiceConfig) *Service {
	s := &Service{streams: map[string]*streamHandle{}, handlers: map[string]NotifyFunc{}}
	s.configure(c)
	return s
}

func (s *Service) configure(c ServiceConfig) {
	s.resolver = c.Resolver
	s.client = c.HTTPClient
	s.settings = c.Settings
	s.log = c.Log
	if s.log == nil {
		s.log = slog.Default()
	}
	n := c.BackgroundParallel
	if n <= 0 {
		n = 1
	}
	s.bg = make(chan struct{}, n)
	s.db, s.queue = c.DB, c.Queue
}

// setNow swaps the clock (tests).
func (s *Service) setNow(f func() time.Time) {
	s.clockMu.Lock()
	s.nowFn = f
	s.clockMu.Unlock()
}

func (s *Service) now() time.Time {
	s.clockMu.RLock()
	f := s.nowFn
	s.clockMu.RUnlock()
	if f == nil {
		return time.Now()
	}
	return f()
}

func (s *Service) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return http.DefaultClient
}

// --- settings ---

func (s *Service) setting(ctx context.Context, key string) string {
	if s.settings != nil {
		return s.settings.String(ctx, key, settings.Scope{})
	}
	for _, d := range Definitions {
		if d.Key == key {
			return fmt.Sprint(d.Default)
		}
	}
	return ""
}

// labelBudget is the label's default thinking budget (llm.<label>.reasoning_budget).
// "server" (or anything unparsable) means send nothing, leaving the engine's launch flags in
// charge: the legacy blank, which the platform settings can't store (empty means default).
func (s *Service) labelBudget(ctx context.Context, label string) *int {
	raw := strings.TrimSpace(s.setting(ctx, "llm."+label+".reasoning_budget"))
	if raw == "" || raw == "None" || strings.EqualFold(raw, "server") {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &n
}

func (s *Service) requestTimeout(ctx context.Context) time.Duration {
	n, err := strconv.Atoi(s.setting(ctx, "llm.request_timeout_seconds"))
	if err != nil || n <= 0 {
		n = 240
	}
	return time.Duration(n) * time.Second
}

// --- labels ---

// echoLabel is the model name the legacy API echoed: "live"/"background" as sent (any case),
// anything else forced to "live".
func echoLabel(model string) string {
	if strings.EqualFold(model, LabelLive) || strings.EqualFold(model, LabelBackground) {
		return model
	}
	return LabelLive
}

// notLoaded is the model service's 503 (model_not_loaded).
func notLoaded(label string, err error) *APIError {
	state, reason := "unavailable", err.Error()
	var nr *NotReadyError
	if errors.As(err, &nr) {
		state, reason = nr.State, nr.Reason
	}
	if reason == "" {
		reason = "not loaded yet"
	}
	st := map[string]any{"status": state}
	if nr != nil && nr.Reason != "" {
		st["error"] = nr.Reason
	}
	return &APIError{Status: http.StatusServiceUnavailable, Type: "model_not_loaded",
		Message: fmt.Sprintf("%s model is %s: %s", label, state, reason), Code: "model_not_loaded",
		Extra: map[string]any{"slot": label, "state": st}}
}

func (s *Service) endpoint(ctx context.Context, label string) (Endpoint, error) {
	if s.resolver == nil {
		return Endpoint{}, notLoaded(label, &NotReadyError{State: StateNotConfigured, Reason: "no engine resolver"})
	}
	ep, err := s.resolver.Resolve(ctx, label)
	if err != nil {
		return ep, notLoaded(label, err)
	}
	return ep, nil
}

func (s *Service) acquire(ctx context.Context, label string) (func(), error) {
	if label != LabelBackground {
		return func() {}, nil
	}
	select {
	case s.bg <- struct{}{}:
		return func() { <-s.bg }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- message checks ---

var dataURL = regexp.MustCompile(`^data:([^;]+);base64,(.+)$`)

// checkMessages is normalize_messages' validation: image parts must be data URLs carrying
// base64. It reports whether any image is present.
func checkMessages(msgs []Message) (bool, error) {
	images := false
	for _, m := range msgs {
		if m.Content == nil {
			continue
		}
		for _, p := range m.Content.Parts {
			if p.Type != "image_url" || p.ImageURL == nil {
				continue
			}
			images = true
			u := p.ImageURL.URL
			if !strings.HasPrefix(u, "data:") {
				return true, apiErr(400, "invalid_request_error", "Only data URLs are supported for images. HTTP(S) URLs are not yet supported.")
			}
			sub := dataURL.FindStringSubmatch(strings.TrimSuffix(u, "\n"))
			if sub == nil {
				return true, apiErr(400, "invalid_request_error", "Invalid data URL format. Expected: data:<mime_type>;base64,<data>")
			}
			if _, err := base64.StdEncoding.DecodeString(stripB64(sub[2])); err != nil {
				return true, apiErr(400, "invalid_request_error", "Invalid base64 image data: "+err.Error())
			}
		}
	}
	return images, nil
}

// stripB64 drops what Python's lenient b64decode discards (anything outside the alphabet).
func stripB64(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
			return r
		}
		return -1
	}, s)
}

// userText is _get_user_text: the last user message's text (its first text part when
// structured), skipping user messages that have none.
func userText(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "user" || m.Content == nil {
			continue
		}
		if m.Content.Text != nil {
			return *m.Content.Text
		}
		for _, p := range m.Content.Parts {
			if p.Type == "text" {
				return p.Text
			}
		}
	}
	return ""
}

// injectJSON is inject_json_system_message.
func injectJSON(msgs []Message) []Message {
	hasSystem := false
	for _, m := range msgs {
		if m.Role == "system" {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		return append([]Message{{Role: "system", Content: TextContent(jsonmode.SystemMessage)}}, msgs...)
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		if m.Role != "system" {
			out[i] = m
			continue
		}
		var parts []string
		switch {
		case m.Content == nil:
			parts = []string{""}
		case m.Content.Text != nil:
			parts = []string{*m.Content.Text}
		default:
			for _, p := range m.Content.Parts {
				if p.Type == "text" {
					parts = append(parts, p.Text)
				}
			}
		}
		out[i] = Message{Role: "system", Content: TextContent(jsonmode.AugmentSystem(parts))}
	}
	return out
}

// --- request shaping ---

const defaultTemperature = 0.7

func (s *Service) payload(ctx context.Context, ep Endpoint, label string, req *ChatRequest, msgs []Message, stream bool) *chatPayload {
	t := defaultTemperature
	if req.Temperature != nil {
		t = *req.Temperature // D8: an explicit 0 is honoured (legacy `or 0.7` made it 0.7)
	}
	p := &chatPayload{Model: ep.Model, Messages: msgs, Temperature: t, Stream: stream,
		MaxTokens: req.MaxTokens, TopP: req.TopP, Seed: req.Seed}
	if stream {
		p.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if len(req.Tools) > 0 {
		p.Tools = req.Tools
		if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
			p.ToolChoice = req.ToolChoice
		}
	}
	p.applyThinking(s.budget(ctx, label, req))
	return p
}

// budget is the effective thinking budget: the request's, else the label default (LD8).
func (s *Service) budget(ctx context.Context, label string, req *ChatRequest) *int {
	if req.ReasoningBudget != nil {
		return req.ReasoningBudget
	}
	return s.labelBudget(ctx, label)
}

func jsonRequested(req *ChatRequest) bool {
	return req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object"
}

// schemaOf decodes response_format.json_schema with Python semantics (key order matters to
// the validator's first error). Absent or undecodable means no schema.
func schemaOf(req *ChatRequest) any {
	if req.ResponseFormat == nil || len(req.ResponseFormat.JSONSchema) == 0 {
		return nil
	}
	v, err := pyjson.Loads(string(req.ResponseFormat.JSONSchema))
	if err != nil {
		return nil
	}
	return v
}

// prepare runs the checks shared by Chat and Stream: label readiness (503), image URLs (400)
// and image capability (400, LD4: no fallback to another label).
func (s *Service) prepare(ctx context.Context, req *ChatRequest) (string, Endpoint, []Message, error) {
	label := NormalizeLabel(req.Label)
	ep, err := s.endpoint(ctx, label)
	if err != nil {
		return label, ep, nil, err
	}
	images, err := checkMessages(req.Messages)
	if err != nil {
		return label, ep, nil, err
	}
	if images && !ep.Vision {
		return label, ep, nil, apiErr(400, "invalid_request_error",
			fmt.Sprintf("Model '%s' does not support images. Use a vision-capable model instead.", echoLabel(req.Label)))
	}
	msgs := req.Messages
	if jsonRequested(req) {
		msgs = injectJSON(msgs)
	}
	return label, ep, msgs, nil
}

// Chat runs a non-stream completion: JSON mode repair and retry, thinking control, tools and
// date keys (02 §3.1, 04).
func (s *Service) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	label, ep, msgs, err := s.prepare(ctx, &req)
	if err != nil {
		return nil, err
	}
	release, err := s.acquire(ctx, label)
	if err != nil {
		return nil, err
	}
	defer release()

	cctx, cancel := context.WithTimeout(ctx, s.requestTimeout(ctx))
	defer cancel()
	p := s.payload(ctx, ep, label, &req, msgs, false)
	c, err := s.complete(cctx, ep, p)
	if err != nil {
		return nil, s.upstreamErr(err)
	}
	if len(req.Tools) == 0 {
		c.Content = jsonmode.PyStrip(c.Content) // the legacy text and vision paths strip
	}
	resp := &ChatResponse{Content: c.Content, ToolCalls: c.ToolCalls, FinishReason: c.FinishReason, Usage: c.Usage}
	if resp.FinishReason == "" {
		resp.FinishReason = "stop"
		if len(resp.ToolCalls) > 0 {
			resp.FinishReason = "tool_calls"
		}
	}
	if req.WantDateKeys {
		resp.DateKeys = dates.Extract(userText(req.Messages)) // [] for no text (04 §8.4)
	}
	if len(c.ToolCalls) > 0 || !jsonRequested(&req) {
		return resp, nil
	}

	// JSON mode (run_chat_completion): repair, validate, one correction retry.
	schema := schemaOf(&req)
	final := c.Content
	repaired, valid := jsonmode.Parse(final)
	schemaErr := ""
	if valid {
		if schema != nil && valueTruthy(schema) {
			v, _ := pyjson.Loads(repaired)
			e, perr := jsonmode.Validate(v, schema)
			switch {
			case perr != nil:
				schemaErr = "invalid JSON: " + pyErrMsg(perr)
			case e != "":
				schemaErr = e
			default:
				final = repaired
			}
		} else {
			final = repaired
		}
	}
	if !valid || schemaErr != "" {
		fixed, ok, err := s.retryJSON(cctx, ep, label, &req, msgs, final, schema)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apiErr(500, "invalid_response_error",
				strings.TrimSpace("Model returned invalid JSON response. "+schemaErr))
		}
		final = fixed
	}
	resp.Content = final
	return resp, nil
}

func pyErrMsg(err error) string {
	var pe *jsonmode.PyError
	if errors.As(err, &pe) {
		return pe.Msg
	}
	return err.Error()
}

func valueTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case *pyjson.Object:
		return x.Len() > 0
	case []any:
		return len(x) > 0
	case string:
		return x != ""
	case bool:
		return x
	}
	return true
}

// retryJSON is fix_json_with_retry with max_retries=1: a correction turn, a cooler and
// longer generation with the same thinking budget, no tools.
func (s *Service) retryJSON(ctx context.Context, ep Endpoint, label string, req *ChatRequest, msgs []Message, invalid string, schema any) (string, bool, error) {
	turn, err := jsonmode.CorrectionTurn(invalid, schema)
	if err != nil {
		return "", false, apiErr(500, "internal_server_error", "Internal error: "+pyErrMsg(err))
	}
	retry := append(append([]Message{}, msgs...), Message{Role: "user", Content: TextContent(turn)})
	t := defaultTemperature
	if req.Temperature != nil {
		t = *req.Temperature
	}
	orig := 0
	if req.MaxTokens != nil {
		orig = *req.MaxTokens
	}
	mt := jsonmode.RetryMaxTokens(orig, invalid)
	p := &chatPayload{Model: ep.Model, Messages: retry, Temperature: jsonmode.RetryTemperature(t),
		MaxTokens: &mt, TopP: req.TopP, Seed: req.Seed}
	p.applyThinking(s.budget(ctx, label, req))
	s.log.Warn("llm: JSON invalid, retrying once", "label", label)
	c, err := s.complete(ctx, ep, p)
	if err != nil {
		return "", false, s.upstreamErr(err)
	}
	fixed, valid := jsonmode.Parse(jsonmode.PyStrip(c.Content))
	if !valid {
		return "", false, nil
	}
	if schema != nil && valueTruthy(schema) {
		v, _ := pyjson.Loads(fixed)
		e, perr := jsonmode.Validate(v, schema)
		var pe *jsonmode.PyError
		if perr != nil && errors.As(perr, &pe) && pe.Type == "AttributeError" {
			// Not one of the exceptions the legacy retry caught: it surfaced as a 500.
			return "", false, apiErr(500, "internal_server_error", "Internal error: "+pe.Msg)
		}
		if perr != nil || e != "" {
			return "", false, nil
		}
	}
	return fixed, true, nil
}

// upstreamErr keeps engine statuses (APIError) and wraps transport failures as the legacy
// 500 "Internal error: …".
func (s *Service) upstreamErr(err error) error {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return apiErr(500, "internal_server_error", "Internal error: "+err.Error())
}

// --- streaming ---

// errCancelled marks a stream stopped by Cancel (or superseded by a newer stream with the
// same request id); it ends with {"cancelled": true}.
var errCancelled = errors.New("llm: stream cancelled")

type streamHandle struct {
	cancel context.CancelCauseFunc
}

// Stream starts a streamed completion. Checks that fail before generation (label not ready,
// images) return an error, so the HTTP layer can answer a real status (D8). After that,
// frames arrive on the channel: deltas, then exactly one of done, error or cancelled. The
// channel closes after the last frame, or early when ctx ends (client gone). Thinking, tools,
// the image check and the JSON instruction apply exactly as on Chat (D8); JSON repair can't,
// since the deltas are already out, and date keys are never emitted on a stream (frozen).
func (s *Service) Stream(ctx context.Context, req ChatRequest) (<-chan Frame, error) {
	label, ep, msgs, err := s.prepare(ctx, &req)
	if err != nil {
		return nil, err
	}
	p := s.payload(ctx, ep, label, &req, msgs, true)
	rid := req.RequestID
	if rid == "" {
		rid = newHex(32)
	}
	sctx, cancel := context.WithCancelCause(ctx)
	h := &streamHandle{cancel: cancel}
	s.mu.Lock()
	if prior := s.streams[rid]; prior != nil {
		prior.cancel(errCancelled) // supersede: a retry with a stable id wins
	}
	s.streams[rid] = h
	s.mu.Unlock()

	ch := make(chan Frame, 64)
	go func() {
		defer close(ch)
		defer func() {
			s.mu.Lock()
			if s.streams[rid] == h {
				delete(s.streams, rid)
			}
			s.mu.Unlock()
			cancel(nil)
		}()
		send := func(f Frame) bool {
			select {
			case ch <- f:
				return true
			case <-ctx.Done():
				return false
			}
		}
		res, err := s.runStream(sctx, ep, label, p, func(d string) bool {
			select {
			case ch <- Frame{Delta: d}:
				return true
			case <-sctx.Done():
				return false
			}
		})
		switch {
		case errors.Is(context.Cause(sctx), errCancelled):
			send(Frame{Cancelled: true})
		case ctx.Err() != nil:
			// The caller is gone; nobody reads a terminal frame.
		case err != nil:
			send(Frame{Err: streamErrMsg(err)})
		default:
			send(Frame{Done: true, Content: res.Content, Usage: res.Usage, ToolCalls: res.ToolCalls, FinishReason: res.FinishReason})
		}
	}()
	return ch, nil
}

func (s *Service) runStream(ctx context.Context, ep Endpoint, label string, p *chatPayload, onDelta func(string) bool) (streamResult, error) {
	release, err := s.acquire(ctx, label)
	if err != nil {
		return streamResult{}, err
	}
	defer release()
	resp, err := s.post(ctx, ep, "/chat/completions", p)
	if err != nil {
		return streamResult{}, err
	}
	defer resp.Body.Close()
	return parseStream(resp.Body, onDelta)
}

func streamErrMsg(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return fmt.Sprintf("HTTP %d: %s", ae.Status, ae.Message)
	}
	return err.Error()
}

// Cancel stops the stream registered under requestID; false when none is active.
func (s *Service) Cancel(requestID string) bool {
	s.mu.Lock()
	h := s.streams[requestID]
	s.mu.Unlock()
	if h == nil {
		return false
	}
	h.cancel(errCancelled)
	return true
}

// --- embeddings ---

// Embeddings is the result of Embed.
type Embeddings struct {
	Vectors [][]float64
	// Model is the embedding model id; stored vectors are tagged with it (LD6).
	Model string
}

// Embed returns one L2-normalised vector per text, in order.
func (s *Service) Embed(ctx context.Context, texts []string) (Embeddings, error) {
	ep, err := s.endpoint(ctx, LabelEmbeddings)
	if err != nil {
		return Embeddings{}, err
	}
	if !ep.Embeddings {
		return Embeddings{}, notLoaded(LabelEmbeddings, &NotReadyError{State: StateNotConfigured, Reason: "no embeddings engine"})
	}
	if len(texts) == 0 {
		return Embeddings{Vectors: [][]float64{}, Model: ep.Model}, nil
	}
	cctx, cancel := context.WithTimeout(ctx, s.requestTimeout(ctx))
	defer cancel()
	vecs, err := s.embed(cctx, ep, texts)
	if err != nil {
		return Embeddings{}, s.upstreamErr(err)
	}
	return Embeddings{Vectors: vecs, Model: ep.Model}, nil
}
