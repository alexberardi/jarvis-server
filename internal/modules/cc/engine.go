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
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
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
	// KeepListening: a follow-up the gate rejected; the node keeps its window open
	// (followup_gate.go). Sent as keep_listening only when true.
	KeepListening bool
	// StoodDown: another node of the household answers this wake (arbitration.go); the stop
	// is not_for_me, sent with stood_down: true.
	StoodDown bool
	Reasoning string
	Err       string
	// ImageArgs are the photos a client call's image parameters resolved to, by call id
	// (chat_image_actions.go, §9). ToolCalls keep the model's photo numbers, so history,
	// traces and transcripts never hold bytes; only the node's tool_call gets them
	// (wireArguments).
	ImageArgs map[string]imageArgs
}

// engineInput is one loop run.
type engineInput struct {
	conv        *conversation
	msgs        []chatMsg // working copy; the caller commits engineRun.msgs on success
	maxIter     int
	utterance   string   // user_utterance (guards, refinement)
	dateKeys    []string // the turn's extracted date keys
	doubleCheck bool     // sentinel_double_check
	// doubleCheckMsg is the double-check pass's message ("": doubleCheckMessage, the wake's).
	doubleCheckMsg string
	turn           servertools.Turn
	// noForce turns off the must-call retry for this turn (a photo-only chat message: with no
	// words the force guard can't run, and there is no request to force a tool for, CI8).
	noForce bool
	// voiceCommand is the turn's voice command as heard, without hints or suffix (the typed-app
	// fill: utterance is the composed user message on the continue path).
	voiceCommand string
	// mustCall is a tool this turn has to call (stop_recording on a stop phrase while the
	// computer records, computer_activity.go): a reply without it, not-for-me included, gets one
	// nudge.
	mustCall string
}

var thinkCaptureRE = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

const (
	nagMustCall    = "[MUST_CALL_RETRY]"
	nagDedupe      = "[TOOL_DEDUPE]"
	nagInvalid     = "[INVALID_PARAM_RETRY]"
	nagISO         = "[ISO_DATE_RETRY]"
	nagDoubleCheck = "[NOT_FOR_ME_DOUBLE_CHECK]"
	nagFailedClaim = "[TOOL_FAILED_CLAIM]"
)

