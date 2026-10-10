package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Client tools in a voice turn (go-pi-node.md §1.1 row 18): the node offers its tools at
// /conversation/start; a 202 tool_calls reply is run here, on the same node.Toolset the MQTT
// dispatcher serves (one source of truth), and the results go back through the blocking
// /voice/command/continue until command-center answers with something to say.
//
// Ported from the Python node's _run_conversation_loop / _execute_tools
// (N:utils/command_execution_service.py:763-1080): at most 10 rounds, an identical repeated
// round ends the turn, and when every tool in the last round failed the tool's error is spoken
// instead of the model's reply (small models report success anyway).

// maxToolRounds bounds tool_calls → continue round trips in one turn (Python: max_iterations).
const maxToolRounds = 10

// ToolRun is one tool call a turn executed (for logs, `listen` output and tests).
type ToolRun struct {
	Name    string
	Args    map[string]any
	Success bool
	Error   string
	Took    time.Duration
}

// stuckMessage is what the Python node says when the model repeats an identical round.
const stuckMessage = "Sorry, I wasn't able to complete that request."

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
	results   []ToolResult
	allFailed bool
	firstErr  string // the first failure's spoken form (failure_message, else the tool's error)
}

// runRound executes a round's calls in order (Python runs them sequentially too: a later call
// may depend on an earlier one's side effect).
func (p *Pipeline) runRound(ctx context.Context, log *slog.Logger, t *Turn, calls []ToolCall, userID *int64) roundResult {
	var r roundResult
	r.allFailed = len(calls) > 0
	for _, c := range calls {
		args := node.DecodeArgs(c.Function.Arguments)
		start := p.Now()
		out := p.callTool(ctx, c.Function.Name, args, node.Request{ToolCallID: c.ID, UserID: userID, VoiceCommand: t.Transcript})
		ok, _ := out["success"].(bool)
		run := ToolRun{Name: c.Function.Name, Args: args, Success: ok, Took: p.Now().Sub(start)}
		if !ok {
			run.Error, _ = out["error"].(string)
			if r.firstErr == "" {
				r.firstErr = run.Error
				if c.FailureMessage != nil && strings.TrimSpace(*c.FailureMessage) != "" {
					r.firstErr = strings.TrimSpace(*c.FailureMessage)
				}
			}
		} else {
			r.allFailed = false
		}
		t.Tools = append(t.Tools, run)
		log.Info("tool call", "tool", run.Name, "success", run.Success, "error", run.Error, "took", run.Took.Round(time.Millisecond))
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

// toolLoop runs tool_calls rounds until command-center's reply is something other than
// tool_calls. It returns that reply, or a sentence to speak instead of it (spoken != "").
func (p *Pipeline) toolLoop(ctx context.Context, log *slog.Logger, t *Turn, c *Control, userID *int64) (last *Control, spoken string, err error) {
	prevSig := ""
	var lastRound *roundResult
	for round := 0; c.StopReason == "tool_calls"; round++ {
		if round == maxToolRounds {
			return nil, "", fmt.Errorf("still calling tools after %d rounds", maxToolRounds)
		}
		if len(c.ToolCalls) == 0 {
			return nil, "", fmt.Errorf("stop_reason tool_calls with no tool calls")
		}
		sig := roundSignature(c.ToolCalls)
		if sig == prevSig {
			log.Warn("model repeated the same tool calls; giving up", "round", round)
			if lastRound != nil && lastRound.firstErr != "" {
				return nil, "Sorry, that didn't work: " + lastRound.firstErr, nil
			}
			return nil, stuckMessage, nil
		}
		prevSig = sig
		rr := p.runRound(ctx, log, t, c.ToolCalls, userID)
		lastRound = &rr
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		next, err := p.Server.ContinueTools(ctx, t.ConversationID, rr.results)
		if err != nil {
			return nil, "", fmt.Errorf("voice/command/continue: %w", err)
		}
		c = next
	}
	if c.StopReason == "complete" && lastRound != nil && lastRound.allFailed && lastRound.firstErr != "" {
		// Every tool failed: say so rather than trusting a reply that may claim success.
		return c, "Sorry, that didn't work: " + lastRound.firstErr, nil
	}
	return c, "", nil
}
