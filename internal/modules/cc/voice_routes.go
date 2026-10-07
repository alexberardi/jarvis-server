package cc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The voice routes (docs/cc/01 §2): blocking /voice/command and /voice/command/continue (the
// node's follow-up contract, F2), the streaming twins with the 200 audio/raw vs 202 JSON
// split, /voice/acknowledge and /wake-response. Status codes and bodies the frozen node
// depends on are kept, quirks included (D8, 01 §8.6).

const notInitialized = "Conversation not initialized for tool-based flow"

// parseTurn reads VoiceCommandRequest.
func parseTurn(w http.ResponseWriter, r *http.Request) (turnInput, bool) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return turnInput{}, false
	}
	var in turnInput
	in.VoiceCommand, _ = b.str("voice_command", true)
	in.ConversationID, _ = b.str("conversation_id", true)
	b.integer("speaker_user_id", false) // validated and ignored (D2)
	if v, ok := b.number("pre_wake_speech_seconds"); ok {
		in.PreWakeSeconds = &v
	}
	in.Affect, _ = b.object("affect", false)
	if s, ok := b.optStrPtr("turn_source"); ok && s != nil {
		in.Source = *s
	}
	if v, ok := b.number("wake_confidence"); ok {
		in.WakeConfidence = &v
	}
	if v, ok := b.integer("follow_up_iteration", false); ok {
		i := int(v)
		in.FollowUpIteration = &i
	}
	if v, ok := b.boolean("self_playback"); ok {
		in.SelfPlayback = &v
	}
	if s, ok := b.optStrPtr("self_playback_kind"); ok && s != nil {
		in.SelfPlaybackKind = *s
	}
	if !b.done(w) {
		return turnInput{}, false
	}
	return in, true
}

// parseToolResults reads ToolResultRequest, keeping each output as an ordered value.
func parseToolResults(w http.ResponseWriter, r *http.Request) (string, []toolResult, bool) {
	b, ordered, ok := readJSONBody(w, r)
	if !ok {
		return "", nil, false
	}
	cid, _ := b.str("conversation_id", true)
	var results []toolResult
	v, present := b.m["tool_results"]
	if !present {
		b.fail("tool_results", "Field required")
	} else if l, isList := v.([]any); !isList {
		b.fail("tool_results", "Input should be a valid list")
	} else {
		ov, _ := ordered.Get("tool_results")
		ol, _ := ov.([]any)
		for i, e := range l {
			em, isObj := e.(map[string]any)
			if !isObj {
				*b.errs = append(*b.errs, fmt.Sprintf("body -> tool_results -> %d: Input should be a valid dictionary or object to extract fields from", i))
				continue
			}
			id, isStr := em["tool_call_id"].(string)
			if _, has := em["tool_call_id"]; !has {
				*b.errs = append(*b.errs, fmt.Sprintf("body -> tool_results -> %d -> tool_call_id: Field required", i))
			} else if !isStr {
				*b.errs = append(*b.errs, fmt.Sprintf("body -> tool_results -> %d -> tool_call_id: Input should be a valid string", i))
			}
			if _, has := em["output"]; !has {
				*b.errs = append(*b.errs, fmt.Sprintf("body -> tool_results -> %d -> output: Field required", i))
			}
			var out any
			if i < len(ol) {
				if oo, ok := ol[i].(*pyjson.Object); ok {
					out, _ = oo.Get("output")
				}
			}
			results = append(results, toolResult{ToolCallID: id, Output: out})
		}
	}
	if !b.done(w) {
		return "", nil, false
	}
	return cid, results, true
}

// --- response bodies (VoiceCommandResponse, pydantic field order) ---

func requestInfo(voiceCommand, convID string) *pyjson.Object {
	return servertools.Obj("voice_command", voiceCommand, "conversation_id", convID)
}

func errorCommand(typ, msg string) *pyjson.Object {
	return servertools.Obj("success", false, "command_name", nil, "parameters", nil,
		"errors", servertools.Obj("type", typ, "message", msg, "missing_parameters", nil, "suggestions", nil, "clarification_question", nil))
}

