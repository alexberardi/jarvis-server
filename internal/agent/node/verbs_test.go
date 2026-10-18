package node

import (
	"context"
	"testing"
)

func TestRegisteredVerbsGetTheirDetails(t *testing.T) {
	cc := newFakeCC()
	got := map[string]map[string]any{}
	d := &Dispatcher{CC: cc, Tools: &echoTools{}, Log: quiet, Verbs: map[string]func(context.Context, map[string]any){
		"enroll_voice": func(_ context.Context, det map[string]any) { got["enroll_voice"] = det },
		"verify_voice": func(_ context.Context, det map[string]any) { got["verify_voice"] = det },
	}}
	d.Handle(context.Background(), publish(t, "enroll_voice", map[string]any{"user_id": 7, "request_id": "r1"}))
	d.Handle(context.Background(), publish(t, "verify_voice", map[string]any{"user_id": 7, "request_id": "r2"}))
	d.Handle(context.Background(), publish(t, "tts", map[string]any{"message": "hi"}))
	if got["enroll_voice"]["request_id"] != "r1" || got["enroll_voice"]["user_id"] != float64(7) {
		t.Fatalf("enroll_voice details = %v", got["enroll_voice"])
	}
	if got["verify_voice"]["request_id"] != "r2" {
		t.Fatalf("verify_voice details = %v", got["verify_voice"])
	}
	if len(got) != 2 || len(cc.results)+len(cc.reports)+len(cc.verified) != 0 {
		t.Fatalf("an unregistered verb did something: %v", got)
	}
}

func TestBuiltInVerbsCannotBeReplaced(t *testing.T) {
	cc := newFakeCC()
	cc.valid["r1"] = true
	called := false
	d := &Dispatcher{CC: cc, Tools: &echoTools{}, Log: quiet, Verbs: map[string]func(context.Context, map[string]any){
		"tool_call": func(context.Context, map[string]any) { called = true },
	}}
	d.Handle(context.Background(), publish(t, "tool_call", map[string]any{"reply_request_id": "r1", "command_name": "echo"}))
	if called || cc.results["r1"] == nil {
		t.Fatalf("tool_call went to the registered verb (called=%v, results=%v)", called, cc.results)
	}
}
