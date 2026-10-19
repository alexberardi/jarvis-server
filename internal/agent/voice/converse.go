package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The conversation loop, ported from the Python node's _run_conversation_loop
// (N:utils/command_execution_service.py:733-975): a control reply is acted on until
// command-center has something final to say.
//   - tool_calls are run on this node and continued (tools.go, go-pi-node.md §1.1 row 18);
//   - validation_required is asked aloud, the answer recorded without a wake word and sent
//     back through the same /voice/command/continue (row 19; Python's make_validation_handler
//     and send_validation_response, N:core/wake_transcription.py:204-231,
//     N:clients/jarvis_command_center_client.py:362-412).
// At most maxRounds of either, as Python's max_iterations.

// maxToolRounds bounds tool_calls → continue round trips and validation questions in one
// reply (Python: max_iterations).
const maxToolRounds = 10

// session is what every exchange after one wake shares.
type session struct {
	id         string
	log        *slog.Logger
	chunks     <-chan []int16
	wakeClip   []int16  // prepended to every speaker_audio in this conversation
	threshold  float64  // the wake's silence threshold, reused by every later recording
	wakeLevel  *float64 // the woken command's speech level (dBFS); follow-ups report theirs against it
	userID     *int64   // the last speaker STT identified
	onComplete []func() error
	held       *held // the computer task the wake word paused (computer.go)
}

// ValidationRequest is a 202's validation_request.
type ValidationRequest struct {
	Question      string `json:"question"`
	ParameterName string `json:"parameter_name"`
	Options       []any  `json:"options"`
	// ToolCallID is null from jarvisd (the question comes from its own request_validation
	// tool); the Python node then answers as "validation-response".
	ToolCallID *string `json:"tool_call_id"`
}