// failedClaimRetry is the one nudge for a reply that claims an action a tool just failed to do.
const failedClaimRetry = nagFailedClaim + " A tool call above did not succeed (see its error). Your reply " +
	"said it was done. Answer again: tell the user plainly what did not happen and why, in one or two " +
	"short sentences, and what they can do about it if anything. Do not say it was done."

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
	msgs := m.visionGuard(ctx, in.msgs)
	hh := conv.householdID

	// 0. Scrub stale steering nags from earlier turns (never messages[0]).
	if len(msgs) > 1 {
		head, rest := msgs[:1], msgs[1:]
		rest = filterMsgs(rest, func(x chatMsg) bool {
			return isNag(x, nagMustCall) || isNag(x, nagDedupe) || isNag(x, nagFailedClaim) || isFollowThroughNag(x)
		})
		msgs = append(append([]chatMsg(nil), head...), rest...)
	}

	native := conv.mode.Native()
	var nativeTools []llm.Tool
	if native {
		nativeTools = nativePayload(conv.tools, in.utterance)
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
	deduped := false // a [TOOL_DEDUPE] nudge this run: answering from earlier results is the point
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
			if len(msgs) > 0 && callNowNag(msgs[len(msgs)-1]) {
				req.ToolChoice = json.RawMessage(`"required"`)
			}
		} else if rf := conv.mode.ResponseFormat(); rf != nil {
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
			return engineResult{Stop: stopError, Err: llmErrText(err)}, msgs
		}
		conv.noteUsage(resp.Usage)
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
			// A call llama-server couldn't parse (no tags, or tags in the content) is
			// recovered as on the text path, but only when every call names an offered tool.
			if finish == parse.FinishStop {
				if f, c, msg := recoverTextCalls(raw, conv.hasTool); f == parse.FinishToolCalls && allOffered(c, conv.hasTool) {
					finish, calls, message = f, c, msg
					for _, tc := range c {
						m.deps.Log.Info("cc: native_call_rescued", "conversation_id", conv.id, "tool", tc.Function.Name)
					}
				}
			}
		} else {
			finish, calls, message = recoverTextCalls(raw, conv.hasTool)
		}
		tr.span(llmName, "llm_proxy", llmStart, llmEnd, nil, map[string]any{"prompt_tokens": resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens, "finish_reason": finish, "output_preview": outputPreview(raw)})

		msgs = append(msgs, chatMsg{Role: "assistant", Content: raw, ToolCalls: calls})
		if doubleChecked {
			msgs = filterMsgs(msgs, func(x chatMsg) bool { return strings.Contains(x.Content, nagDoubleCheck) })
		}
		if dedupePending {
			msgs = filterMsgs(msgs, func(x chatMsg) bool { return isNag(x, nagDedupe) })
			dedupePending = false
		}

		// A stop phrase while recording outranks the sentinel (#288).
		if in.mustCall != "" && len(calls) == 0 && !calledThisTurn(msgs, in.mustCall) && countNags(msgs, nagStopRecording) < 1 &&
			iter+1 < in.maxIter {
			msgs = msgs[:len(msgs)-1]
			msgs = append(msgs, sysMsg(stopRecordingRetry))
			continue
		}
		// A native not_for_me call is the sentinel (decision 2026-10-20-prompt-diet): alone it ends
		// the turn silently, with the plain sentinel kept in the history (no call without a
		// result); next to real calls it is dropped and they run.
		if native {
			rest, only := splitNotForMe(calls)
			if only && toolRanThisTurn(msgs[:len(msgs)-1]) && countNags(msgs, nagAnswerAfterTool) < 1 && iter+1 < in.maxIter {
				msgs = msgs[:len(msgs)-1]
				msgs = append(msgs, sysMsg(answerAfterToolRetry))
				continue
			}
			if only {
				raw, message, calls, finish = parse.NotForMeLiteral, parse.NotForMeLiteral, nil, parse.FinishStop
				msgs[len(msgs)-1] = chatMsg{Role: "assistant", Content: raw}
			} else if len(rest) != len(calls) {
				calls = rest
				msgs[len(msgs)-1].ToolCalls = rest
			}
		}
		// h. The sentinel outranks every guard.
		if parse.SentinelNotForMe(raw, message) {
			if havePopped {
				msgs = msgs[:len(msgs)-1]
				for len(msgs) > 0 && isNag(msgs[len(msgs)-1], nagMustCall) {
					msgs = msgs[:len(msgs)-1]
				}
				restored := cleanForTTS(prompts.SanitizeText(poppedProse))
				msgs = append(msgs, chatMsg{Role: "assistant", Content: restored})
				return withReasoning(engineResult{Stop: stopComplete, Message: restored}), msgs
			}
			if in.doubleCheck && !doubleChecked && iter+1 < in.maxIter {
				doubleChecked = true
				msgs = msgs[:len(msgs)-1]
				dc := in.doubleCheckMsg
				if dc == "" {
					dc = doubleCheckMessage
				}
				msgs = append(msgs, chatMsg{Role: "user", Content: dc})
				nextMax = doubleCheckMaxTokens
				continue
			}
			return withReasoning(engineResult{Stop: stopNotForMe, Message: raw}), msgs
		}

		switch finish {
		case parse.FinishStop:
			exchange := parse.SentinelExchangeComplete(raw, message)
			terminal := exchange || parse.SentinelNotForMe(raw, message)
			// The must-call retry nudges a model that answered an action request in prose. It is
			// spent once a tool ran this turn (any result, success or failure: an offline node's
			// error is the answer, and re-arming on every continue looped the node's tool), and
			// it fires at most once a turn.
			// A dedupe nudge counts as using the tool: it asked for an answer from the results above.
			force := conv.mode.ForceTools && !in.noForce && !deduped && !toolRanThisTurn(msgs)
			retries := countNags(msgs, nagMustCall)
			if force && retries < maxMustCallRetries && !doubleChecked && !terminal && in.utterance != "" {
				force = m.forceGuardArmed(conv, in.utterance, raw, message)
			}
			if force && retries < maxMustCallRetries && !doubleChecked && !terminal {
				if parse.PyStrip(message) != "" {
					poppedProse, havePopped = message, true
				}
				msgs = msgs[:len(msgs)-1]
				msgs = append(msgs, sysMsg(mustCallRetry(retries+1)))
				continue
			}
			// A tool failed this turn but the reply claims it worked (beyond legacy; the pipeline
			// scenarios caught "Got it, I'll remember that" after remember refused): one nudge.
			if !terminal && !doubleChecked && countNags(msgs, nagFailedClaim) < 1 &&
				tf.ResponseClaimsAction(parse.OutsideThink(message)) && toolFailedThisTurn(msgs) {
				msgs = msgs[:len(msgs)-1]
				msgs = append(msgs, sysMsg(failedClaimRetry))
				continue
			}
			// The request was left half done: a memory request answered "noted" with no tool to
			// store it, "not in my memory" without a search, "open X and type Y" without the
			// typing (followthrough.go).
			// Each follow-through nudge needs its request and its claim, so <exchange_complete/>
			// doesn't skip them (#435: "Got it, your locker code is 4471. <exchange_complete/>").
			if !parse.SentinelNotForMe(raw, message) && !doubleChecked {
				if nag := followThroughNag(conv, in.utterance, parse.OutsideThink(message), msgs); nag != "" {
					msgs = msgs[:len(msgs)-1]
					msgs = append(msgs, sysMsg(nag))
					continue
				}
			}
			cleaned := message
			if message != "" {
				cleaned = cleanForTTS(prompts.SanitizeText(message))
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
			// 2b. Typing keeps the app the user named (desktop_hint.go).
			for i := range calls {
				if out, changed := fillTypedApp(in.voiceCommand, calls[i]); changed {
					calls[i].Function.Arguments = out
					m.deps.Log.Info("cc: typed_app_filled", "conversation_id", conv.id)
				}
			}
			// The history keeps the calls as executed.
			msgs[len(msgs)-1].ToolCalls = append([]parse.ToolCall(nil), calls...)

			// 3. Split and run the server tools.
			var serverResults []chatMsg
			var clientCalls []parse.ToolCall
			var callImages map[string]imageArgs
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
					if params := conv.imageParams[c.Function.Name]; len(params) > 0 {
						// A photo tool on the node: swap the model's numbers for the photos, or
						// refuse here like a server tool would, without calling the node (§9).
						imgs, refusal := resolveImageArgs(c.Function.Arguments, params, conv.turnImages(msgs))
						if refusal != nil {
							serverResults = append(serverResults, chatMsg{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name,
								Content: pyjson.Dumps(refusal, true)})
							continue
						}
						if len(imgs) > 0 {
							if callImages == nil {
								callImages = map[string]imageArgs{}
							}
							callImages[c.ID] = imgs
						}
					}
					clientCalls = append(clientCalls, c)
					continue
				}
				endTool := tr.measure("server_tool_"+c.Function.Name, "cc", nil)
				turn := in.turn
				turn.Images = conv.turnImages(msgs)
				res := m.tools.Execute(ctx, servertools.Call{ID: c.ID, Name: c.Function.Name, Args: argsObject(c.Function.Arguments)}, turn)
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
				if conv.mode.ForceTools && in.utterance != "" && !native {
					clientCalls = m.refineParams(ctx, conv, clientCalls, in.utterance)
				}
				if name, key, since, dup := m.findDuplicate(ctx, conv, clientCalls, nudged); dup {
					nudged[[2]string{name, key}] = true
					dedupePending = true
					deduped = true
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
				return withReasoning(engineResult{Stop: stopToolCalls, ToolCalls: clientCalls, Message: message,
					ImageArgs: callImages}), msgs
			}
			if len(serverResults) > 0 && !native {
				return withReasoning(engineResult{Stop: stopServerToolComplete, ServerResults: serverResults, Message: message}), msgs
			}
			if ack, ok := acceptedAck(serverResults); ok {
				// Work that runs after the reply (an errand plan, a call plan, research): its
				// ack is the only true thing to say yet. A composed reply turned "I'm planning
				// that errand" into "I've got the timer set" while the plan later failed, and it
				// cost a second live call (the demo dry run). Say the ack as is.
				ack = cleanForTTS(prompts.SanitizeText(ack))
				msgs = append(msgs, chatMsg{Role: "assistant", Content: ack})
				return withReasoning(engineResult{Stop: stopComplete, Message: ack}), msgs
			}
			if refusal, ok := speakerRefusal(serverResults); ok && speakerRefusedThisTurn(msgs[:len(msgs)-len(serverResults)]) {
				// Refused an unidentified voice again: no retry changes that, and the 9B called
				// remember over and over until the loop gave up (#435). The first refusal is the
				// model's to word; a repeat is said as is.
				msgs = append(msgs, chatMsg{Role: "assistant", Content: refusal})
				return withReasoning(engineResult{Stop: stopComplete, Message: refusal}), msgs
			}
			// Native server-only results (or nothing callable): loop again.
		default:
			return withReasoning(engineResult{Stop: stopComplete, Message: message}), msgs
		}
	}
	m.deps.Log.Warn("cc: tool loop iteration limit reached", "conversation_id", conv.id, "max", in.maxIter)
	return withReasoning(engineResult{Stop: stopComplete, Message: maxIterationsFallback, Err: "max_iterations_exceeded"}), msgs
}

