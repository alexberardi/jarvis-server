package builtins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/devices"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeCC is jarvisd's node API: the device registry, send-link and the LLM passthrough.
type fakeCC struct {
	mu      sync.Mutex
	devices []map[string]any
	sent    bool
	reply   string // the model's answer; "" fails the call
	posts   map[string][]map[string]any
}

func (f *fakeCC) start(t *testing.T) *node.CC {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.posts == nil {
			f.posts = map[string][]map[string]any{}
		}
		f.posts[r.URL.Path] = append(f.posts[r.URL.Path], body)
		switch r.URL.Path {
		case "/api/v0/node/devices":
			_ = json.NewEncoder(w).Encode(f.devices)
		case "/api/v0/node/send-link":
			_ = json.NewEncoder(w).Encode(map[string]any{"sent": f.sent})
		case "/api/v0/node/llm/chat":
			if f.reply == "" {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"content": f.reply})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k"})
}

func (f *fakeCC) last(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.posts[path]
	if len(p) == 0 {
		return nil
	}
	return p[len(p)-1]
}

func uid(n int64) *int64 { return &n }

func TestSendLink(t *testing.T) {
	f := &fakeCC{sent: true}
	cc := f.start(t)
	tool := &SendLink{CC: func() *node.CC { return cc }}
	ctx := context.Background()

	out, _ := tool.Call(ctx, "send_link", map[string]any{"url": "https://example.com/a"}, node.Request{})
	if out["success"] != false || !strings.Contains(out["message"].(string), "couldn't tell who's asking") {
		t.Fatalf("no speaker = %v", out)
	}
	out, _ = tool.Call(ctx, "send_link", map[string]any{"url": "javascript:alert(1)"}, node.Request{UserID: uid(7)})
	if out["success"] != false || out["message"] != "I don't have a valid link to send." {
		t.Fatalf("bad url = %v", out)
	}
	if f.last("/api/v0/node/send-link") != nil {
		t.Fatal("a refused link was posted")
	}

	out, _ = tool.Call(ctx, "send_link", map[string]any{"url": " https://example.com/a ", "title": "An article"}, node.Request{UserID: uid(7)})
	if out["success"] != true || out["message"] != "Sent to your phone." {
		t.Fatalf("send = %v", out)
	}
	want := map[string]any{"user_id": float64(7), "url": "https://example.com/a", "title": "An article", "body": "An article"}
	if got := f.last("/api/v0/node/send-link"); !reflect.DeepEqual(got, want) {
		t.Fatalf("posted %v, want %v", got, want)
	}
	tool.Call(ctx, "send_link", map[string]any{"url": "https://example.com/b"}, node.Request{UserID: uid(7)})
	if got := f.last("/api/v0/node/send-link"); got["title"] != "Link from Jarvis" || got["body"] != "Tap to open" {
		t.Fatalf("default title: posted %v", got)
	}

	f.sent = false
	out, _ = tool.Call(ctx, "send_link", map[string]any{"url": "https://example.com/a"}, node.Request{UserID: uid(7)})
	if out["success"] != false || !strings.Contains(out["message"].(string), "didn't deliver") {
		t.Fatalf("not sent = %v", out)
	}
	if _, ok := tool.Call(ctx, "other", nil, node.Request{}); ok {
		t.Fatal("handled another tool")
	}
}

// fakeFamilies is the sidecar's device side.
type fakeFamilies struct {
	protocols []string
	params    map[string]any
	res       map[string]any
}

func (f *fakeFamilies) Protocols() []string { return f.protocols }
func (f *fakeFamilies) Action(p map[string]any) (map[string]any, error) {
	f.params = p
	return f.res, nil
}

func direct(entityID, name, protocol string) map[string]any {
	return map[string]any{"entity_id": entityID, "name": name, "domain": strings.Split(entityID, ".")[0],
		"protocol": protocol, "source": "direct", "local_ip": "10.0.0.9"}
}

