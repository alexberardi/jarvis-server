package builtins

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// proposable is the named callback's proposable action in a tool's advertised definition.
func proposable(t *testing.T, defs []any, callback string) map[string]any {
	t.Helper()
	for _, e := range defs {
		if m, _ := e.(map[string]any); m["callback"] == callback {
			return m
		}
	}
	t.Fatalf("no proposable %q in %v", callback, defs)
	return nil
}

// The reminder's set_at and the to-do's add_item are offered after calls
// (docs/cc/phone-followups.md); the reminder keeps every field the pinned Python def had.
func TestBuiltinsOfferFollowUpsAfterCalls(t *testing.T) {
	r := (&Reminder{}).Available(context.Background())[0]
	set := proposable(t, r.Command["proposable_actions"].([]any), "set_at")
	if set["offer_after_calls"] != reminderAfterCalls || set["card_title"] != "Set a reminder?" || set["idempotency_param"] != "idempotency_key" {
		t.Fatalf("set_at: %v", set)
	}
	if def(reminderName).Command["proposable_actions"].([]any)[0].(map[string]any)["offer_after_calls"] != nil {
		t.Fatal("the pinned definition was changed in place")
	}
	td := (&Todo{}).Available(context.Background())[0]
	add := proposable(t, td.Command["proposable_actions"].([]any), "add_item")
	if add["offer_after_calls"] == nil || len(add["params"].([]any)) != 2 {
		t.Fatalf("add_item: %v", add)
	}
}

func TestTodoAddItemCallback(t *testing.T) {
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	td := &Todo{Store: st}
	cb := func(name string, data map[string]any) map[string]any {
		out, ok := td.Callback(map[string]any{"command_name": todoName, "callback_name": name, "data": data, "user_id": float64(5)})
		if !ok {
			t.Fatal("not handled")
		}
		return out
	}
	if _, ok := td.Callback(map[string]any{"command_name": "reminder", "callback_name": "set_at"}); ok {
		t.Fatal("handled another command's callback")
	}
	out := cb("add_item", map[string]any{"task": "Bring your insurance card", "idempotency_key": "call:s1:1"})
	if out["success"] != true || out["context_data"].(map[string]any)["message"] != "Added Bring your insurance card to your to-do list." {
		t.Fatalf("add: %v", out)
	}
	if again := cb("add_item", map[string]any{"task": "bring your insurance card"}); again["context_data"].(map[string]any)["message"] != "Bring your insurance card is already on it." {
		t.Fatalf("repeat: %v", again)
	}
	if r := td.read(); len(r["tasks"].([]string)) != 1 {
		t.Fatalf("list: %v", r)
	}
	if bad := cb("add_item", map[string]any{"task": "  "}); bad["success"] != false {
		t.Fatalf("blank: %v", bad)
	}
	if other := cb("nope", nil); other["success"] != false {
		t.Fatalf("unknown callback: %v", other)
	}
	if off, _ := td.Callback(map[string]any{"command_name": todoName, "callback_name": "add_item", "enabled": false,
		"data": map[string]any{"task": "x"}}); off["success"] != false {
		t.Fatalf("disabled: %v", off)
	}
}
