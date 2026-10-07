package cc

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The tool loop (docs/cc/02 §3.2), ToolExecutionEngine.execute. Changes from legacy:
//   - no router decision, router hint or must-call guard (D9);
//   - date keys come from the raw transcript once per turn (D40 02.Q10), no LLM fallback;
//   - a call reaches a server tool only when that tool was offered this conversation
//     (explicit plane routing, 02 §11); everything else is a client tool;
//   - the native transcript stays valid: an assistant tool_calls message never stays in
//     history without tool replies (D8 02.Q6);
//   - the iteration limit speaks a natural fallback (D40 02.Q9);
//   - the history is a working copy the caller commits on success.

// Stop reasons.
const (
	stopComplete           = "complete"
	stopToolCalls          = "tool_calls"
	stopValidation         = "validation_required"
	stopServerToolComplete = "server_tool_complete"
	stopNotForMe           = "not_for_me"
	stopError              = "error"
)

const (
	iterMaxTokens        = 256
	doubleCheckMaxTokens = 1536
	loopTemperature      = 0.4
)

// validationRequest is request_validation's payload.
type validationRequest struct {
	Question      string
	ParameterName string
	Options       []any
}

// engineResult is execute()'s result dict.
type engineResult struct {
	Stop          string
	Message       string
	ToolCalls     []parse.ToolCall
	Validation    *validationRequest
	ServerResults []chatMsg
	EndOfExchange bool
	Reasoning     string
	Err           string
}

// engineInput is one loop run.
type engineInput struct {
	conv        *conversation
	msgs        []chatMsg // working copy; the caller commits engineRun.msgs on success
	maxIter     int
	utterance   string   // user_utterance (guards, refinement)
	dateKeys    []string // the turn's extracted date keys
	doubleCheck bool     // sentinel_double_check
	turn        servertools.Turn
}

var thinkCaptureRE = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

const (
	nagMustCall    = "[MUST_CALL_RETRY]"
	nagDedupe      = "[TOOL_DEDUPE]"
	nagInvalid     = "[INVALID_PARAM_RETRY]"
	nagISO         = "[ISO_DATE_RETRY]"
	nagDoubleCheck = "[NOT_FOR_ME_DOUBLE_CHECK]"
)

// isNag is _is_retry_nag: a standalone system message whose content starts with tag
// (messages[0] merely mentions the tags).
func isNag(m chatMsg, tag string) bool {
	return m.Role == "system" && strings.HasPrefix(strings.TrimLeft(m.Content, " \t\n\r\v\f"), tag)
}

func countNags(msgs []chatMsg, tag string) int {
	n := 0
	for _, m := range msgs {
		if isNag(m, tag) {
			n++
		}
	}
	return n
}

func filterMsgs(msgs []chatMsg, drop func(chatMsg) bool) []chatMsg {
	out := msgs[:0:0]
	for _, m := range msgs {
		if !drop(m) {
			out = append(out, m)
		}
	}
	return out
}