func (v *ValidationRequest) options() []string {
	var out []string
	for _, o := range v.Options {
		if s := strings.TrimSpace(fmt.Sprint(o)); o != nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// spoken is the question as the Python node says it: "… Your options are: a, b".
func (v *ValidationRequest) spoken() string {
	q := strings.TrimSpace(v.Question)
	if opts := v.options(); len(opts) > 0 {
		q += " Your options are: " + strings.Join(opts, ", ")
	}
	return q
}

// answer is the tool result that carries the user's answer (send_validation_response).
func (v *ValidationRequest) answer(text string) ToolResult {
	id := "validation-response"
	if v.ToolCallID != nil && *v.ToolCallID != "" {
		id = *v.ToolCallID
	}
	var opts any // null when the question had none, as Python's Optional[List[str]]
	if v.Options != nil {
		opts = v.Options
	}
	return ToolResult{ToolCallID: id, Output: map[string]any{
		"answer": text, "parameter_name": v.ParameterName, "question": v.Question, "options": opts,
	}}
}

// notCaught is what Python sends as the answer when nothing was transcribed.
const notCaught = "I didn't catch that, sorry."

// Messages the Python node falls back on.
const (
	stuckMessage     = "Sorry, I wasn't able to complete that request."
	listeningMessage = "Go ahead, I'm listening." // wait_for_input with nothing to say
)

// resolve acts on c: tool rounds and validation questions until a final reply, which it
// speaks. It returns why the follow-up window must not open (or stay open): "" when it may.
func (p *Pipeline) resolve(ctx context.Context, s *session, ex *Exchange, c *Control) FollowUpEnd {
	prevSig := ""
	var last *roundResult
	for round := 0; c.StopReason == "tool_calls" || c.StopReason == "validation_required"; round++ {
		if round == maxToolRounds {
			p.fail(ctx, ex, fmt.Errorf("still calling tools after %d rounds", maxToolRounds))
			return EndError
		}
		if c.StopReason == "validation_required" {
			if c.Validation == nil {
				p.fail(ctx, ex, errors.New("validation_required without a validation_request"))
				return EndError
			}
			// The user said something new, so the same call is legitimate again.
			prevSig = ""
			text, err := p.askValidation(ctx, s, ex, c.Validation)
			if err != nil {
				p.fail(ctx, ex, err)
				return EndError
			}
			p.cue(CueThinking, 0)
			next, err := p.Server.ContinueTools(ctx, s.id, []ToolResult{c.Validation.answer(text)})
			if err != nil {
				p.fail(ctx, ex, fmt.Errorf("voice/command/continue: %w", err))
				return EndError
			}
			c = next
			continue
		}
		if len(c.ToolCalls) == 0 {
			p.fail(ctx, ex, errors.New("stop_reason tool_calls with no tool calls"))
			return EndError
		}
		sig := roundSignature(c.ToolCalls)
		if sig == prevSig {
			s.log.Warn("model repeated the same tool calls; giving up", "round", round)
			msg := stuckMessage
			if last != nil && last.firstErr != "" {
				msg = "Sorry, that didn't work: " + last.firstErr
			}
			return p.say(ctx, s, ex, msg, "")
		}
		prevSig = sig
		rr := p.runRound(ctx, s, ex, c.ToolCalls)
		last = &rr
		if ctx.Err() != nil {
			ex.Outcome, ex.Err = OutcomeError, ctx.Err()
			return EndError
		}
		next, err := p.Server.ContinueTools(ctx, s.id, rr.results)
		if err != nil {
			p.fail(ctx, ex, fmt.Errorf("voice/command/continue: %w", err))
			return EndError
		}
		c = next
		// A command waiting for input gets the model one reply to its results, never another
		// tool round (Python breaks here so a chatty command can't loop).
		if rr.signals.WaitForInput && c.StopReason != "not_for_me" {
			break
		}
	}

	var sig node.Signals
	if last != nil {
		sig = last.signals
		if f := sig.OnResponseComplete; f != nil {
			s.onComplete = append(s.onComplete, f)
		}
	}
	msg := ""
	if c.AssistantMessage != nil {
		msg = strings.TrimSpace(*c.AssistantMessage)
	}
	switch c.StopReason {
	case "not_for_me":
		ex.Outcome = OutcomeNotForMe
		return EndNotForMe
	case "complete", "", "tool_calls", "validation_required":
		// (tool_calls / validation_required only after a wait_for_input break.)
	default: // "error"
		ex.Outcome, ex.Err = OutcomeError, fmt.Errorf("command-center stopped with %q", c.StopReason)
		if msg != "" {
			ex.Message = msg
			ex.ReplySecs, _ = p.speak(ctx, msg, nil)
		} else {
			p.chime(ctx, errorChime)
		}
		return EndError
	}
	if msg == "" && sig.WaitForInput {
		// Adapter models may put the reply in a tool call's "message" argument.
		for _, tc := range c.ToolCalls {
			if m, _ := node.DecodeArgs(tc.Function.Arguments)["message"].(string); strings.TrimSpace(m) != "" {
				msg = strings.TrimSpace(m)
				break
			}
		}
		if msg == "" {
			msg = listeningMessage
		}
	}
	switch {
	case last != nil && last.allFailed && last.firstErr != "":
		// Every tool failed: say so rather than trusting a reply that may claim success
		// (small models report success anyway).
		msg = "Sorry, that didn't work: " + last.firstErr
	case last != nil && sig.WaitForInput && last.toolMessage != "":
		// An informational command's own words beat the model's reading of its data.
		msg = last.toolMessage
	}
	end := FollowUpEnd("")
	switch {
	case c.EndOfExchange:
		end = EndOfExchange
	case sig.ClearHistory:
		end = EndClearHistory
	}
	if msg == "" {
		// The Python node says "Task completed." here (go-pi-node.md §1.4); say nothing.
		ex.Outcome = OutcomeSilent
		return end
	}
	return p.say(ctx, s, ex, msg, end)
}

// say speaks msg as the exchange's reply. The user may talk over it (barge-in), which ends
// the conversation without a follow-up window.
func (p *Pipeline) say(ctx context.Context, s *session, ex *Exchange, msg string, end FollowUpEnd) FollowUpEnd {
	ex.Message = msg
	secs, err := p.speak(ctx, msg, s)
	ex.ReplySecs += secs
	if errors.Is(err, errBargeIn) {
		ex.Outcome = OutcomeInterrupted
		return EndBargeIn
	}
	if err != nil {
		p.fail(ctx, ex, err)
		return EndError
	}
	ex.Outcome = OutcomeSpoken
	return end
}

// askValidation speaks the question and records the answer, without a wake word. The answer
// is endpointed like a command (same threshold), after the same quiet-wait the Python node's
// listen() does, so the tail of the question isn't taken for the answer.
func (p *Pipeline) askValidation(ctx context.Context, s *session, ex *Exchange, v *ValidationRequest) (string, error) {
	q := v.spoken()
	s.log.Info("validation question", "question", q, "parameter", v.ParameterName)
	secs, err := p.speak(ctx, q, nil)
	ex.ReplySecs += secs
	if err != nil {
		return "", err
	}
	flush(s.chunks) // what was captured while the question played
	s.log.Info("validation: listening for the answer")
	if p.listening != nil {
		p.listening("validation")
	}
	p.cue(CueListening, 0)
	if err := p.quietWait(ctx, s, frames(validationQuietWaitSecs)); err != nil {
		return "", err
	}
	pcm, _, err := p.record(ctx, s)
	if err != nil {
		return "", err
	}
	text := ""
	if len(pcm) > 0 {
		cmd := Normalize(pcm)
		tr, err := p.Server.Transcribe(ctx, wavBytes(cmd), wavBytes(append(append([]int16(nil), s.wakeClip...), cmd...)), s.id)
		if err != nil {
			return "", fmt.Errorf("transcribe: %w", err)
		}
		text = strings.TrimSpace(tr.Text)
	}
	if IsNonSpeech(text) {
		text = notCaught
	}
	ex.Validations = append(ex.Validations, Validation{Question: q, Answer: text})
	s.log.Info("validation answer", "answer", text)
	return text, nil
}
