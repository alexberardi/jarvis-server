package cc

import (
	"context"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"strings"
)

func routinePendingQuestion(o *pyjson.Object) (string, bool) {
	if o == nil {
		return "", false
	}
	pending, _ := o.Get("requires_follow_up")
	status, _ := o.Get("status")
	choice, _ := o.Get("choice")
	var st chatState
	harvestActions(&st, "", o)
	if pending != true && status != "awaiting_confirmation" && choice != true && len(st.actions) == 0 {
		return "", false
	}
	for _, key := range []string{"question", "message", "island"} {
		if v, _ := o.Get(key); v != nil {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s, true
			}
		}
	}
	return "Please choose or confirm the pending action before I continue.", true
}

func simpleRoutineQuestion(conv *conversation, results []toolResult) string {
	if !conv.simpleRoutine {
		return ""
	}
	for _, r := range results {
		if question, pending := routinePendingQuestion(outputObject(r.Output)); pending {
			return question
		}
	}
	return ""
}

// A nonempty sentinel overwrites stale definitions even on old nodes that keep
// built-in/package slugs when absent from a pull, or fall back on empty steps.
// It has no voice trigger and names no executable command in either node runtime.
func unavailableSimpleRoutine() nodeRoutine {
	return nodeRoutine{TriggerPhrases: []byte("[]"), ResponseLength: "short",
		Steps: []nodeStep{{Command: "__jarvis_simple_routine_requires_update__", Args: map[string]any{}, Label: "unavailable"}}}
}

// Legacy Advanced writes keep their existing validation. Mode-aware writes must
// supply an executable active payload; inactive content can be incomplete.
func validateRoutineMode(b *body, mode, utterance string, stepCount int, modeAware bool) {
	if mode == "simple" && strings.TrimSpace(utterance) == "" {
		b.fail("utterance", "An instruction is required for Simple routines")
	}
	if mode == "advanced" && modeAware && stepCount == 0 {
		b.fail("steps", "At least one step is required for Advanced routines")
	}
}

func (m *Module) validateSimpleSchedule(ctx context.Context, b *body, hh, mode string, enabled bool, s *routineSchedule) {
	if mode != "simple" || !enabled || s == nil || !s.Enabled {
		return
	}
	if s.TargetNodeID == nil || *s.TargetNodeID == "" {
		b.fail("schedule", "Choose a node that supports Simple routines")
		return
	}
	n, err := m.nodeByID(ctx, *s.TargetNodeID)
	if err != nil || n.householdID.String != hh || n.caps()["simple_routines"] != true {
		b.fail("schedule", "Update Jarvis on the scheduled node to run Simple routines")
	}
}