// runEngine executes the loop. It returns the result and the working history.
func (m *Module) runEngine(ctx context.Context, in engineInput) (engineResult, []chatMsg) {
	conv := in.conv
	p := conv.provider
	msgs := in.msgs
	hh := conv.householdID

	// 0. Scrub stale steering nags from earlier turns (never messages[0]).
	if len(msgs) > 1 {
		head, rest := msgs[:1], msgs[1:]
		rest = filterMsgs(rest, func(x chatMsg) bool { return isNag(x, nagMustCall) || isNag(x, nagDedupe) })
		msgs = append(append([]chatMsg(nil), head...), rest...)
	}

	native := p.SupportsNativeTools()
	var nativeTools []llm.Tool
	if native {
		nativeTools = llmTools(prompts.NativeTools(p, conv.tools))
	}
	smallMode := m.settings.Bool(ctx, settingSmallModelMode, settings.Scope{})
	budget := m.thinkingBudget(ctx, hh)
	dctx := dates.New(m.now(), conv.timezone)

	var reasoning []string
	withReasoning := func(r engineResult) engineResult {
		if len(reasoning) > 0 {
			r.Reasoning = strings.Join(reasoning, "\n\n")
		}
		return r
	}
	poppedProse := ""
	havePopped := false
	doubleChecked := false
	nudged := map[[2]string]bool{}
	dedupePending := false
	nextMax := iterMaxTokens
	tr := traceFrom(ctx)

	for iter := 0; iter < in.maxIter; iter++ {
		maxTok := nextMax
		nextMax = iterMaxTokens
		temp := loopTemperature
		req := llm.ChatRequest{Label: llm.LabelLive, Messages: toLLM(msgs), MaxTokens: &maxTok, Temperature: &temp,
			ReasoningBudget: budget}
		if doubleChecked && maxTok == doubleCheckMaxTokens {
			b := -1 // the double-check pass ends in /think: let it reason
			req.ReasoningBudget = &b
		}
		if native {
			req.Tools = nativeTools
			req.ToolChoice = json.RawMessage(`"auto"`)
		} else if rf := p.ResponseFormat(); rf != nil {
			t, _ := rf.Get("type")
			ts, _ := t.(string)
			req.ResponseFormat = &llm.ResponseFormat{Type: ts}
		}
		llmName := "llm_call_iter_" + strconv.Itoa(iter+1)
		llmStart := tr.since()
		resp, err := m.LLM.Chat(ctx, req)
		llmEnd := tr.since()
		if err != nil {
			tr.span(llmName, "llm_proxy", llmStart, llmEnd, err, nil)
			m.deps.Log.Error("cc: tool loop LLM call failed", "conversation_id", conv.id, "err", err)
			return engineResult{Stop: stopError, Err: err.Error()}, msgs
		}
		raw := resp.Content
		for _, mt := range thinkCaptureRE.FindAllStringSubmatch(raw, -1) {
			if s := parse.PyStrip(mt[1]); s != "" {
				reasoning = append(reasoning, s)
			}
		}

		var calls []parse.ToolCall
		finish := parse.FinishStop
		message := raw
		if native {
			if resp.FinishReason == "tool_calls" && len(resp.ToolCalls) > 0 {
				calls = normalizeNativeCalls(resp.ToolCalls)
				if len(calls) > 0 {
					finish = parse.FinishToolCalls
				}
			}
		} else {
			content := raw
			if t, ok := p.ParseResponse(raw); ok {
				content = t
			}
			pr := parse.ParseToolCalls(content)
			finish, calls, message = pr.FinishReason, pr.ToolCalls, pr.Message
		}
		tr.span(llmName, "llm_proxy", llmStart, llmEnd, nil, map[string]any{"prompt_tokens": resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens, "finish_reason": finish})

		msgs = append(msgs, chatMsg{Role: "assistant", Content: raw, ToolCalls: calls})
		if doubleChecked {
			msgs = filterMsgs(msgs, func(x chatMsg) bool { return strings.Contains(x.Content, nagDoubleCheck) })
		}
		if dedupePending {
			msgs = filterMsgs(msgs, func(x chatMsg) bool { return isNag(x, nagDedupe) })
			dedupePending = false
		}

		// h. The sentinel outranks every guard.
		if parse.SentinelNotForMe(raw, message) {
			if havePopped {
				msgs = msgs[:len(msgs)-1]
				for len(msgs) > 0 && isNag(msgs[len(msgs)-1], nagMustCall) {
					msgs = msgs[:len(msgs)-1]
				}
				restored := cleanForTTS(p.SanitizeText(poppedProse))
				msgs = append(msgs, chatMsg{Role: "assistant", Content: restored})
				return withReasoning(engineResult{Stop: stopComplete, Message: restored}), msgs
			}
			if in.doubleCheck && !doubleChecked && iter+1 < in.maxIter {
				doubleChecked = true
				msgs = msgs[:len(msgs)-1]
				msgs = append(msgs, chatMsg{Role: "user", Content: doubleCheckMessage})
				nextMax = doubleCheckMaxTokens
				continue
			}
			return withReasoning(engineResult{Stop: stopNotForMe, Message: raw}), msgs
		}

		switch finish {
		case parse.FinishStop:
			exchange := parse.SentinelExchangeComplete(raw, message)
			terminal := exchange || parse.SentinelNotForMe(raw, message)
			force := conv.forceTools
			retries := countNags(msgs, nagMustCall)
			if force && retries < 2 && !doubleChecked && !terminal && in.utterance != "" {
				force = m.forceGuardArmed(conv, in.utterance, raw, message)
			}
			if force && retries < 2 && !doubleChecked && !terminal {
				if parse.PyStrip(message) != "" {
					poppedProse, havePopped = message, true
				}
				msgs = msgs[:len(msgs)-1]
				msgs = append(msgs, sysMsg(mustCallRetry(retries+1)))
				continue
			}
			cleaned := message
			if message != "" {
				cleaned = cleanForTTS(p.SanitizeText(message))
			}
			return withReasoning(engineResult{Stop: stopComplete, Message: cleaned, EndOfExchange: exchange}), msgs

		case parse.FinishToolCalls:
			// 1. ISO date guard on resolved_datetimes.
			args := make([]string, len(calls))
			for i, c := range calls {
				args[i] = c.Function.Arguments
			}
			status, fixed := dctx.FixISODates(args)
			if status == "bad" && countContains(msgs, nagISO) < 1 {
				msgs = msgs[:len(msgs)-1]
				msgs = append(msgs, sysMsg(isoDateRetry))
				continue
			}
			for i := range calls {
				calls[i].Function.Arguments = fixed[i]
			}
			// 2. Date injection against the full cached schemas.
			for i := range calls {
				props := dates.SchemaProperties(conv.tools, calls[i].Function.Name)
				if out, changed := dctx.InjectDates(calls[i].Function.Arguments, props, in.dateKeys); changed {
					calls[i].Function.Arguments = out
				}
			}
			// The history keeps the calls as executed.
			msgs[len(msgs)-1].ToolCalls = append([]parse.ToolCall(nil), calls...)

			// 3. Split and run the server tools.
			var serverResults []chatMsg
			var clientCalls []parse.ToolCall
			validation := false
			var validationRes *pyjson.Object
			names := make([]any, len(calls))
			for i, c := range calls {
				names[i] = c.Function.Name
			}
			endExec := tr.measure("tool_exec_iter_"+strconv.Itoa(iter+1), "cc", map[string]any{"tools": names})
			for _, c := range calls {
				if c.Function.Name == "request_validation" {
					validation = true
				}
				if !conv.serverNames[c.Function.Name] {
					clientCalls = append(clientCalls, c)
					continue
				}
				endTool := tr.measure("server_tool_"+c.Function.Name, "cc", nil)
				res := m.tools.Execute(ctx, servertools.Call{ID: c.ID, Name: c.Function.Name, Args: argsObject(c.Function.Arguments)}, in.turn)
				endTool(nil)
				if o, ok := res.(*pyjson.Object); ok && c.Function.Name == "request_validation" {
					if v, _ := o.Get("_validation_request"); v == true {
						validationRes = o
					}
				}
				serverResults = append(serverResults, chatMsg{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name,
					Content: pyjson.Dumps(res, true)})
			}
			endExec(nil)
			msgs = append(msgs, serverResults...)
			others := len(calls) > 1 || len(clientCalls) > 0
			if validation && others {
				msgs = m.closeDanglingCalls(msgs, clientCalls)
				continue
			}
			if validation && validationRes != nil {
				q, _ := validationRes.Get("question")
				pn, _ := validationRes.Get("parameter_name")
				opts, _ := validationRes.Get("options")
				ol, _ := opts.([]any)
				if ol == nil {
					ol = []any{}
				}
				qs, _ := q.(string)
				ps, _ := pn.(string)
				return withReasoning(engineResult{Stop: stopValidation,
					Validation: &validationRequest{Question: qs, ParameterName: ps, Options: ol}}), msgs
			}
			if len(serverResults) > 0 && len(clientCalls) > 0 {
				// Client calls are dropped until re-issued; give them replies so the
				// transcript stays valid (D8 02.Q6).
				msgs = m.closeDanglingCalls(msgs, clientCalls)
				continue
			}
			if len(clientCalls) > 0 {
				if conv.forceTools && in.utterance != "" && !native {
					clientCalls = m.refineParams(ctx, conv, clientCalls, in.utterance)
				}
				if name, key, since, dup := m.findDuplicate(ctx, conv, clientCalls, nudged); dup {
					nudged[[2]string{name, key}] = true
					dedupePending = true
					m.deps.Log.Info("cc: tool_dedupe_nudge", "tool", name, "seconds_since", since.Seconds(), "outcome", "nudge_issued")
					if len(msgs) > 0 && msgs[len(msgs)-1].Role == "assistant" {
						msgs = msgs[:len(msgs)-1]
					}
					msgs = append(msgs, sysMsg(toolDedupeNudge(name)))
					continue
				}
				if invalid := findInvalidParams(clientCalls, conv.commands); len(invalid) > 0 {
					max := 2
					if smallMode {
						max = 1
					}
					if r := countNags(msgs, nagInvalid); r < max {
						// D8 02.Q6: drop the rejected tool_calls message so no call is left without
						// a reply; the nag carries what was wrong.
						if native {
							msgs = msgs[:len(msgs)-1]
						}
						msgs = append(msgs, sysMsg(invalidParamRetry(r+1, max, invalid)))
						continue
					}
				}
				for _, c := range clientCalls {
					if key, ok := canonicalArgsKey(c.Function.Arguments); ok {
						conv.issued = append(conv.issued, issuedCall{name: c.Function.Name, argsHash: key, at: m.now()})
					}
				}
				msgs[len(msgs)-1].ToolCalls = clientCalls
				return withReasoning(engineResult{Stop: stopToolCalls, ToolCalls: clientCalls, Message: message}), msgs
			}
			if len(serverResults) > 0 && !native {
				return withReasoning(engineResult{Stop: stopServerToolComplete, ServerResults: serverResults, Message: message}), msgs
			}
			// Native server-only results (or nothing callable): loop again.
		default:
			return withReasoning(engineResult{Stop: stopComplete, Message: message}), msgs
		}
	}
	m.deps.Log.Warn("cc: tool loop iteration limit reached", "conversation_id", conv.id, "max", in.maxIter)
	return withReasoning(engineResult{Stop: stopComplete, Message: maxIterationsFallback, Err: "max_iterations_exceeded"}), msgs
}

