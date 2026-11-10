package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestSimpleRoutineNeverRunsInactiveSteps(t *testing.T) {
	steps := &stepTools{calls: map[string]map[string]any{}, outs: map[string]map[string]any{"stale_step": {"success": true}}}
	def := map[string]any{"mode": "simple", "utterance": "Play the radio", "trigger_phrases": []any{"radio time"},
		"steps": []any{map[string]any{"command": "stale_step"}}}
	r := &Routine{Tools: steps, CC: func() *node.CC { return nil }, Pantry: func() map[string]map[string]any { return map[string]map[string]any{"radio_time": def} }}
	out, ok := r.Direct(context.Background(), "Radio time!")
	if !ok || len(steps.calls) != 0 {
		t.Fatalf("Simple ran inactive steps: %v, calls=%v", out, steps.calls)
	}
	// A model-directed routine call cannot enter a nested loop or fall back to steps.
	out, _ = r.Call(context.Background(), "routine", map[string]any{"routine_name": "radio_time", "routine_definition": def}, node.Request{})
	if out["success"] != false || len(steps.calls) != 0 {
		t.Fatalf("Simple fell back to steps: %v, calls=%v", out, steps.calls)
	}
}

func TestRoutinePreservesDeferredSteps(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "call", true: "trigger phrase"}[direct], func(t *testing.T) {
			var played []string
			var logs bytes.Buffer
			playErr := errors.New("speaker unavailable")
			steps := &stepTools{calls: map[string]map[string]any{}, outs: map[string]map[string]any{}}
			var definition []any
			for _, name := range []string{"first", "failed", "last"} {
				steps.outs[name] = map[string]any{"success": name != "failed", "message": "Ready.",
					node.SignalsKey: node.Signals{WaitForInput: true, OnResponseComplete: func() error {
						played = append(played, name)
						if name == "first" {
							return playErr
						}
						return nil
					}}}
				definition = append(definition, map[string]any{"command": name})
			}
			tool := &Routine{Tools: steps, Log: slog.New(slog.NewTextHandler(&logs, nil)), CC: func() *node.CC { return nil }, Pantry: func() map[string]map[string]any {
				return map[string]map[string]any{"radio_time": {
					"steps": definition, "trigger_phrases": []any{"radio time"},
				}}
			}}
			var out map[string]any
			if direct {
				out, _ = tool.Direct(context.Background(), "radio time")
			} else {
				out, _ = tool.Call(context.Background(), "routine", map[string]any{"routine_name": "radio_time"}, node.Request{})
			}
			if out["success"] != true || out["passed"] != 2 || out["failed"] != 1 {
				t.Fatalf("routine = %v", out)
			}
			if len(played) != 0 {
				t.Fatalf("started playback before the reply: %v", played)
			}
			sig := node.TakeSignals(out)
			if sig.OnResponseComplete == nil {
				t.Fatal("routine lost the deferred playback")
			}
			if sig.Speak != direct || sig.WaitForInput {
				t.Fatalf("routine conversation flags = %+v", sig)
			}
			if _, err := json.Marshal(out); err != nil {
				t.Fatalf("node-local callbacks leaked into the result: %v", err)
			}
			if err := sig.OnResponseComplete(); err != nil {
				t.Fatalf("partial playback must still engage music handling: %v", err)
			}
			if !strings.Contains(logs.String(), playErr.Error()) {
				t.Fatalf("lost playback error: %s", &logs)
			}
			if !slices.Equal(played, []string{"first", "last"}) {
				t.Fatalf("deferred steps = %v; want successful steps in order, continuing after an error", played)
			}
		})
	}
}