func TestControlDevice(t *testing.T) {
	f := &fakeCC{devices: []map[string]any{
		direct("light.office", "Office Light", "lifx"),
		direct("light.kitchen", "Kitchen Light", "lifx"),
		direct("climate.hall", "Hall Thermostat", "nest"),
	}}
	cc := f.start(t)
	ctx := context.Background()
	reg := &devices.Registry{}
	fam := &fakeFamilies{res: map[string]any{"success": true}}
	tool := &ControlDevice{Devices: reg, Families: fam, CC: func() *node.CC { return cc }}

	// Offered only once a direct device uses a family configured here.
	if len(tool.Available(ctx)) != 0 {
		t.Fatal("offered with an empty registry")
	}
	if _, err := reg.Refresh(ctx, cc); err != nil {
		t.Fatal(err)
	}
	if len(tool.Available(ctx)) != 0 {
		t.Fatal("offered with no family configured")
	}
	fam.protocols = []string{"nest"}
	if defs := tool.Available(ctx); len(defs) != 1 || defs[0].Name != "control_device" {
		t.Fatalf("available = %v", defs)
	}

	out, _ := tool.Call(ctx, "control_device", map[string]any{"device_name": "office light", "action": "turn_on"}, node.Request{UserID: uid(3)})
	if out["success"] != true || out["message"] != "Turn On Office Light" || out["device"] != "Office Light" {
		t.Fatalf("turn_on = %v", out)
	}
	actx, _ := fam.params["context"].(map[string]any)
	if fam.params["command_name"] != "control_device" || fam.params["action_name"] != "turn_on" || fam.params["enabled"] != false ||
		actx["entity_id"] != "light.office" || actx["protocol"] != "lifx" || actx["local_ip"] != "10.0.0.9" {
		t.Fatalf("family got %v", fam.params)
	}

	// The data keys the families read, as the Python command maps them.
	for _, c := range []struct {
		args map[string]any
		key  string
		want any
	}{
		{map[string]any{"action": "set_temperature", "value": "69"}, "temperature", "69"},
		{map[string]any{"action": "set_mode", "value": "heat"}, "mode", "heat"},
		{map[string]any{"action": "set_brightness", "value": "40"}, "brightness", "40"},
		{map[string]any{"action": "set_color", "color": "green"}, "color", "green"},
		{map[string]any{"action": "set_color", "value": "blue"}, "color", "blue"},
		{map[string]any{"action": "volume_up", "value": "3"}, "value", "3"},
	} {
		c.args["device_name"] = "Hall Thermostat"
		tool.Call(ctx, "control_device", c.args, node.Request{})
		if got := fam.params["context"].(map[string]any)[c.key]; got != c.want {
			t.Errorf("%v: %s = %v, want %v", c.args, c.key, got, c.want)
		}
	}

	// An entity_id wins over the name; a made-up one falls back to the name.
	tool.Call(ctx, "control_device", map[string]any{"device_name": "light", "entity_id": "light.kitchen", "action": "turn_off"}, node.Request{})
	if fam.params["context"].(map[string]any)["entity_id"] != "light.kitchen" {
		t.Fatalf("by entity_id: %v", fam.params)
	}
	tool.Call(ctx, "control_device", map[string]any{"device_name": "kitchen", "entity_id": "light.nope", "action": "turn_off"}, node.Request{})
	if fam.params["context"].(map[string]any)["entity_id"] != "light.kitchen" {
		t.Fatalf("made-up entity_id: %v", fam.params)
	}

	out, _ = tool.Call(ctx, "control_device", map[string]any{"device_name": "garage door", "action": "lock"}, node.Request{})
	if out["success"] != false || out["error"] != "Device 'garage door' not found." ||
		!reflect.DeepEqual(out["available_devices"], []string{"Hall Thermostat", "Kitchen Light", "Office Light"}) {
		t.Fatalf("not found = %v", out)
	}
	out, _ = tool.Call(ctx, "control_device", map[string]any{"device_name": "x"}, node.Request{})
	if out["success"] != false || out["error"] != "Both device_name and action are required." {
		t.Fatalf("missing action = %v", out)
	}

	ir := map[string]any{"type": "pin"}
	fam.res = map[string]any{"success": false, "error": "PIN needed", "input_required": ir}
	out, _ = tool.Call(ctx, "control_device", map[string]any{"device_name": "Office Light", "action": "turn_on"}, node.Request{})
	if out["success"] != false || out["error"] != "PIN needed" || !reflect.DeepEqual(out["input_required"], ir) {
		t.Fatalf("family failure = %v", out)
	}
}