func toolCallsList(calls []parse.ToolCall) []any {
	out := []any{}
	for _, c := range calls {
		out = append(out, toolCallObject(c.ID, c.Function.Name, c.Function.Arguments, c.FailureMessage))
	}
	return out
}

func validationObject(v *validationRequest) any {
	if v == nil {
		return nil
	}
	opts := v.Options
	if opts == nil {
		opts = []any{}
	}
	return servertools.Obj("question", v.Question, "parameter_name", v.ParameterName, "options", opts, "tool_call_id", nil)
}

func nullIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// voiceResponse builds a VoiceCommandResponse.
func voiceResponse(commands []any, info *pyjson.Object, stop any, calls []any, validation any, message, reasoning, endOfExchange any) *pyjson.Object {
	if commands == nil {
		commands = []any{}
	}
	return servertools.Obj("commands", commands, "request_information", info, "stop_reason", stop,
		"tool_calls", calls, "validation_request", validation, "assistant_message", message,
		"reasoning", reasoning, "end_of_exchange", endOfExchange)
}

// wireStop maps an engine stop onto the StopReason enum (unknown → complete).
func wireStop(s string) string {
	switch s {
	case stopComplete, stopToolCalls, stopValidation, stopError, stopNotForMe:
		return s
	}
	return stopComplete
}

// --- /voice/command (blocking) ---

func (m *Module) handleVoiceCommand(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	in, ok := parseTurn(w, r)
	if !ok {
		return
	}
	if !m.voiceReady(w) {
		return
	}
	start := m.now()
	out, err := m.processTurn(r.Context(), n, in)
	info := requestInfo(in.VoiceCommand, in.ConversationID)
	if errors.Is(err, errPrecondition) {
		detail(w, http.StatusUnprocessableEntity, notInitialized)
		return
	}
	if err != nil {
		m.recordVoiceTrace(n, in.ConversationID, "voice_command", in.VoiceCommand, "", start, err)
		httpx.WriteJSON(w, http.StatusOK, voiceResponse([]any{errorCommand("processing_error", "Failed to process command: "+err.Error())},
			info, stopComplete, []any{}, nil, nil, nil, nil))
		return
	}
	res := out.res
	m.recordVoiceTrace(n, in.ConversationID, "voice_command", in.VoiceCommand, res.Message, start, nil)
	if res.Stop == stopError {
		msg := res.Err
		if msg == "" {
			msg = "An internal error occurred"
		}
		httpx.WriteJSON(w, http.StatusOK, voiceResponse([]any{errorCommand("llm_error", msg)}, info, stopError,
			[]any{}, nil, nil, nil, nil))
		return
	}
	msg := res.Message
	if strings.Contains(msg, "[Tool data:") {
		if i := strings.Index(msg, "]\n\n"); i >= 0 {
			msg = parse.PyStrip(msg[i+3:])
		}
	}
	httpx.WriteJSON(w, http.StatusOK, voiceResponse(nil, info, wireStop(res.Stop), toolCallsList(res.ToolCalls),
		validationObject(res.Validation), nullIfBlank(msg), nullIfBlank(res.Reasoning), res.EndOfExchange))
}

// --- /voice/command/stream ---