// acceptedAck reports whether every server result is an accepted-for-later ack
// ({"status": "accepted", "message": …}: run_errand, schedule_errand, make_phone_call,
// deep_research) and returns their messages joined, the reply the text path's fast path
// already speaks.
func acceptedAck(results []chatMsg) (string, bool) {
	if len(results) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(results))
	for _, r := range results {
		v, err := pyjson.Loads(r.Content)
		if err != nil {
			return "", false
		}
		o, _ := v.(*pyjson.Object)
		if o == nil {
			return "", false
		}
		st, _ := o.Get("status")
		msg, _ := o.Get("message")
		ms, _ := msg.(string)
		if st != "accepted" || parse.PyStrip(ms) == "" {
			return "", false
		}
		parts = append(parts, parse.PyStrip(ms))
	}
	return strings.Join(parts, " "), true
}

// callNowNag reports a nudge that names the one call the request still needs (remember, recall,
// the typing, cancel_watch, stop_recording while recording, a computer control switch tool): the
// retry after it requires a tool call. The 9B answered "[NOT_SAVED] … Call remember now" with
// "Got it, your parking spot is B12." again (#435). The must-call retry and the other nudges
// allow an answer, so stay auto.
func callNowNag(m chatMsg) bool {
	return isNag(m, nagUnsaved) || isNag(m, nagRecallFirst) || isNag(m, nagUnfinished) || isNag(m, nagNotCancelled) ||
		isNag(m, nagStopRecording) || isNag(m, nagRecordingNotRun) || isNag(m, nagSwitchNotRun)
}