// closeDanglingCalls answers client calls the loop dropped with a not-executed tool reply, so
// an assistant tool_calls message never stands alone (D8 02.Q6).
func (m *Module) closeDanglingCalls(msgs []chatMsg, dropped []parse.ToolCall) []chatMsg {
	for _, c := range dropped {
		msgs = append(msgs, chatMsg{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name,
			Content: `{"error": "not_executed", "message": "Not executed; call it again if it is still needed."}`})
	}
	return msgs
}

func countContains(msgs []chatMsg, tag string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, tag) {
			n++
		}
	}
	return n
}

// argsObject parses JSON arguments; anything but an object is {} (legacy json.loads → {}).
func argsObject(args string) *pyjson.Object {
	v, err := pyjson.Loads(args)
	if err != nil {
		return pyjson.NewObject()
	}
	if o, ok := v.(*pyjson.Object); ok {
		return o
	}
	return pyjson.NewObject()
}

// normalizeNativeCalls is _normalize_native_tool_calls: name required, arguments a JSON
// string, ids preserved or minted.
func normalizeNativeCalls(raw []llm.ToolCall) []parse.ToolCall {
	var out []parse.ToolCall
	for _, c := range raw {
		if c.Function.Name == "" {
			continue
		}
		args := c.Function.Arguments
		if args == "" {
			args = "{}"
		}
		id := c.ID
		if id == "" {
			id = parse.NewCallID()
		}
		out = append(out, parse.ToolCall{ID: id, Type: "function", Function: parse.FunctionCall{Name: c.Function.Name, Arguments: args}})
	}
	return out
}