func (m *Module) handleVoiceStream(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	in, ok := parseTurn(w, r)
	if !ok {
		return
	}
	if !m.voiceReady(w) {
		return
	}
	if m.convs.get(in.ConversationID) == nil {
		detail(w, http.StatusBadRequest, notInitialized)
		return
	}
	start := m.now()
	out, err := m.processTurn(r.Context(), n, in)
	if err != nil {
		m.recordVoiceTrace(n, in.ConversationID, "voice_command_stream", in.VoiceCommand, "", start, err)
		detail(w, http.StatusInternalServerError, "Failed to process command: "+err.Error())
		return
	}
	res := out.res
	if res.Stop == stopComplete && parse.PyStrip(res.Message) != "" {
		m.streamSpeech(r.Context(), w, quotePy(res.Message), func(say func(string) bool) { say(res.Message) })
		m.recordVoiceTrace(n, in.ConversationID, "voice_command_stream", in.VoiceCommand, res.Message, start, nil)
		return
	}
	m.recordVoiceTrace(n, in.ConversationID, "voice_command_stream", in.VoiceCommand, res.Message, start, nil)
	msg := resultMessage(res)
	httpx.WriteJSON(w, http.StatusAccepted, voiceResponse(nil, requestInfo(in.VoiceCommand, in.ConversationID),
		wireStop(res.Stop), toolCallsList(res.ToolCalls), validationObject(res.Validation), msg, nil, res.EndOfExchange))
}

// --- /voice/command/continue ---

func (m *Module) handleContinue(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	cid, results, ok := parseToolResults(w, r)
	if !ok {
		return
	}
	if !m.voiceReady(w) {
		return
	}
	start := m.now()
	info := requestInfo("[continuation with tool results]", cid)
	res, _, err := m.continueBlocking(r.Context(), cid, results)
	if errors.Is(err, errPrecondition) {
		detail(w, http.StatusUnprocessableEntity, "Conversation "+cid+" not found or expired")
		return
	}
	m.pushActionsToInbox(r.Context(), n, results)
	m.recordVoiceTrace(n, cid, "voice_command_continue", "[continuation with tool results]", res.Message, start, err)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, voiceResponse([]any{errorCommand("processing_error", "Failed to continue conversation: "+err.Error())},
			info, nil, nil, nil, nil, nil, nil))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, voiceResponse(nil, info, wireStop(res.Stop), toolCallsList(res.ToolCalls),
		validationObject(res.Validation), resultMessage(res), nil, res.EndOfExchange))
}

// resultMessage is result.get("assistant_message"): the string as is, null where the legacy
// result dict had no such key (validation, error).
func resultMessage(res engineResult) any {
	if res.Stop == stopValidation || res.Stop == stopError {
		return nil
	}
	return res.Message
}

// --- /voice/command/continue/stream ---

func (m *Module) handleContinueStream(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	cid, results, ok := parseToolResults(w, r)
	if !ok {
		return
	}
	if !m.voiceReady(w) {
		return
	}
	start := m.now()
	// Inbox actions are a real background job here, so audio isn't delayed (01 §11).
	go m.pushActionsToInbox(context.WithoutCancel(r.Context()), n, results)
	plan := m.planContinueStream(cid, results)
	if plan == nil {
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"fallback": "use_blocking_continue"})
		return
	}
	ctx := r.Context()
	if plan.spoken != "" {
		m.streamSpeech(ctx, w, "", func(say func(string) bool) {
			for _, s := range splitSentences(plan.spoken) {
				if !say(s) {
					return
				}
			}
		})
		m.commitContinueStream(ctx, plan, plan.spoken)
		m.recordVoiceTrace(n, cid, "voice_command_continue", "[continuation]", plan.spoken, start, nil)
		return
	}
	answer := ""
	m.streamSpeech(ctx, w, "", func(say func(string) bool) {
		answer = m.streamContinueLLM(ctx, plan, say)
	})
	m.commitContinueStream(context.WithoutCancel(ctx), plan, answer)
	m.recordVoiceTrace(n, cid, "voice_command_continue", "[continuation]", answer, start, nil)
}