// nagAnswerAfterTool asks for the answer when the model called not_for_me after a tool ran in
// the turn: the turn was for Jarvis (#435: stop_recording ran, then not_for_me, and the user
// heard nothing).
const (
	nagAnswerAfterTool   = "[ANSWER]"
	answerAfterToolRetry = nagAnswerAfterTool + " You already acted on this request, so it was meant for you. Tell the " +
		"user the result in one short sentence."
)

// speakerRefusedThisTurn reports whether a tool result since the turn's user message is the
// no_speaker refusal.
func speakerRefusedThisTurn(msgs []chatMsg) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		switch {
		case msgs[i].Role == "tool":
			if _, ok := speakerRefusal(msgs[i : i+1]); ok {
				return true
			}
		case msgs[i].Role == "user" && !strings.HasPrefix(msgs[i].Content, nagDoubleCheck):
			return false
		}
	}
	return false
}

// speakerRefusal reports whether every server result is the no_speaker refusal (the memory
// tools' toolSpeakerError) and returns its message.
func speakerRefusal(results []chatMsg) (string, bool) {
	msg := ""
	for _, r := range results {
		v, err := pyjson.Loads(r.Content)
		if err != nil {
			return "", false
		}
		o, _ := v.(*pyjson.Object)
		if o == nil {
			return "", false
		}
		e, _ := o.Get("error")
		m, _ := o.Get("message")
		ms, _ := m.(string)
		if e != "no_speaker" || parse.PyStrip(ms) == "" {
			return "", false
		}
		msg = parse.PyStrip(ms)
	}
	return msg, msg != ""
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
		args, failure := takeFailureMessage(args)
		out = append(out, parse.ToolCall{ID: id, Type: "function", Function: parse.FunctionCall{Name: c.Function.Name, Arguments: args},
			FailureMessage: failure})
	}
	return out
}

// recoverTextCalls is the text path's reading of a reply: tagged calls (ParseToolCalls), else
// one bare call at the end of the prose (trailingBareCall, #56).
func recoverTextCalls(raw string, offered func(string) bool) (string, []parse.ToolCall, string) {
	content := raw
	if t, ok := prompts.ParseResponse(raw); ok {
		content = t
	}
	pr := parse.ParseToolCalls(content)
	finish, calls, message := pr.FinishReason, pr.ToolCalls, pr.Message
	if finish == parse.FinishStop {
		if c, prose, ok := trailingBareCall(message, offered); ok {
			finish, calls, message = parse.FinishToolCalls, []parse.ToolCall{c}, prose
		}
	}
	return finish, calls, message
}

