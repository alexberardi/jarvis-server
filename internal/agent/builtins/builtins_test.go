package builtins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/devices"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
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

func TestMatchesTrigger(t *testing.T) {
	phrases := []any{"good night", "bedtime", "going to bed", "time for bed"}
	for said, want := range map[string]bool{
		"good night":              true,
		"Good night!":             true, // speech-to-text's punctuation
		"okay good night jarvis":  true, // the phrase inside
		"time for bed":            true,
		"bed time for":            true, // 80% of the phrase's words
		"going to bed now":        true,
		"bedtime":                 true,
		"good":                    false, // a lone word isn't the phrase (N: matched it)
		"night":                   false,
		"what's the weather":      false,
		"good thanks":             false,
		"is it time for the news": false,
	} {
		if got := matchesTrigger(normalizeSaid(said), phrases); got != want {
			t.Errorf("%q: %v, want %v", said, got, want)
		}
	}
}

func TestRoutineRunsByItsTriggerPhrase(t *testing.T) {
	f := &fakeCC{reply: "Sleep well."}
	cc := f.start(t)
	steps := &stepTools{calls: map[string]map[string]any{}, outs: map[string]map[string]any{
		"control_device":      {"success": true},
		"get_calendar_events": {"success": true, "message": "Nothing tomorrow."},
		"get_weather":         {"success": true, "message": "Rain."},
	}}
	pantry := map[string]map[string]any{"rainy_day": {
		"trigger_phrases": []any{"is it going to rain"},
		"steps":           []any{map[string]any{"command": "get_weather", "label": "weather"}},
	}}
	tool := &Routine{Tools: steps, CC: func() *node.CC { return cc }, Pantry: func() map[string]map[string]any { return pantry }}
	ctx := node.WithSpeaker(context.Background(), uid(5))

	out, ok := tool.Direct(ctx, "Good night!")
	if !ok || out["success"] != true || out["message"] != "Sleep well." || !node.TakeSignals(out).Speak {
		t.Fatalf("good night = %v, %v", out, ok)
	}
	if _, ran := steps.calls["get_calendar_events"]; !ran {
		t.Fatal("good_night's steps didn't run")
	}
	// A package's routine is found by its own phrase.
	steps.calls = map[string]map[string]any{}
	if _, ok := tool.Direct(ctx, "Hey, is it going to rain today?"); !ok || steps.calls["get_weather"] == nil {
		t.Fatalf("pantry routine: ran %v", steps.calls)
	}
	if _, ok := tool.Direct(ctx, "What's the capital of France?"); ok {
		t.Fatal("ran a routine for an ordinary question")
	}
	// The household turned the routine command off: no trigger runs.
	off := node.WithEnabled(ctx, func(name string) bool { return name != "routine" })
	if _, ok := tool.Direct(off, "good night"); ok {
		t.Fatal("ran with the routine command off")
	}
}

// speakerTools records who a step ran for.
type speakerTools struct{ got *int64 }

func (s *speakerTools) Available(context.Context) []node.ToolDef { return nil }
func (s *speakerTools) Call(_ context.Context, _ string, _ map[string]any, req node.Request) (map[string]any, bool) {
	s.got = req.UserID
	return map[string]any{"success": true, "message": "ok"}, true
}

func TestTriggeredRoutineRunsForTheSpeaker(t *testing.T) {
	st := &speakerTools{}
	tool := &Routine{Tools: st, CC: func() *node.CC { return nil }}
	tool.Direct(node.WithSpeaker(context.Background(), uid(9)), "good morning")
	if st.got == nil || *st.got != 9 {
		t.Fatalf("steps ran for %v", st.got)
	}
}