// forceGuardArmed decides whether the force-tools guard stays armed for a prose reply
// (02 §3.2 i): question-shaped utterances, non-action utterances whose reply claims no
// action, and utterances matching no command keyword all disarm it.
func (m *Module) forceGuardArmed(conv *conversation, utterance, raw, message string) bool {
	return forceGate(conv, utterance, []string{parse.OutsideThink(raw), parse.OutsideThink(message)})
}

// findDuplicate is _find_duplicate_issued_call: the first client call identical (name +
// canonical args) to one issued in this conversation within voice.tool_dedupe_window_seconds
// and not already nudged this turn. Every failure passes the call through.
func (m *Module) findDuplicate(ctx context.Context, conv *conversation, calls []parse.ToolCall, nudged map[[2]string]bool) (string, string, time.Duration, bool) {
	window := m.settings.Float(ctx, settingDedupeWindow, settings.Scope{})
	if window <= 0 || len(conv.issued) == 0 {
		return "", "", 0, false
	}
	now := m.now()
	for _, c := range calls {
		key, ok := canonicalArgsKey(c.Function.Arguments)
		if !ok || nudged[[2]string{c.Function.Name, key}] {
			continue
		}
		for _, rec := range conv.issued {
			if rec.name == c.Function.Name && rec.argsHash == key && now.Sub(rec.at).Seconds() <= window {
				return c.Function.Name, key, now.Sub(rec.at), true
			}
		}
	}
	return "", "", 0, false
}
