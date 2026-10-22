package voice

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Client tools in a voice turn (go-pi-node.md §1.1 row 18): the node offers its tools at
// /conversation/start; a 202 tool_calls reply is run here, on the same node.Toolset the MQTT
// dispatcher serves (one source of truth), and the results go back through the blocking
// /voice/command/continue (converse.go) until command-center answers with something to say.
//
// Ported from the Python node's _execute_tools (N:utils/command_execution_service.py:977-1089):
// calls run in order, and what the commands signal to the node alone (wait_for_input,
// clear_history, on_response_complete) is collected per round, never posted.

// ToolRun is one tool call a turn executed (for logs, `listen` output and tests).
type ToolRun struct {
	Name    string
	Args    map[string]any
	Success bool
	Error   string
	Took    time.Duration
}

// toolSchemas renders what this node offers now (capabilities are per call).
func (p *Pipeline) toolSchemas(ctx context.Context) ToolSchemas {
	var s ToolSchemas
	if p.Tools == nil {
		return s
	}
	for _, d := range p.Tools.Available(ctx) {
		s.ClientTools = append(s.ClientTools, d.ClientTool)
		s.Commands = append(s.Commands, d.Command)
	}
	return s
}

// roundSignature identifies a round's calls by name and arguments (Python's
// _get_tool_signature), so a model stuck re-issuing the same call is caught.
func roundSignature(calls []ToolCall) string {
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		args, _ := json.Marshal(node.DecodeArgs(c.Function.Arguments)) // map keys sort
		parts = append(parts, c.Function.Name+string(args))
	}
	slices.Sort(parts)
	return strings.Join(parts, "\x00")
}

// roundResult is one executed round.
type roundResult struct {
	results     []ToolResult
	allFailed   bool
	firstErr    string // the first failure's spoken form (failure_message, else the tool's error)
	toolMessage string // the first successful call's own "message"
	// signals: wait_for_input and clear_history if any call set them; the last deferred step.
	signals node.Signals
}

// runRound executes a round's calls in order (Python runs them sequentially too: a later call
// may depend on an earlier one's side effect).
func (p *Pipeline) runRound(ctx context.Context, s *session, ex *Exchange, calls []ToolCall) roundResult {
	var r roundResult
	r.allFailed = len(calls) > 0
	for _, c := range calls {
		args := node.DecodeArgs(c.Function.Arguments)
		start := p.Now()
		out := p.callTool(ctx, c.Function.Name, args, node.Request{ToolCallID: c.ID, UserID: s.userID, VoiceCommand: StripNonSpeech(ex.Transcript)})
		sig := node.TakeSignals(out)
		r.signals.WaitForInput = r.signals.WaitForInput || sig.WaitForInput
		r.signals.ClearHistory = r.signals.ClearHistory || sig.ClearHistory
		if sig.OnResponseComplete != nil {
			r.signals.OnResponseComplete = sig.OnResponseComplete
		}
		ok, _ := out["success"].(bool)
		run := ToolRun{Name: c.Function.Name, Args: args, Success: ok, Took: p.Now().Sub(start)}
		if !ok && out["status"] == "awaiting_confirmation" {
			// A parked action (dictate.Confirmer.Ask): nothing done yet, so not a success, but a
			// question for the user, not a failure: the model's reply asks it (#420).
			r.allFailed = false
		} else if !ok {
			run.Error, _ = out["error"].(string)
			if r.firstErr == "" {
				r.firstErr = run.Error
				if c.FailureMessage != nil && strings.TrimSpace(*c.FailureMessage) != "" {
					r.firstErr = strings.TrimSpace(*c.FailureMessage)
				}
			}
		} else {
			r.allFailed = false
			if m, _ := out["message"].(string); r.toolMessage == "" && strings.TrimSpace(m) != "" {
				r.toolMessage = strings.TrimSpace(m)
			}
		}
		ex.Tools = append(ex.Tools, run)
		s.log.Info("tool call", "tool", run.Name, "args", node.LogArgs(args), "success", run.Success, "error", run.Error, "error_code", out["error_code"], "took", run.Took.Round(time.Millisecond))
		r.results = append(r.results, ToolResult{ToolCallID: c.ID, Output: out})
	}
	return r
}

// callTool runs one tool under Cfg.ToolTimeout. A tool that ignores its context (the sidecar
// doesn't propagate deadlines yet) or panics can't hold the turn: the user hears about it and
// the model gets a failure it can explain.
func (p *Pipeline) callTool(ctx context.Context, name string, args map[string]any, req node.Request) map[string]any {
	if name == "" {
		return node.Fail("missing tool name")
	}
	if p.Tools == nil {
		return node.Fail("Unknown tool: %s", name)
	}
	timeout := p.Cfg.ToolTimeout
	if timeout <= 0 {
		timeout = 25 * time.Second // the MQTT dispatcher's default
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type answer struct {
		out     map[string]any
		handled bool
	}
	ch := make(chan answer, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				ch <- answer{node.Fail("%s crashed: %v", name, v), true}
			}
		}()
		out, handled := p.Tools.Call(cctx, name, args, req)
		ch <- answer{out, handled}
	}()
	select {
	case a := <-ch:
		switch {
		case !a.handled:
			return node.Fail("Unknown tool: %s", name)
		case a.out == nil:
			return node.Fail("%s returned nothing", name)
		}
		return a.out
	case <-cctx.Done():
		if ctx.Err() != nil {
			return node.Fail("cancelled")
		}
		return node.Fail("%s timed out after %s", name, timeout)
	}
}