// stepTools are the routine's steps: each records its args and answers.
type stepTools struct {
	calls map[string]map[string]any
	outs  map[string]map[string]any
}

func (s *stepTools) Available(context.Context) []node.ToolDef { return nil }
func (s *stepTools) Call(_ context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	out, ok := s.outs[name]
	if !ok {
		return nil, false
	}
	s.calls[name] = args
	return out, true
}

func TestRoutine(t *testing.T) {
	f := &fakeCC{reply: "<think>hmm</think>\nGood morning! Sunny today."}
	cc := f.start(t)
	ctx := context.Background()
	steps := &stepTools{calls: map[string]map[string]any{}, outs: map[string]map[string]any{
		"control_device":      {"success": true, "message": "Turn On Downstairs"},
		"get_weather":         {"success": true, "message": "Sunny, 70."},
		"get_calendar_events": {"success": false, "error": "calendar not set up"},
	}}
	now := time.Date(2026, 10, 27, 7, 0, 0, 0, time.Local)
	tool := &Routine{Tools: steps, CC: func() *node.CC { return cc }, Now: func() time.Time { return now }}

	// The default good_morning: two of three steps pass; the model composes the reply.
	out, _ := tool.Call(ctx, "routine", map[string]any{"routine_name": "good_morning"}, node.Request{})
	if out["success"] != true || out["message"] != "Good morning! Sunny today." || out["passed"] != 2 || out["failed"] != 1 {
		t.Fatalf("good_morning = %v", out)
	}
	if got := steps.calls["get_weather"]["resolved_datetimes"]; !reflect.DeepEqual(got, []any{"2026-10-27"}) {
		t.Fatalf("weather got dates %v", got)
	}
	prompt := f.last("/api/v0/node/llm/chat")["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(prompt, "/no_think\nGive a cheerful morning briefing") || !strings.Contains(prompt, `"Sunny, 70."`) ||
		!strings.Contains(prompt, `{"calendar":"calendar not set up"}`) || !strings.Contains(prompt, "2-4 spoken sentences") {
		t.Fatalf("prompt = %q", prompt)
	}

	// jarvisd's definition (the verb's) wins over the stored or default one; no model: the
	// steps' own messages.
	f.reply = ""
	def := map[string]any{"type": "briefing", "steps": []any{
		map[string]any{"command": "get_weather", "label": "w", "args": map[string]any{"resolved_datetimes": []any{"tomorrow", "2026-12-25"}}},
		map[string]any{"command": "routine", "label": "nested"},
		map[string]any{"command": "get_news", "label": "news"},
	}}
	out, _ = tool.Call(ctx, "routine", map[string]any{"routine_name": "good_morning", "routine_definition": def}, node.Request{})
	if out["success"] != true || out["message"] != "Sunny, 70." || out["passed"] != 1 || out["failed"] != 2 {
		t.Fatalf("inline = %v", out)
	}
	if got := steps.calls["get_weather"]["resolved_datetimes"]; !reflect.DeepEqual(got, []any{"2026-10-28", "2026-12-25"}) {
		t.Fatalf("weather got dates %v", got)
	}
	if _, ran := steps.calls["routine"]; ran {
		t.Fatal("a routine ran a routine")
	}

	out, _ = tool.Call(ctx, "routine", map[string]any{"routine_name": "nope"}, node.Request{})
	if out["success"] != false || out["error"] != "Unknown routine: nope" {
		t.Fatalf("unknown = %v", out)
	}
	out, _ = tool.Call(ctx, "routine", map[string]any{"routine_name": "x", "routine_definition": map[string]any{
		"steps": []any{map[string]any{"command": "get_calendar_events"}}}}, node.Request{})
	if out["success"] != false || out["error"] != "All routine steps failed." || out["failed"] != 1 {
		t.Fatalf("all failed = %v", out)
	}
}