// streamContinueLLM streams the post-tool-results answer sentence by sentence into say and
// returns what to commit (the cleaned answer, the substituted fallback, or "").
func (m *Module) streamContinueLLM(ctx context.Context, plan *continuePlan, say func(string) bool) string {
	conv := plan.conv
	maxTok := 512
	temp := 0.7
	frames, err := m.LLM.Stream(ctx, llm.ChatRequest{Label: llm.LabelLive, Messages: toLLM(plan.llmMsgs), MaxTokens: &maxTok,
		Temperature: &temp, ReasoningBudget: m.thinkingBudget(ctx, conv.householdID)})
	if err != nil {
		m.deps.Log.Error("cc: continue stream failed", "conversation_id", conv.id, "err", err)
		return ""
	}
	start, end := conv.provider.ThinkDelimiters()
	cs := tf.NewContinueStreamer(start, end)
	alive := true
	for f := range frames {
		if f.Err != "" || f.Cancelled {
			// Legacy returned silently on a stream exception: nothing is committed.
			m.deps.Log.Error("cc: continue stream ended early", "conversation_id", conv.id, "err", f.Err)
			return ""
		}
		if f.Done {
			break
		}
		for _, s := range cs.Push(f.Delta) {
			if alive {
				alive = say(s)
			}
		}
	}
	if !alive {
		return ""
	}
	tail, commit := cs.Finish()
	if tail != "" {
		say(tail)
	}
	if cs.Spoken == 0 {
		fallback := tf.ToolResultFallback(resultOutputs(plan.results))
		for _, s := range splitSentences(fallback) {
			say(s)
		}
		if fallback != "" {
			return fallback
		}
	}
	return commit
}

// --- PCM streaming (01 §7.3, 06 §2.1) ---

// streamSpeech writes 200 audio/raw: the X-Audio-* headers from the engine's real format
// (D40 01.Q8), X-Assistant-Message, then PCM per spoken piece, flushed as it renders. produce
// calls say for each piece of text; say returns false once the client is gone.
func (m *Module) streamSpeech(ctx context.Context, w http.ResponseWriter, assistantHeader string, produce func(say func(string) bool)) {
	f := m.TTS.AudioFormat()
	h := w.Header()
	h.Set("Content-Type", "audio/raw")
	h.Set("X-Audio-Sample-Rate", strconv.Itoa(f.SampleRate))
	h.Set("X-Audio-Channels", strconv.Itoa(f.Channels))
	h.Set("X-Audio-Sample-Width", strconv.Itoa(f.SampleWidth))
	h.Set("X-Assistant-Message", assistantHeader)
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	alive := true
	say := func(text string) bool {
		if !alive || ctx.Err() != nil {
			return false
		}
		st, err := m.TTS.Speak(ctx, text)
		if err != nil {
			m.deps.Log.Warn("cc: TTS failed for a sentence (skipped)", "err", err)
			return true
		}
		defer st.Close()
		for {
			b, err := st.Next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					m.deps.Log.Warn("cc: TTS sentence produced no audio", "err", err)
				}
				return true
			}
			if _, err := w.Write(b); err != nil {
				alive = false
				return false
			}
			_ = rc.Flush()
		}
	}
	produce(say)
}

// quotePy is urllib.parse.quote(text, safe=""): everything but unreserved ASCII is
// percent-encoded as UTF-8 bytes.
func quotePy(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// --- /voice/acknowledge and /wake-response ---

func (m *Module) handleAcknowledge(w http.ResponseWriter, r *http.Request, _ *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	cmd, _ := b.str("voice_command", true)
	if !b.done(w) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"text": acknowledgment(cmd, rand.IntN)})
}

const wakeResponseFallback = "Yes?"