func allOffered(calls []parse.ToolCall, offered func(string) bool) bool {
	for _, c := range calls {
		if !offered(c.Function.Name) {
			return false
		}
	}
	return len(calls) > 0
}

// forceGuardArmed decides whether the force-tools guard stays armed for a prose reply
// (02 §3.2 i): question-shaped utterances, non-action utterances whose reply claims no
// action, and utterances matching no command keyword all disarm it.
// A closing remark ("thanks", "got it", "no thanks") never arms it, whatever the reply says.
func (m *Module) forceGuardArmed(conv *conversation, utterance, raw, message string) bool {
	if tf.IsAcknowledgementShaped(utterance) {
		return false
	}
	return forceGate(conv, utterance, []string{parse.OutsideThink(raw), parse.OutsideThink(message)})
}

// maxMustCallRetries is the must-call retry budget per turn (legacy allowed 2; a second nag
// after a model declined once only bought a worse reply or a duplicate call).
const maxMustCallRetries = 1

// toolRanThisTurn reports whether any tool result follows the turn's user message: a server
// tool run in this loop, or the node's results a continue appended. The double-check pass's
// user message is not a turn boundary.
func toolRanThisTurn(msgs []chatMsg) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		switch {
		case msgs[i].Role == "tool":
			return true
		case msgs[i].Role == "user" && !strings.HasPrefix(msgs[i].Content, nagDoubleCheck):
			return false
		}
	}
	return false
}

// toolFailedThisTurn reports whether a tool result since the turn's user message is a failure:
// a JSON object with a non-empty "error" or with "success": false.
func toolFailedThisTurn(msgs []chatMsg) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		switch {
		case msgs[i].Role == "tool":
			var o map[string]any
			if json.Unmarshal([]byte(msgs[i].Content), &o) != nil || o["status"] == "awaiting_confirmation" {
				continue // a parked action asks the user; it didn't fail
			}
			if e, ok := o["error"]; ok && e != nil && e != "" && e != false {
				return true
			}
			if s, ok := o["success"].(bool); ok && !s {
				return true
			}
		case msgs[i].Role == "user" && !strings.HasPrefix(msgs[i].Content, nagDoubleCheck):
			return false
		}
	}
	return false
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

// outputPreviewRunes caps the model output a trace keeps per LLM call: enough to see what a
// slow or odd turn generated (a long preamble before a tool call, the wrong tool), without
// storing every long answer in the trace row.
const outputPreviewRunes = 600

// outputPreview is the trimmed model output for a trace span, cut at outputPreviewRunes runes
// and marked with "…" when cut. Tool calls the engine parsed natively aren't in it; the span's
// finish_reason and the next tool_exec span's tools name them.
func outputPreview(raw string) string {
	s := strings.TrimSpace(raw)
	if r := []rune(s); len(r) > outputPreviewRunes {
		return string(r[:outputPreviewRunes]) + "…"
	}
	return s
}

// llmErrText is what a turn that failed on its LLM call reports: the user-facing sentence when
// the error carries one (a GPU waiting for a reboot), else the error as before.
func llmErrText(err error) string {
	if msg, ok := llm.UserMessage(err); ok {
		return msg
	}
	return err.Error()
}

// trailingBareCall is a reply that ends in a bare {"name", "arguments"} call after prose
// (beyond legacy, #56: qwen3.8-27b wrote "I'll type that now." and then the call with no
// <tool_call> tags; ParseQwen25 only wraps an object that is the whole reply). Only an offered
// tool counts, so prose that quotes some other JSON stays prose.
func trailingBareCall(message string, offered func(string) bool) (parse.ToolCall, string, bool) {
	text := parse.PyStrip(parse.OutsideThink(message))
	for i := strings.IndexByte(text, '{'); i >= 0; {
		obj, isObj := func() (*pyjson.Object, bool) {
			v, err := pyjson.Loads(text[i:])
			o, ok := v.(*pyjson.Object)
			return o, err == nil && ok
		}()
		if isObj {
			name, _ := obj.Get("name")
			n, _ := name.(string)
			_, hasArgs := obj.Get("arguments")
			if !hasArgs || !offered(n) {
				return parse.ToolCall{}, "", false
			}
			pr := parse.ParseToolCalls(text[i:])
			if len(pr.ToolCalls) != 1 {
				return parse.ToolCall{}, "", false
			}
			return pr.ToolCalls[0], parse.PyStrip(text[:i]), true
		}
		j := strings.IndexByte(text[i+1:], '{')
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return parse.ToolCall{}, "", false
}
