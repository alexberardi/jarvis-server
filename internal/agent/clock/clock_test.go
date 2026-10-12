package clock

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func call(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	ny, _ := time.LoadLocation("America/New_York")
	tool := &Tool{Now: func() time.Time { return time.Date(2026, 10, 12, 4, 55, 0, 0, time.UTC) }, Local: ny}
	out, ok := tool.Call(context.Background(), ToolName, args, node.Request{})
	if !ok {
		t.Fatal("get_current_time not handled")
	}
	return out
}

// TestLocalTimeIsExact: the reason this tool exists. 00:55 is "12:55 AM", not the ambient
// block's quarter-hour "12:45".
func TestLocalTimeIsExact(t *testing.T) {
	for _, args := range []map[string]any{{}, {"location": "here"}, {"location": "local time"}, {"location": "  "}} {
		out := call(t, args)
		if out["message"] != "It's 12:55 AM." || out["current_date"] != "Monday, October 12" || out["location"] != "local" {
			t.Errorf("%v → %v", args, out)
		}
		if out["timezone"] != "America/New_York" {
			t.Errorf("timezone %v", out["timezone"])
		}
	}
}

func TestOtherZones(t *testing.T) {
	for loc, want := range map[string]string{
		"Asia/Tokyo": "It's 1:55 PM in Asia/Tokyo.",
		"tokyo":      "It's 1:55 PM in tokyo.",
		"London":     "It's 5:55 AM in London.",
		"UTC":        "It's 4:55 AM in UTC.",
	} {
		if out := call(t, map[string]any{"location": loc}); out["message"] != want {
			t.Errorf("%s → %v, want %q", loc, out["message"], want)
		}
	}
}

// TestUnknownLocationAnswersLocally: never an error for a location the model made up.
func TestUnknownLocationAnswersLocally(t *testing.T) {
	out := call(t, map[string]any{"location": "the kitchen"})
	if out["success"] != true || out["message"] != "It's 12:55 AM." || out["note"] == nil {
		t.Fatalf("%v", out)
	}
}

func TestOnlyItsOwnTool(t *testing.T) {
	if _, ok := (&Tool{}).Call(context.Background(), "get_weather", nil, node.Request{}); ok {
		t.Fatal("handled another tool")
	}
	defs := (&Tool{}).Available(context.Background())
	if len(defs) != 1 || defs[0].Name != ToolName {
		t.Fatalf("%v", defs)
	}
}