// handleWakeResponse generates the short wake greeting; always 200, "Yes?" on any failure.
func (m *Module) handleWakeResponse(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	reply := func(s string) { httpx.WriteJSON(w, http.StatusOK, map[string]any{"text": s}) }
	if m.LLM == nil {
		reply(wakeResponseFallback)
		return
	}
	ctx := r.Context()
	provider, err := m.promptProvider(ctx)
	user := "Hello Jarvis"
	if err == nil {
		if s := provider.UserMessageSuffix(m.householdBool(ctx, settingIncludeThinking, n.HouseholdID)); s != "" {
			user += "\n" + s
		}
	}
	maxTok := 12
	temp := 1.1
	off := 0
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err2 := m.LLM.Chat(wctx, llm.ChatRequest{Label: llm.LabelLive, MaxTokens: &maxTok, Temperature: &temp, ReasoningBudget: &off,
		Messages: []llm.Message{{Role: "system", Content: llm.TextContent(wakeSystemPrompt)}, {Role: "user", Content: llm.TextContent(user)}}})
	if err2 != nil || resp.Content == "" {
		if err2 != nil {
			m.deps.Log.Warn("cc: wake-response LLM call failed", "err", err2)
		}
		reply(wakeResponseFallback)
		return
	}
	content := resp.Content
	if err == nil {
		content = provider.SanitizeText(content)
	}
	if text := parse.PyStrip(cleanForTTS(content)); text != "" {
		reply(text)
		return
	}
	reply(wakeResponseFallback)
}

const wakeSystemPrompt = "You are Jarvis, a voice-assistant butler. The user has just called your " +
	"name. Respond with ONE short, charming, varied greeting — STRICTLY 1 to " +
	"3 words ONLY (HARD CAP at 3 words), gender neutral. " +
	"CRITICAL: speed matters more than wit — every extra word delays the " +
	"user. You are generating many of these throughout the day; pick " +
	"something DIFFERENT every time, but never go over 3 words. " +
	"Reply with JUST the greeting, no labels, no quotes, no XML, no " +
	"trailing explanation.\n\n" +
	"Style examples (riff, don't copy verbatim):\n" +
	"- Yes?\n" +
	"- Mhm?\n" +
	"- Listening.\n" +
	"- Go ahead.\n" +
	"- What's up?\n" +
	"- Hit me.\n" +
	"- Shoot.\n" +
	"- Whatcha got?\n" +
	"- Right here.\n" +
	"- You rang?\n" +
	"- Speak.\n" +
	"- Tell me.\n" +
	"- Sure thing?\n" +
	"- I'm here.\n" +
	"- Sup?\n" +
	"- Ready.\n"

// voiceReady answers 503 when the voice pipeline's engines aren't wired.
func (m *Module) voiceReady(w http.ResponseWriter) bool {
	if m.LLM == nil || m.TTS == nil {
		detail(w, http.StatusServiceUnavailable, "Voice pipeline unavailable")
		return false
	}
	return true
}

// --- inbox actions on tool results (_maybe_push_actions_to_inbox) ---

func (m *Module) pushActionsToInbox(ctx context.Context, n *nodeCtx, results []toolResult) {
	for _, tr := range results {
		o, ok := tr.Output.(*pyjson.Object)
		if !ok {
			continue
		}
		cv, _ := o.Get("context")
		c, ok := cv.(*pyjson.Object)
		if !ok {
			continue
		}
		av, _ := c.Get("actions")
		actions, ok := av.([]any)
		if !ok || len(actions) == 0 {
			continue
		}
		draft, ok := c.Get("draft")
		if !ok {
			draft = pyjson.NewObject()
		}
		preview := pyStrOr(c, "preview", "")
		message := pyStrOr(c, "message", "")
		command := pyStrOr(c, "command_name", "unknown")
		title := pyStrOr(c, "inbox_title", "")
		if title == "" {
			title = "Confirm: " + command
		}
		summary := pyStrOr(c, "inbox_summary", "")
		if summary == "" {
			summary = message
		}
		if summary == "" {
			summary = truncRunes(preview, 100)
		}
		if n.HouseholdID == "" {
			m.deps.Log.Warn("cc: cannot push confirmation: no household on node")
			return
		}
		m.pushConfirmation(ctx, n.HouseholdID, nil, n.ID, title, summary, preview, command, actions, draft, "household")
	}
}

func pyStrOr(o *pyjson.Object, k, def string) string {
	v, ok := o.Get(k)
	if !ok || v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return pyjson.Str(v)
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// jsonValue converts an ordered value to plain JSON-compatible Go data for the notifications
// module (which stores metadata as a map).
func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