// A routine whose phrases the household switched off isn't run by them; the others still are.
func TestRoutineFastPathSwitches(t *testing.T) {
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	steps := &stepTools{calls: map[string]map[string]any{}, outs: map[string]map[string]any{
		"control_device": {"success": true, "message": "ok"}, "get_calendar_events": {"success": true, "message": "ok"},
		"get_weather": {"success": true, "message": "ok"}}}
	tool := &Routine{Store: st, Tools: steps, CC: func() *node.CC { return nil }}

	fps := tool.FastPaths("routine")
	if len(fps) != 4 || fps[1].ID != "routine.good_night" || fps[1].Example != "good night" {
		t.Fatalf("fast paths = %+v", fps)
	}
	if tool.FastPaths("get_weather") != nil {
		t.Fatal("fast paths for another command")
	}
	if err := st.SetFastPathEnabled("routine", "routine.good_night", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := tool.Direct(context.Background(), "good night"); ok {
		t.Fatal("ran a routine whose phrases are off")
	}
	if _, ok := tool.Direct(context.Background(), "good morning"); !ok {
		t.Fatal("another routine's phrases stopped working")
	}
}

// "Set a timer for 5 minutes." runs with no model and is spoken; the period speech-to-text adds
// isn't a label (N: answered "Timer set for 5 minutes for ."). A switched-off pattern falls
// through to the model, and a fast path whose command fails does too.
func TestTimerFastPaths(t *testing.T) {
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	svc := &timers.Service{Path: filepath.Join(t.TempDir(), "timers.json")}
	tm := &Timers{Service: svc, Store: st}
	ctx := context.Background()

	out, ok := tm.Direct(ctx, "Set a timer for 5 minutes.")
	if !ok || out["message"] != "Timer set for 5 minutes" || !node.TakeSignals(out).Speak || len(svc.Active()) != 1 {
		t.Fatalf("set = %v, %v, timers %v", out, ok, svc.Active())
	}
	if out, ok := tm.Direct(ctx, "How much time is left?"); !ok || !strings.HasPrefix(out["message"].(string), "Your timer has ") {
		t.Fatalf("check = %v, %v", out, ok)
	}
	if err := st.SetFastPathEnabled("set_timer", "timer.set", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := tm.Direct(ctx, "set a timer for 10 minutes"); ok {
		t.Fatal("a switched-off pattern ran")
	}
	if _, ok := tm.Direct(ctx, "wake me in 20 minutes"); !ok {
		t.Fatal("another pattern of the command stopped working")
	}
	if fps := tm.FastPaths("set_timer"); len(fps) != 3 || fps[0].ID != "timer.set" {
		t.Fatalf("fast paths = %+v", fps)
	}
	off := node.WithEnabled(ctx, func(name string) bool { return name != "cancel_timer" })
	if _, ok := tm.Direct(off, "cancel all timers"); ok {
		t.Fatal("ran a command the household turned off")
	}
	if out, ok := tm.Direct(ctx, "cancel all timers"); !ok || out["message"] != "Cancelled all 2 timers" {
		t.Fatalf("cancel = %v, %v", out, ok)
	}
	// Nothing to cancel is still a success with words, as on N:; nothing to say falls through.
	if out, ok := tm.Direct(ctx, "cancel my timer"); !ok || out["message"] != "No active timers to cancel" {
		t.Fatalf("cancel none = %v, %v", out, ok)
	}
}

// Spoken numbers carry no trailing zeros: "5 miles", "2.5", "212°F".
func TestSpokenNumbersDropTrailingZeros(t *testing.T) {
	c := &Convert{}
	out, _ := c.Call(context.Background(), "convert_measurement", map[string]any{"value": 5.0, "from_unit": "miles", "to_unit": "kilometers"}, node.Request{})
	if out["message"] != "5 miles equals 8.05 kilometers." {
		t.Errorf("miles = %q", out["message"])
	}
	out, _ = c.Call(context.Background(), "convert_measurement", map[string]any{"value": 100.0, "from_unit": "celsius", "to_unit": "fahrenheit"}, node.Request{})
	if out["message"] != "100 celsius equals 212°F." || out["result_text"] != "212°F" {
		t.Errorf("temperature = %q / %q", out["message"], out["result_text"])
	}
	out, _ = (&Calculate{}).Call(context.Background(), "calculate", map[string]any{"num1": 5.0, "num2": 2.0, "operation": "divide"}, node.Request{})
	if out["message"] != "5 divided by 2 equals 2.5." {
		t.Errorf("divide = %q", out["message"])
	}
}

// A volume change ends the exchange; a failed one doesn't.
func TestControlNodeEndsTheExchange(t *testing.T) {
	a := &sdkhost.Audio{Path: filepath.Join(t.TempDir(), "v.json"), Run: func(context.Context, string, ...string) (string, error) {
		return "Volume: front-left: 1 / 50% / 0 dB", nil
	}}
	c := &ControlNode{Audio: a}
	out, _ := c.Call(context.Background(), "control_node", map[string]any{"action": "volume_up"}, node.Request{})
	if !node.TakeSignals(out).ClearHistory || out["message"] != "Volume 60 percent." {
		t.Fatalf("volume_up = %v", out)
	}
	out, _ = c.Call(context.Background(), "control_node", map[string]any{"action": "explode"}, node.Request{})
	if node.TakeSignals(out).ClearHistory {
		t.Fatalf("a failure ended the exchange: %v", out)
	}
}
