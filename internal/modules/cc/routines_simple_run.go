package cc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// This is an adapter to chatStream, not a second tool loop. Its observer records
// actual tool outcomes even when the model's final sentence claims success.
type simpleRoutineRun struct {
	id         string
	result     routineResult
	errors     []string
	incomplete bool
	timedOut   bool
	finished   bool
}

type simpleRoutineKey struct{}

func simpleRun(ctx context.Context) *simpleRoutineRun {
	r, _ := ctx.Value(simpleRoutineKey{}).(*simpleRoutineRun)
	return r
}

func recordRoutineTool(ctx context.Context, name string, output any) {
	r := simpleRun(ctx)
	if r == nil || name == "request_validation" {
		return
	}
	o, _ := output.(*pyjson.Object)
	var success, message, timeout any
	if o != nil {
		success, _ = o.Get("success")
		message, _ = o.Get("error")
		timeout, _ = o.Get("timeout")
		if question, pending := routinePendingQuestion(o); pending {
			r.incomplete, r.finished = true, true
			r.result.Message = "Needs a follow-up: " + question + " Run it by voice or edit its instruction."
			return // a parked action is not completed work
		}
	}
	// Server tools sometimes return data directly, without a success envelope.
	if success != false && !pyTruthy(message) {
		r.result.Passed++
	} else {
		r.result.Failed++
		msg, _ := message.(string)
		if msg == "" {
			msg = name + " failed"
		}
		r.errors = append(r.errors, msg)
	}
	if timeout == true {
		r.timedOut = true
	}
}

func (r *simpleRoutineRun) finish(res engineResult, st *chatState) {
	r.finished = true
	if r.incomplete {
		return
	} // preserve a tool's explicit pending-input explanation
	r.result.Message = stripToolData(res.Message)
	if res.Stop == stopValidation {
		r.incomplete = true
		if res.Validation != nil {
			r.result.Message = "Needs clarification: " + res.Validation.Question + " Edit the instruction or run it by voice."
		}
	} else if res.Stop != stopComplete && res.Stop != stopServerToolComplete {
		r.incomplete = true
	} else if !res.EndOfExchange {
		// The normal exchange-complete marker is the model's explicit indication
		// that no conversational follow-up is needed; never assume a question is done.
		r.incomplete = true
		r.result.Message = "Completion needs a follow-up. " + res.Message + " Run this routine by voice or make its instruction more specific."
	}
	if len(st.actions) > 0 || res.IslandChoice {
		r.incomplete = true
		r.result.Message = "This routine needs confirmation. Run it by voice or edit its instruction."
	}
}

func (m *Module) runSimpleRoutine(ctx context.Context, rt *routineRow, nodeID string) routineResult {
	n, err := m.nodeByID(ctx, nodeID)
	if err != nil || n.householdID.String != rt.householdID {
		return routineResult{Status: "failed", Message: "The routine's node is unavailable."}
	}
	if n.caps()["simple_routines"] != true {
		return routineResult{Status: "failed", Message: "Update Jarvis on this node to run Simple routines."}
	}
	if !m.nodeReachable(n) || m.LLM == nil {
		return routineResult{Status: "failed", Message: "The node or model is unavailable."}
	}
	wctx, cancel := context.WithTimeout(ctx, routineWait)
	defer cancel()
	run := &simpleRoutineRun{id: rt.id}
	wctx = context.WithValue(wctx, simpleRoutineKey{}, run)
	zone := m.householdTimezone(ctx, rt.householdID)
	if s := rt.sched(); s != nil && s.Timezone != "" {
		zone = s.Timezone
	}
	cid := "routine-" + uuid4()
	defer m.convs.evict(cid)
	defer m.signals.drop(cid)
	m.chatStream(wctx, &sseWriter{silent: true}, authn.User{}, n, chatRequest{
		Message: rt.utterance, NodeID: nodeID, HouseholdID: rt.householdID, Timezone: zone, routine: run, ConversationID: cid,
	})
	res := run.result
	switch {
	case run.timedOut || errors.Is(wctx.Err(), context.DeadlineExceeded):
		res.Status, res.Message = "timeout", "The routine did not finish in time. Some actions may have run; it has not been retried."
	case wctx.Err() != nil:
		res.Status, res.Message = "failed", "The routine was interrupted. Some actions may have run."
	case run.incomplete || !run.finished || res.Failed > 0:
		res.Status = "failed"
		if res.Passed > 0 {
			res.Status, res.Success = "partial", true
		}
		if len(run.errors) > 0 {
			res.Message = "Routine finished with issues: " + strings.Join(run.errors, "; ")
		}
	default:
		res.Status, res.Success = "success", true
	}
	m.deps.Log.Info("cc: simple routine finished", "routine_id", rt.id, "node", nodeID,
		"status", res.Status, "passed", res.Passed, "failed", res.Failed)
	return res
}

func (r *simpleRoutineRun) fail(message string) {
	r.finished, r.incomplete = true, true
	r.result.Message = fmt.Sprintf("Routine could not finish: %s", message)
}
