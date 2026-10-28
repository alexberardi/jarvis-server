package sidecar_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/builtins"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/reminders"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// The Go ports against the pinned Python commands they replace: the same calls on the same
// state give the same output and signals, and the same phrasings are claimed by the same fast
// path with the same arguments. Needs a Python with jarvis-node-setup (JARVIS_SIDECAR_PYTHON).

// pythonBuiltins starts the sidecar on the pinned built-ins as the Pi first loaded them.
func pythonBuiltins(t *testing.T, py string, h *sdkhost.Host) *sidecar.Sidecar {
	t.Helper()
	sc, _ := pythonBuiltinsStore(t, py, h)
	return sc
}

// pythonBuiltinsStore is pythonBuiltins with the settings store its commands keep data in;
// seed writes to it before the commands load (the reminder service reads its records at boot).
func pythonBuiltinsStore(t *testing.T, py string, h *sdkhost.Host, seed ...func(*nodesettings.Store)) (*sidecar.Sidecar, *nodesettings.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := nodesettings.OpenStore(dir, "n1")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range seed {
		f(st)
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{}, "builtins": map[string]any{"package": "commands",
		"skip": []string{"control_device_command", "routine_command", "send_link_command"}}})
	listing := filepath.Join(dir, "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := sidecar.StartWith(context.Background(), py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), sidecar.Options{Storage: st, Host: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	return sc, st
}

// normal is a call's output as JSON sees it, and its wait_for_input.
func normal(t *testing.T, out map[string]any) (map[string]any, bool) {
	t.Helper()
	sig := node.TakeSignals(out)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, sig.WaitForInput
}

// pythonPreRoute is each command's pre_route for each phrase: {phrase: {command, arguments}}.
func pythonPreRoute(t *testing.T, py string, modules map[string]string, phrases []string) map[string]map[string]any {
	t.Helper()
	script := `
import importlib, json, sys
mods, phrases = json.loads(sys.argv[1]), json.loads(sys.argv[2])
cmds = []
for mod, cls in sorted(mods.items()):
    cmds.append(getattr(importlib.import_module("commands." + mod), cls)())
out = {}
for p in phrases:
    for c in cmds:
        r = c.pre_route(p)
        if r is not None:
            out[p] = {"command": c.command_name, "arguments": r.arguments}
            break
print(json.dumps(out))
`
	m, _ := json.Marshal(modules)
	ps, _ := json.Marshal(phrases)
	raw, err := exec.Command(py, "-c", script, string(m), string(ps)).Output()
	if err != nil {
		t.Fatalf("pre_route: %v", err)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func clockAndIDs() (func() time.Time, func() string) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time { return now }, func() string { n++; return fmt.Sprintf("t%07d", n) }
}

func TestTimersMatchPython(t *testing.T) {
	py := nodeCommandsPython(t)
	dir := t.TempDir()
	pyNow, pyID := clockAndIDs()
	goNow, goID := clockAndIDs()
	pyTimers := &timers.Service{Path: filepath.Join(dir, "py.json"), Now: pyNow, NewID: pyID}
	goTimers := &timers.Service{Path: filepath.Join(dir, "go.json"), Now: goNow, NewID: goID}
	sc := pythonBuiltins(t, py, &sdkhost.Host{Timers: pyTimers, Alerts: &sdkhost.Alerts{}})
	port := &builtins.Timers{Service: goTimers}
	ctx := context.Background()

	steps := []struct {
		name string
		args map[string]any
	}{
		{"check_timers", nil},
		{"cancel_timer", nil},
		{"set_timer", map[string]any{}},
		{"set_timer", map[string]any{"duration_seconds": -5}},
		{"set_timer", map[string]any{"duration_seconds": "ten"}},
		{"set_timer", map[string]any{"duration_seconds": 45}},
		{"check_timers", nil},
		{"cancel_timer", nil},
		{"set_timer", map[string]any{"duration_seconds": 600, "label": "pasta"}},
		{"set_timer", map[string]any{"duration_seconds": "5430"}},
		{"set_timer", map[string]any{"duration_seconds": 3725.0, "label": "laundry"}},
		{"check_timers", nil},
		{"check_timers", map[string]any{"label": "pasta"}},
		{"check_timers", map[string]any{"label": "Laundry"}},
		{"check_timers", map[string]any{"label": "eggs"}},
		{"cancel_timer", map[string]any{"label": "eggs"}},
		{"cancel_timer", nil},
		{"cancel_timer", map[string]any{"label": "pasta"}},
		{"cancel_timer", map[string]any{"label": "all"}},
		{"cancel_timer", map[string]any{"label": "pasta"}},
	}
	for i, s := range steps {
		pyOut, ok := sc.Call(ctx, s.name, s.args, node.Request{})
		if !ok {
			t.Fatalf("python didn't run %s", s.name)
		}
		goOut, ok := port.Call(ctx, s.name, s.args, node.Request{})
		if !ok {
			t.Fatalf("go didn't run %s", s.name)
		}
		p, pw := normal(t, pyOut)
		g, gw := normal(t, goOut)
		if !reflect.DeepEqual(p, g) || pw != gw {
			t.Errorf("step %d %s %v:\n python %v (wait %v)\n go     %v (wait %v)", i, s.name, s.args, p, pw, g, gw)
		}
	}

	phrases := []string{
		"set a timer for 5 minutes", "set a 10 minute timer for pasta", "egg timer 7 minutes",
		"set a timer for 1 hour and 30 minutes", "timer for half an hour", "set a timer for an hour",
		"set a quarter hour timer", "wake me in 20 minutes", "let me know in 30 minutes", "notify me in 2 hours",
		"set a timer for 2 minutes 30 seconds", "set a timer", "how much time is left", "how much time is left?",
		"what timers are running", "check my timers", "timer status", "how long do i have left",
		"check the pasta timer", "how long until the egg timer", "is the laundry timer still running?",
		"what's the status of the nap timer", "time left on my tea timer", "cancel all timers", "stop all my timers",
		"cancel my timer", "stop the timer", "cancel timer", "never mind the timer", "cancel the pasta timer",
		"stop the egg timer", "cancel the 5 minute timer", "what's the weather", "remind me in 15 minutes",
	}
	want := pythonPreRoute(t, py, map[string]string{"cancel_timer_command": "CancelTimerCommand",
		"check_timers_command": "CheckTimersCommand", "timer_command": "TimerCommand"}, phrases)
	for _, p := range phrases {
		got, ok := builtins.TimersMatch(p)
		w, wok := want[p]
		if ok != wok || (ok && (got.Command != w["command"] || !reflect.DeepEqual(asMap(t, got.Args), w["arguments"]))) {
			t.Errorf("%q: go %+v (%v), python %v (%v)", p, got, ok, w, wok)
		}
	}
}

func asMap(t *testing.T, m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// parityCase is one call through the Python command and the Go port.
type parityCase struct {
	name string
	args map[string]any
}

// checkParity runs each case through the sidecar and the port and compares output and
// wait_for_input; adjust applies the port's deliberate differences to Python's output first.
func checkParity(t *testing.T, sc *sidecar.Sidecar, port node.Toolset, cases []parityCase, adjust ...func(map[string]any)) {
	t.Helper()
	ctx := context.Background()
	uid := int64(7)
	for i, c := range cases {
		req := node.Request{UserID: &uid, VoiceCommand: c.name}
		pyOut, ok := sc.Call(ctx, c.name, c.args, req)
		if !ok {
			t.Fatalf("python didn't run %s", c.name)
		}
		goOut, ok := port.Call(ctx, c.name, c.args, req)
		if !ok {
			t.Fatalf("go didn't run %s", c.name)
		}
		p, pw := normal(t, pyOut)
		g, gw := normal(t, goOut)
		for _, a := range adjust {
			a(p)
		}
		if !reflect.DeepEqual(p, g) || pw != gw {
			t.Errorf("case %d %s %v:\n python %v (wait %v)\n go     %v (wait %v)", i, c.name, c.args, p, pw, g, gw)
		}
	}
}

// checkFastPaths compares which command claims each phrase, and with what arguments.
func checkFastPaths(t *testing.T, py string, modules map[string]string, phrases []string, match func(string) (builtins.Match, bool)) {
	t.Helper()
	want := pythonPreRoute(t, py, modules, phrases)
	for _, p := range phrases {
		got, ok := match(p)
		w, wok := want[p]
		if ok != wok || (ok && (got.Command != w["command"] || !reflect.DeepEqual(asMap(t, got.Args), w["arguments"]))) {
			t.Errorf("%q: go %+v (%v), python %v (%v)", p, got, ok, w, wok)
		}
	}
}

func TestCalculateMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	sc := pythonBuiltins(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}})
	var cases []parityCase
	for _, a := range [][3]any{
		{6, 7, "multiply"}, {5, 3, "add"}, {50, 13, "subtract"}, {81, 9, "divide"}, {10, 3, "divide"}, {1, 0, "divide"},
		{3.5, 2.1, "add"}, {0.1, 0.2, "add"}, {0.2, 150, "multiply"}, {1e17, 3, "multiply"}, {-4, 2.5, "multiply"},
		{0.0001, 0.00002, "multiply"}, {2, 3, "power"}, {"6", 7, "add"}, {true, 2, "add"}, {7, nil, "add"},
	} {
		args := map[string]any{"num1": a[0], "num2": a[1], "operation": a[2]}
		if a[1] == nil {
			delete(args, "num2")
		}
		cases = append(cases, parityCase{"calculate", args})
	}
	checkParity(t, sc, &builtins.Calculate{}, cases, spokenWithoutZeros("message"))
	checkFastPaths(t, py, map[string]string{"calculator_command": "CalculatorCommand"}, []string{
		"what's 5 plus 3", "what is 12 times 8", "12 x 8", "100 divided by 7", "9 multiplied by 9", "what's 7 minus -2?",
		"3.5 over 2", "add 5 and 3", "subtract 7 from 22", "multiply 6 by 7", "divide 72 by 8", "what's seven times nine",
		"5 + 3", "5 / 2", "calculate 5 plus 3",
	}, builtins.CalculateMatch)
}

func TestConvertMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	sc := pythonBuiltins(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}})
	var cases []parityCase
	for _, a := range []map[string]any{
		{"value": 5, "from_unit": "miles", "to_unit": "kilometers"}, {"from_unit": "gallons", "to_unit": "cups"},
		{"value": 1, "from_unit": "cups", "to_unit": "tablespoons"}, {"value": 350, "from_unit": "fahrenheit", "to_unit": "celsius"},
		{"value": 0, "from_unit": "celsius", "to_unit": "kelvin"}, {"value": 300, "from_unit": "kelvin", "to_unit": "fahrenheit"},
		{"value": 20, "from_unit": "celsius", "to_unit": "celsius"}, {"value": 2.5, "from_unit": "pounds", "to_unit": "grams"},
		{"value": 1, "from_unit": "teaspoons", "to_unit": "gallons"}, {"value": 3, "from_unit": "milligrams", "to_unit": "tons"},
		{"value": 12, "from_unit": "Inches", "to_unit": "FEET"}, {"value": 4, "from_unit": "miles", "to_unit": "liters"},
		{"value": 4, "from_unit": "mile", "to_unit": "meters"}, {"value": 4, "from_unit": "meters", "to_unit": "furlongs"},
		{"value": 4, "from_unit": "meters", "to_unit": "meters", "category": "Distance"}, {"value": nil, "from_unit": "cups", "to_unit": "liters"},
		{"value": 4, "from_unit": "cups", "to_unit": "liters", "category": nil}, {"value": "4", "from_unit": "cups", "to_unit": "liters"},
		{"from_unit": "cups"}, {"value": 0.0005, "from_unit": "grams", "to_unit": "kilograms"}, {"value": 1e15, "from_unit": "millimeters", "to_unit": "meters"},
	} {
		cases = append(cases, parityCase{"convert_measurement", a})
	}
	checkParity(t, sc, &builtins.Convert{}, cases, spokenWithoutZeros("message", "result_text"))
	checkFastPaths(t, py, map[string]string{"measurement_conversion_command": "MeasurementConversionCommand"}, []string{
		"convert 5 miles to kilometers", "convert 3 cups into ml", "convert 10 fl oz to tbsp", "how many cups in a gallon",
		"how many teaspoons are in 3 tablespoons?", "how many feet in an inch", "what's 350 fahrenheit in celsius",
		"what is 100 degrees c in degrees f", "20 k in c", "5 km in miles", "what's 2 fluid ounces in ml",
		"convert 5 parsecs to meters", "how many grams in a pound.", "convert 1 metric ton to kg", "what's 3 in in cm",
	}, builtins.ConvertMatch)
}

func TestTalkMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	sc := pythonBuiltins(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}})
	checkParity(t, sc, builtins.Talk{}, []parityCase{
		{"answer_question", map[string]any{"query": "why is the sky blue"}}, {"answer_question", nil},
		{"answer_question", map[string]any{"query": 42}}, {"chat", map[string]any{"message": "hi there"}},
		{"chat", map[string]any{"message": ""}}, {"chat", nil}, {"chat", map[string]any{"message": 3}},
	})
}

// fakeLLM is jarvisd's node LLM passthrough: it answers reply and records each prompt.
type fakeLLM struct {
	mu      sync.Mutex
	reply   string
	prompts []string
}

func (f *fakeLLM) start(t *testing.T) *node.CC {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		if len(body.Messages) > 0 {
			f.prompts = append(f.prompts, body.Messages[0].Content)
		}
		reply := f.reply
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content": reply})
	}))
	t.Cleanup(srv.Close)
	return node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k"})
}

func (f *fakeLLM) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.prompts
	f.prompts = nil
	return p
}

// The Python tell_joke reaches the model through the old node's own client and service
// discovery, which a Go Pi doesn't have: it always fell back to the bank. The port asks the
// model through jarvisd's node passthrough, so only the fallback is compared with Python; the
// model path is checked against what the Python command's cleaning would give.
func TestJokeMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	llm := &fakeLLM{}
	cc := llm.start(t)
	h := &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}}
	h.SetCC(cc)
	sc := pythonBuiltins(t, py, h)
	port := &builtins.Joke{CC: func() *node.CC { return cc }}
	ctx := context.Background()
	for _, c := range []struct {
		reply string
		args  map[string]any
		want  string // the joke from the model; "" when the bank answers
	}{
		{"Here's a joke: Why did the cat sit on the computer? To keep an eye on the mouse.", map[string]any{"topic": "cats"},
			"Why did the cat sit on the computer? To keep an eye on the mouse."},
		{"<think>hmm</think>Okay! \"I used to be a banker, but I lost interest.\"", nil, "I used to be a banker, but I lost interest."},
		{"Ha.", map[string]any{"topic": "dad jokes"}, ""}, // too short: the bank answers
		{"Knock knock. Who's there? Interrupting cow. Interrupting c- MOO!", map[string]any{"topic": "knock knock"},
			"Knock knock. Who's there? Interrupting cow. Interrupting c- MOO!"},
		{"", map[string]any{"topic": 7}, ""},
	} {
		llm.reply = c.reply
		pyOut, _ := sc.Call(ctx, "tell_joke", c.args, node.Request{})
		llm.take()
		goOut, _ := port.Call(ctx, "tell_joke", c.args, node.Request{})
		goPrompts := llm.take()
		p, pw := normal(t, pyOut)
		g, gw := normal(t, goOut)
		if p["_validation_error"] == true || g["_validation_error"] == true {
			if !reflect.DeepEqual(p, g) {
				t.Errorf("%v:\n python %v\n go     %v", c.args, p, g)
			}
			continue
		}
		if p["fallback_used"] != true {
			t.Errorf("%v: the Python command reached the model on a Go node: %v", c.args, p)
		}
		if len(asList(g["style_examples"])) != 2 || len(goPrompts) != 1 ||
			!strings.Contains(goPrompts[0], "Generate ONE original, clean, family-friendly joke") {
			t.Errorf("%v: go examples %v, prompts %q", c.args, g["style_examples"], goPrompts)
		}
		if c.want != "" {
			if g["message"] != c.want || g["joke"] != c.want || g["fallback_used"] != false || gw {
				t.Errorf("%v: go %v", c.args, g)
			}
			continue
		}
		for _, m := range []map[string]any{p, g} { // each side's own random joke from the bank
			delete(m, "message")
			delete(m, "joke")
			delete(m, "style_examples")
		}
		if !reflect.DeepEqual(p, g) || pw != gw {
			t.Errorf("%v fallback:\n python %v (wait %v)\n go     %v (wait %v)", c.args, p, pw, g, gw)
		}
	}
	checkFastPaths(t, py, map[string]string{"tell_a_joke_command": "TellAJokeCommand"}, []string{
		"tell me a joke", "Tell me a joke!", "tell me another joke", "make me laugh", "got any jokes?", "tell me a dad joke",
		"give me a knock-knock joke", "give me a knock knock joke", "tell me a joke about cats", "got any jokes about the ocean",
		"tell me a clean joke", "i want a joke about pizza.", "give me joke", "tell me something", "say something funny",
	}, builtins.JokeMatch)
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// The ports say "5" and "2.5" where N: said "5.0" and "2.50" (the user, 2026-10-27).
var (
	zeroFractionRe = regexp.MustCompile(`\b(\d+)\.0+\b`)
	trailingZeroRe = regexp.MustCompile(`\b(\d+\.\d*[1-9])0+\b`)
)

func spokenWithoutZeros(keys ...string) func(map[string]any) {
	return func(m map[string]any) {
		for _, k := range keys {
			if s, ok := m[k].(string); ok {
				m[k] = zeroFractionRe.ReplaceAllString(trailingZeroRe.ReplaceAllString(s, "$1"), "$1")
			}
		}
	}
}

// fakeMixer is pactl: a volume at 50% that every set or mute call changes.
type fakeMixer struct {
	mu    sync.Mutex
	level int
	fail  bool
}

func (m *fakeMixer) run(_ context.Context, name string, args ...string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return "", fmt.Errorf("exit status 1")
	}
	if len(args) > 2 && args[0] == "set-sink-volume" {
		fmt.Sscanf(args[2], "%d%%", &m.level)
	}
	return fmt.Sprintf("Volume: front-left: 1 / %d%% / 0 dB", m.level), nil
}

func TestControlNodeMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	dir := t.TempDir()
	pyMix, goMix := &fakeMixer{level: 50}, &fakeMixer{level: 50}
	sc := pythonBuiltins(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(dir, "t.json")}, Alerts: &sdkhost.Alerts{},
		Audio: &sdkhost.Audio{Path: filepath.Join(dir, "py.json"), Run: pyMix.run}})
	port := &builtins.ControlNode{Audio: &sdkhost.Audio{Path: filepath.Join(dir, "go.json"), Run: goMix.run}}
	var cases []parityCase
	for _, a := range []map[string]any{
		{"action": "volume_up"}, {"action": "Volume_Down"}, {"action": "set_volume", "target_percent": 35},
		{"action": "set_volume", "target_percent": 140}, {"action": "set_volume"}, {"action": "set_volume", "target_percent": "x"},
		{"action": "mute"}, {"action": "unmute"}, {"action": "explode"}, {}, {"action": "set_volume", "target_percent": 7.0},
	} {
		cases = append(cases, parityCase{"control_node", a})
	}
	checkParity(t, sc, port, cases)
	pyMix.fail, goMix.fail = true, true
	checkParity(t, sc, port, []parityCase{{"control_node", map[string]any{"action": "volume_up"}},
		{"control_node", map[string]any{"action": "set_volume", "target_percent": 20}}, {"control_node", map[string]any{"action": "mute"}}})
	checkFastPaths(t, py, map[string]string{"control_node_command": "ControlNodeCommand"}, []string{
		"volume up", "turn the volume up", "louder", "turn it down", "quieter please", "set the volume to 5", "set volume to 50%",
		"change the volume to 80", "volume 7", "volume 30%", "mute", "please mute the speaker.", "unmute", "mute the tv",
		"make it louder", "crank it up", "what's the volume", "volume", "put volume at 3",
	}, builtins.ControlNodeMatch)
}

func TestCheckAlertsMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	llm := &fakeLLM{reply: "<think>x</think>Heads up: a storm is coming tonight."}
	cc := llm.start(t)
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	pyQ, goQ := &sdkhost.Alerts{Now: clock}, &sdkhost.Alerts{Now: clock}
	h := &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(dir, "t.json")}, Alerts: pyQ}
	h.SetCC(cc)
	sc := pythonBuiltins(t, py, h)
	port := &builtins.CheckAlerts{Alerts: goQ, CC: func() *node.CC { return cc }}
	add := func() {
		for _, q := range []*sdkhost.Alerts{pyQ, goQ} {
			q.Add(sdkhost.Alert{ID: "a1", SourceAgent: "weather", Title: "Storm warning", Summary: "Storm tonight", Priority: 3,
				CreatedAt: now, ExpiresAt: now.Add(time.Hour), Category: "weather"})
		}
	}
	add()
	checkParity(t, sc, port, []parityCase{
		{"check_alerts", nil}, {"check_alerts", nil}, // composed, then none left
		{"check_alerts", map[string]any{"alerts_json": `[{"title": "Mail", "summary": "2 new"}]`}},
		{"check_alerts", map[string]any{"alerts_json": "not json"}},
		{"check_alerts", map[string]any{"dismiss": "true"}}, {"check_alerts", map[string]any{"dismiss": "maybe"}},
	})
	add()
	checkParity(t, sc, port, []parityCase{{"check_alerts", map[string]any{"dismissed": true}}, {"check_alerts", nil}})
	llm.reply = "" // the model down: the titles
	add()
	checkParity(t, sc, port, []parityCase{{"check_alerts", nil}})
	if pyQ.Count() != 0 || goQ.Count() != 0 {
		t.Fatalf("queues: python %d, go %d", pyQ.Count(), goQ.Count())
	}

	// Which fast path claims each phrase, with an alert waiting (pre_route's backends stubbed).
	phrases := []string{"what's up", "Whats up?", "any alerts", "hey what's new with you today friend", "what did i miss",
		"up", "check notifications", "clear my alerts", "dismiss all of the notifications", "cancel that alert",
		"don't clear my alerts", "clear notification sounds", "cancel the timer and read my notifications",
		"delete pending alerts please", "remove the notification settings", "what's the weather"}
	script := `
import json, sys
import commands.whats_up_command as w
w.alerts_count = lambda: 1
w.alerts_flush = lambda: []
c = w.WhatsUpCommand()
out = {}
for p in json.loads(sys.argv[1]):
    r = c.pre_route(p)
    out[p] = "" if r is None else ("dismiss" if "dismissed" in r.arguments else "greeting")
print(json.dumps(out))
`
	ps, _ := json.Marshal(phrases)
	raw, err := exec.Command(py, "-c", script, string(ps)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	_ = json.Unmarshal(raw, &want)
	for _, p := range phrases {
		if got := builtins.AlertsMatch(p); got != want[p] {
			t.Errorf("%q: go %q, python %q", p, got, want[p])
		}
	}
}

func TestActOnItemsMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	sc := pythonBuiltins(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}})
	checkParity(t, sc, builtins.ActOnItems{}, []parityCase{
		{"act_on_items", map[string]any{"action": "mark_read", "ref_ids": []any{"e1"}}}, {"act_on_items", nil},
		{"act_on_items", map[string]any{"action": "mark_read", "ref_ids": "e1"}},
	})
}

// storedRecords is a command's records as JSON, timestamps blanked (Python reads the real clock).
func storedRecords(t *testing.T, st *nodesettings.Store, command string) string {
	t.Helper()
	var out []string
	for _, r := range st.Records(command, time.Now()) {
		b, _ := json.Marshal(r.Data)
		out = append(out, r.Key+"="+isoStamp.ReplaceAllString(string(b), `"T"`))
	}
	return strings.Join(out, "\n")
}

var isoStamp = regexp.MustCompile(`"\d{4}-\d\d-\d\dT[0-9:.]+\+00:00"`)

func TestShoppingMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	sc, pySt := pythonBuiltinsStore(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}})
	goSt, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	port := &builtins.Shopping{Store: goSt}
	var cases []parityCase
	for _, a := range []map[string]any{
		{"action": "read"}, {"action": "add", "items": []any{"milk", "Almond Butter", "eggs"}},
		{"action": "add", "items": "bread, cheese and some apples"}, {"items": []any{"milk"}},
		{"action": "add", "items": []any{"milk"}, "regular": "true"}, {"action": "add", "items": []any{"coffee"}, "regular": true},
		{"action": "read"}, {"action": "check_off", "items": []any{"milk", "butter", "kiwi"}},
		{"action": "add", "items": []any{"milk"}}, {"action": "remove", "items": []any{"eggs", "kiwi"}},
		{"action": "remove", "items": []any{"kiwi"}}, {"action": "check_off"}, {"action": "add", "items": []any{"  ", "!!"}},
		{"action": "fly"}, {"action": "clear", "scope": "sometimes"}, {"action": "read"}, {"action": "check_off", "items": []any{"coffee"}},
		{"action": "clear"}, {"action": "read"}, {"action": "clear", "scope": "everything"}, {"action": "clear", "scope": "everything"},
		{"action": "add", "items": 5}, {},
	} {
		cases = append(cases, parityCase{"shopping_list", a})
	}
	checkParity(t, sc, port, cases)
	// The same records, key for key and field for field.
	cases = cases[:0]
	for _, a := range []map[string]any{
		{"action": "add", "items": []any{"oat milk", "Peanut Butter"}}, {"action": "add", "items": []any{"rice"}, "regular": true},
		{"action": "check_off", "items": []any{"butter"}},
	} {
		cases = append(cases, parityCase{"shopping_list", a})
	}
	checkParity(t, sc, port, cases)
	p, g := storedRecords(t, pySt, "shopping_list"), storedRecords(t, goSt, "shopping_list")
	t.Logf("records:\n%s", p)
	if p != g || p == "" {
		t.Errorf("stored records:\n python %s\n go     %s", p, g)
	}
	checkFastPaths(t, py, map[string]string{"shopping_list_command": "ShoppingListCommand"}, []string{
		"add milk to the shopping list", "put some eggs, bread, and a few apples on my grocery list", "add milk to my list",
		"what's on the shopping list?", "read me the grocery list", "check milk off the shopping list",
		"cross out eggs from the groceries list", "remove the milk from our shopping list", "take bread off of the grocery list",
		"clear the shopping list", "wipe my grocery list.", "remind me to buy milk", "add mac and cheese to the shopping list",
	}, builtins.ShoppingMatch)
}

func TestShoppingSummariesMatchPython(t *testing.T) {
	py := nodeCommandsPython(t)
	sc := pythonBuiltins(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}})
	var records []nodesettings.Object
	for _, r := range []map[string]any{
		{"item": "milk", "checked": false, "regular": false, "quantity": 1},
		{"item": "Eggs", "checked": true, "regular": true, "quantity": 3},
		{"checked": true}, {"item": "bread", "quantity": nil, "regular": true},
	} {
		o, _ := nodesettings.ObjectOf(r)
		records = append(records, o)
	}
	pySum, err := sc.Summaries(context.Background(), "shopping_list", records)
	if err != nil {
		t.Fatal(err)
	}
	goSum, _ := (&builtins.Shopping{}).Summaries(context.Background(), "shopping_list", records)
	if p, g := asJSON(pySum), asJSON(goSum); p != g {
		t.Errorf("summaries:\n python %s\n go     %s", p, g)
	}
	want := asJSON(sc.DataCommands())
	for _, d := range (&builtins.Shopping{}).DataCommands() {
		if !strings.Contains(want, asJSON(d)) {
			t.Errorf("browser settings %s not among python's %s", asJSON(d), want)
		}
	}
}

func TestTodoMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	sc, pySt := pythonBuiltinsStore(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}})
	goSt, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	port := &builtins.Todo{Store: goSt}
	var cases []parityCase
	for _, a := range []map[string]any{
		{"action": "read"}, {"action": "add", "tasks": []any{"call mom and dad", "Fold the Laundry"}},
		{"action": "add", "tasks": "walk the dog"}, {"tasks": []any{"call mom and dad"}}, {"action": "read"},
		{"action": "complete", "tasks": []any{"laundry", "taxes"}}, {"action": "add", "tasks": []any{"fold the laundry"}},
		{"action": "uncomplete", "tasks": []any{"fold the laundry"}}, {"action": "complete", "tasks": []any{"walk the dog"}},
		{"action": "remove", "tasks": []any{"mom", "taxes"}}, {"action": "complete"}, {"action": "add", "tasks": []any{" "}},
		{"action": "fly"}, {"action": "clear", "scope": "someday"}, {"action": "add", "tasks": []any{"pay rent"}},
		{"action": "complete", "tasks": []any{"pay rent"}}, {"action": "clear", "scope": "completed"}, {"action": "read"},
		{"action": "clear", "scope": "completed"},
	} {
		cases = append(cases, parityCase{"todo_list", a})
	}
	checkParity(t, sc, port, cases)
	checkParity(t, sc, port, []parityCase{{"todo_list", map[string]any{"action": "add", "tasks": []any{"book flights"}}}})
	p, g := storedRecords(t, pySt, "todo_list"), storedRecords(t, goSt, "todo_list")
	if p != g || p == "" {
		t.Errorf("stored records:\n python %s\n go     %s", p, g)
	}
	checkParity(t, sc, port, []parityCase{{"todo_list", map[string]any{"action": "clear"}}, {"todo_list", map[string]any{"action": "clear"}}})

	var records []nodesettings.Object
	for _, r := range []map[string]any{{"task": "call mom", "completed": false}, {"task": "taxes", "completed": true}, {}} {
		o, _ := nodesettings.ObjectOf(r)
		records = append(records, o)
	}
	pySum, _ := sc.Summaries(context.Background(), "todo_list", records)
	goSum, _ := port.Summaries(context.Background(), "todo_list", records)
	if a, b := asJSON(pySum), asJSON(goSum); a != b {
		t.Errorf("summaries:\n python %s\n go     %s", a, b)
	}
	checkFastPaths(t, py, map[string]string{"todo_list_command": "TodoListCommand"}, []string{
		"add call the dentist to my to-do list", "put walk the dog and feed the cat on the todo list", "what's on my to do list?",
		"mark the laundry done on my to-do list", "mark the laundry as completed on my task list",
		"mark the laundry on my to-do list as done", "mark the laundry as not done on my to-do list",
		"mark the laundry on my todo list as not finished", "mark do not disturb on my todo list as done",
		"mark do not disturb as done on my todo list", "check off the taxes from my to-do list", "uncheck the taxes on my todo list",
		"remove pay rent from my to-do list", "clear out my to-do list", "wipe the task list.", "remind me to call mom",
		"mark the laundry as unfinished on my to-do list", "cross out buy milk on the to-do list",
	}, builtins.TodoMatch)
}

// fakeInbox is jarvisd's /node/inbox-item: it records each posted item and answers sent.
type fakeInbox struct {
	mu    sync.Mutex
	items []map[string]any
	sent  bool
}

func (f *fakeInbox) start(t *testing.T) *node.CC {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.items = append(f.items, body)
		sent := f.sent
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"sent": sent})
	}))
	t.Cleanup(srv.Close)
	return node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k"})
}

func (f *fakeInbox) take() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.items
	f.items = nil
	return out
}

func TestExportTodoMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	inbox := &fakeInbox{sent: true}
	cc := inbox.start(t)
	h := &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}}
	h.SetCC(cc)
	sc, pySt := pythonBuiltinsStore(t, py, h)
	goSt, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	port := &builtins.ExportTodo{Store: goSt, CC: func() *node.CC { return cc }}
	ctx := context.Background()
	// The same to-do list in both stores, added in a known order (read sorts by added_at).
	for i, task := range []map[string]any{{"task": "Fold the laundry", "completed": false}, {"task": "pay rent", "completed": true},
		{"task": "call mom", "completed": false}, {"task": strings.Repeat("x", 130), "completed": false}} {
		task["added_at"] = fmt.Sprintf("2026-10-27T1%d:00:00+00:00", i)
		task["completed_at"] = nil
		o, _ := nodesettings.ObjectOf(task)
		key := strings.ToLower(strings.ReplaceAll(task["task"].(string), " ", "_"))
		_ = pySt.SaveData("todo_list", key, o, nil)
		_ = goSt.SaveData("todo_list", key, o, nil)
	}
	compare := func(label string, args map[string]any, uid *int64) {
		t.Helper()
		pyOut, _ := sc.Call(ctx, "export_todo_list", args, node.Request{UserID: uid})
		pyPosted := inbox.take()
		goOut, _ := port.Call(ctx, "export_todo_list", args, node.Request{UserID: uid})
		goPosted := inbox.take()
		p, pw := normal(t, pyOut)
		g, gw := normal(t, goOut)
		if !reflect.DeepEqual(p, g) || pw != gw {
			t.Errorf("%s:\n python %v\n go     %v", label, p, g)
		}
		if a, b := asJSON(pyPosted), asJSON(goPosted); a != b {
			t.Errorf("%s posted:\n python %s\n go     %s", label, a, b)
		}
		if wantPost := uid != nil; (len(goPosted) == 1) != wantPost {
			t.Errorf("%s: %d items posted", label, len(goPosted))
		}
		if label == "export" {
			t.Logf("posted: %s", asJSON(goPosted))
		}
	}
	seven := int64(7)
	compare("unknown speaker", nil, nil)
	compare("export", nil, &seven)
	inbox.sent = false
	compare("not delivered", nil, &seven)
	inbox.sent = true

	// Save: the user ticked call mom and left pay rent unticked; laundry stays open.
	for _, data := range []map[string]any{
		{"selected": []any{map[string]any{"key": "call_mom"}}, "context": map[string]any{"keys": []any{"fold_the_laundry", "pay_rent", "call_mom", "gone"}}},
		{"selected": []any{map[string]any{"key": "call_mom"}}, "context": map[string]any{"keys": []any{"call_mom"}}},
		{"selected": []any{}},
	} {
		params := map[string]any{"command_name": "export_todo_list", "callback_name": "save_todo_state", "data": data, "user_id": 7,
			"conversation_id": "c", "voice_command": "cb", "enabled": true}
		pyRes, err := sc.Callback(params)
		if err != nil {
			t.Fatal(err)
		}
		goRes, _ := port.Callback(params)
		if a, b := asJSON(pyRes), asJSON(goRes); a != b {
			t.Errorf("save %v:\n python %s\n go     %s", data, a, b)
		}
		if a, b := storedRecords(t, pySt, "todo_list"), storedRecords(t, goSt, "todo_list"); a != b {
			t.Errorf("records after save:\n python %s\n go     %s", a, b)
		}
	}
	for _, params := range []map[string]any{
		{"command_name": "export_todo_list", "callback_name": "nope", "data": map[string]any{}, "enabled": true},
		{"command_name": "export_todo_list", "callback_name": "save_todo_state", "data": map[string]any{}, "enabled": false},
	} {
		pyRes, _ := sc.Callback(params)
		goRes, _ := port.Callback(params)
		if a, b := asJSON(pyRes), asJSON(goRes); a != b {
			t.Errorf("%v:\n python %s\n go     %s", params, a, b)
		}
	}
	checkFastPaths(t, py, map[string]string{"export_todo_list_command": "ExportTodoListCommand"}, []string{
		"send my to-do list to my phone", "export the task list", "send me my todo list", "show my to-do list on my phone",
		"pull up the to do list on phone.", "show me my to-do list", "send my shopping list",
	}, builtins.ExportTodoMatch)
}

func TestExportShoppingMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	inbox := &fakeInbox{sent: true}
	cc := inbox.start(t)
	h := &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}}
	h.SetCC(cc)
	sc, pySt := pythonBuiltinsStore(t, py, h)
	goSt, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	port := &builtins.ExportShopping{Store: goSt, CC: func() *node.CC { return cc }}
	ctx := context.Background()
	both := func(f func(st *nodesettings.Store)) { f(pySt); f(goSt) }
	both(func(st *nodesettings.Store) {
		for _, r := range []map[string]any{ // a fixed order: the store keeps insertion order
			{"item": "milk", "checked": false, "regular": true, "quantity": 2},
			{"item": "eggs", "checked": false, "regular": false, "quantity": 1},
			{"item": "bread", "checked": true, "regular": false, "quantity": 1},
			{"item": "coffee", "checked": false, "regular": false, "quantity": 150},
		} {
			o, _ := nodesettings.ObjectOf(r)
			_ = st.SaveData("shopping_list", r["item"].(string), o, nil)
		}
		w, _ := nodesettings.ObjectOf(map[string]any{"item": "milk", "walmart_item_id": " 10450114 "})
		_ = st.SaveData("walmart_items", "milk", w, nil)
	})
	seven := int64(7)
	compare := func(label string, args map[string]any, req node.Request) {
		t.Helper()
		pyOut, _ := sc.Call(ctx, "export_shopping_list", args, req)
		pyPosted := inbox.take()
		goOut, _ := port.Call(ctx, "export_shopping_list", args, req)
		goPosted := inbox.take()
		p, pw := normal(t, pyOut)
		g, gw := normal(t, goOut)
		if !reflect.DeepEqual(p, g) || pw != gw {
			t.Errorf("%s:\n python %v\n go     %v", label, p, g)
		}
		if a, b := asJSON(pyPosted), asJSON(goPosted); a != b {
			t.Errorf("%s posted:\n python %s\n go     %s", label, a, b)
		}
		if (len(goPosted) == 1) != (req.UserID != nil) || (req.UserID != nil && !strings.Contains(asJSON(goPosted), "checkbox_stepper")) {
			t.Errorf("%s: posted %s", label, asJSON(goPosted))
		}
	}
	compare("unknown speaker", nil, node.Request{})
	compare("walmart", nil, node.Request{UserID: &seven, VoiceCommand: "send my shopping list"})
	if a, b := storedRecords(t, pySt, "walmart_items"), storedRecords(t, goSt, "walmart_items"); a != b || !strings.Contains(a, "eggs") {
		t.Errorf("seeded walmart_items:\n python %s\n go     %s", a, b)
	}
	compare("notes by voice", map[string]any{"provider": "walmart"}, node.Request{UserID: &seven, VoiceCommand: "send my list to my notes"})
	both(func(st *nodesettings.Store) {
		_ = st.SetSecret("EXPORT_PROVIDER", " Notes ", "integration", "string", nil)
	})
	compare("notes by setting", nil, node.Request{UserID: &seven, VoiceCommand: "send it"})

	cb := func(label string, data map[string]any) {
		t.Helper()
		params := map[string]any{"command_name": "export_shopping_list", "callback_name": "export_selected", "data": data,
			"user_id": 7, "conversation_id": "c", "voice_command": "cb", "enabled": true}
		pyRes, _ := sc.Callback(params)
		goRes, _ := port.Callback(params)
		if a, b := asJSON(pyRes), asJSON(goRes); a != b {
			t.Errorf("%s:\n python %s\n go     %s", label, a, b)
		}
		if a, b := storedRecords(t, pySt, "shopping_list"), storedRecords(t, goSt, "shopping_list"); a != b {
			t.Errorf("%s records:\n python %s\n go     %s", label, a, b)
		}
	}
	cb("nothing selected", map[string]any{"selected": []any{map[string]any{"key": ""}}, "context": map[string]any{"provider": "walmart"}})
	cb("no walmart ids", map[string]any{"selected": []any{map[string]any{"key": "eggs"}}, "context": map[string]any{"provider": "walmart"}})
	cb("walmart", map[string]any{"selected": []any{map[string]any{"key": "milk", "quantity": 3}, map[string]any{"key": "eggs"}},
		"context": map[string]any{"provider": "walmart"}})
	cb("notes", map[string]any{"selected": []any{map[string]any{"key": "coffee", "quantity": "x"}, map[string]any{"key": "gone", "quantity": 120}},
		"provider": "notes"})

	var records []nodesettings.Object
	for _, r := range []map[string]any{{"item": "milk", "walmart_item_id": "123"}, {"item": "eggs", "walmart_item_id": " "}, {}} {
		o, _ := nodesettings.ObjectOf(r)
		records = append(records, o)
	}
	pySum, _ := sc.Summaries(ctx, "export_shopping_list", records)
	goSum, _ := port.Summaries(ctx, "export_shopping_list", records)
	if a, b := asJSON(pySum), asJSON(goSum); a != b {
		t.Errorf("summaries:\n python %s\n go     %s", a, b)
	}
	var pySecrets []nodesettings.SecretSpec
	for _, c := range sc.Commands() {
		if c.Name == "export_shopping_list" {
			pySecrets = c.Secrets
		}
	}
	if a, b := asJSON(pySecrets), asJSON(port.Secrets("export_shopping_list")); a != b {
		t.Errorf("secrets:\n python %s\n go     %s", a, b)
	}
	checkFastPaths(t, py, map[string]string{"export_shopping_list_command": "ExportShoppingListCommand"}, []string{
		"send my shopping list", "export the grocery list to walmart", "send our groceries list to the store.",
		"send my shopping list to my notes", "send my shopping list to target", "export my contacts", "send the shopping list to notes?",
	}, builtins.ExportShoppingMatch)
}

// reminderNoise blanks what differs between two runs of the same reminder calls: the random
// ids and the stamps read from the clock mid-call (microseconds). A due time computed from a
// date or a clock time is whole seconds and still compared. Python 3.14's datetime adds
// ", not N" to a range error that 3.12 (CI's, the Pi's) doesn't.
var reminderNoise = regexp.MustCompile(`rem_[0-9a-f]{8}|"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}\+00:00"`)

var py314Range = regexp.MustCompile(`(must be in \d+\.\.\d+), not -?\d+`)

func reminderJSON(v any) string {
	return py314Range.ReplaceAllString(reminderNoise.ReplaceAllString(asJSON(v), "X"), "$1")
}

// reminderRecords is the set_reminder records, noise blanked.
func reminderRecords(st *nodesettings.Store) string {
	var out []string
	for _, r := range st.Records("set_reminder", time.Now()) {
		out = append(out, reminderJSON(r.Data))
	}
	return strings.Join(out, "\n")
}

func TestReminderMatchesPython(t *testing.T) {
	py := nodeCommandsPython(t)
	// A zone without DST, the same for both: Python places dates at today's offset, which the
	// port deliberately doesn't (Reminder's doc); across a DST change they would differ.
	t.Setenv("TZ", "Asia/Kolkata")
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	// Seeded reminders, due or not: one-shot, daily, weekdays (a Friday), monthly (the 31st),
	// snoozed, long fired (cleaned up), in the future, another speaker's. Times whose UTC and
	// Kolkata weekdays agree (Python advances in UTC, the port on the local clock).
	seed := func(st *nodesettings.Store) {
		for _, r := range []map[string]any{
			{"reminder_id": "rem_00000001", "text": "water the plants", "due_at": "2026-10-01T03:00:00+00:00", "recurrence": nil, "user_id": 7},
			{"reminder_id": "rem_00000002", "text": "take my meds", "due_at": "2026-10-20T02:30:00+00:00", "recurrence": "daily", "user_id": 7},
			{"reminder_id": "rem_00000003", "text": "standup", "due_at": "2026-10-23T03:30:00+00:00", "recurrence": "weekdays", "user_id": 7},
			{"reminder_id": "rem_00000004", "text": "pay rent", "due_at": "2026-01-31T03:30:00+00:00", "recurrence": "monthly", "user_id": 7},
			{"reminder_id": "rem_00000005", "text": "call the bank", "due_at": "2026-10-01T03:00:00+00:00", "snooze_until": "2099-01-01T00:00:00+00:00", "user_id": 7},
			{"reminder_id": "rem_00000006", "text": "old one", "due_at": "2026-09-01T03:00:00+00:00", "announced": true, "announce_count": 1,
				"last_announced_at": "2026-09-01T03:00:00+00:00", "user_id": 7},
			{"reminder_id": "rem_00000007", "text": "dentist", "due_at": "2099-03-01T04:30:00+00:00", "user_id": 7},
			{"reminder_id": "rem_00000008", "text": "feed the cat", "due_at": "2026-10-02T03:00:00+00:00", "user_id": 9},
		} {
			for k, v := range map[string]any{"created_at": "2026-09-01T00:00:00+00:00", "recurrence": nil, "announced": false,
				"snooze_until": nil, "announce_count": 0, "last_announced_at": nil} {
				if _, ok := r[k]; !ok {
					r[k] = v
				}
			}
			o, _ := nodesettings.ObjectOf(r)
			_ = st.SaveData("set_reminder", r["reminder_id"].(string), o, nil)
		}
	}
	sc, pySt := pythonBuiltinsStore(t, py, &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(t.TempDir(), "t.json")}, Alerts: &sdkhost.Alerts{}}, seed)
	goSt, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	seed(goSt)
	port := &builtins.Reminder{Store: goSt, Loc: loc}
	ctx := context.Background()

	// The poller's check: what fires, and the records after (twice: the daily is still behind).
	for i := range 2 {
		pyDue, pyPending, err := sc.RemindersDue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		goDue, goPending, _ := port.Due(ctx)
		var want []reminders.Due
		for _, d := range pyDue {
			want = append(want, reminders.Due(d))
		}
		if a, b := asJSON(want), asJSON(goDue); a != b || pyPending != goPending {
			t.Errorf("due %d:\n python %s (%d pending)\n go     %s (%d pending)", i, a, pyPending, b, goPending)
		}
		if a, b := reminderRecords(pySt), reminderRecords(goSt); a != b {
			t.Errorf("records after due %d:\n python %s\n go     %s", i, a, b)
		}
	}

	seven, nine := int64(7), int64(9)
	call := func(args map[string]any, user *int64) {
		t.Helper()
		req := node.Request{UserID: user, VoiceCommand: "x"}
		pyOut, _ := sc.Call(ctx, "reminder", args, req)
		goOut, _ := port.Call(ctx, "reminder", args, req)
		p, pw := normal(t, pyOut)
		g, gw := normal(t, goOut)
		if a, b := reminderJSON(p), reminderJSON(g); a != b || pw != gw {
			t.Errorf("%v (user %v):\n python %s (wait %v)\n go     %s (wait %v)", args, user, a, pw, b, gw)
		}
	}
	for _, a := range []map[string]any{
		{"action": "list"}, {"action": "list", "filter": "recurring"}, {"action": "list", "filter": "today"},
		{"action": "set"}, {"action": "set", "text": "call mom"}, {"action": "set", "text": "call mom", "recurrence": "daily"},
		{"action": "set", "text": "check the laundry", "relative_minutes": 30}, {"action": "set", "text": "call mom", "time": "15:00"},
		{"action": "set", "text": "pick up the package", "resolved_datetimes": []any{"tomorrow"}, "time": "08:30"},
		{"action": "set", "text": "timesheet", "resolved_datetimes": []any{"next_monday"}, "time": "09:00", "recurrence": "weekly"},
		{"action": "set", "text": "trash", "resolved_datetimes": []any{"tonight"}},
		{"action": "set", "text": "water plants", "resolved_datetimes": []any{"tomorrow_morning"}},
		{"action": "set", "text": "groceries", "resolved_datetimes": []any{"next_monday", "morning"}},
		{"action": "set", "text": "hike", "resolved_datetimes": []any{"this_weekend"}, "time": "10:00"},
		{"action": "set", "text": "late", "time": "25:00"}, {"action": "set", "text": "odd", "time": "7:xx"},
		{"action": "set", "text": "noon", "time": "noon"}, {"action": "set", "text": "x", "recurrence": "hourly"},
		{"action": "list"}, {"action": "list", "filter": "recurring"}, {"action": "list", "filter": "today"},
		{"action": "delete"}, {"action": "delete", "text": "MOM"}, {"action": "delete", "text": "nothing like it"},
		{"action": "snooze"}, {"action": "snooze", "text": "laundry", "minutes": 15}, {"action": "snooze", "text": "nope"},
		{}, {"text": "buy milk"},
	} {
		call(a, &seven)
	}
	call(map[string]any{"action": "set", "text": "who am i", "time": "10:00"}, nil)
	call(map[string]any{"action": "list"}, &nine)
	call(map[string]any{"action": "list"}, nil)
	if a, b := reminderRecords(pySt), reminderRecords(goSt); a != b {
		t.Errorf("records:\n python %s\n go     %s", a, b)
	}

	// set_at, the proposable action's callback.
	for _, data := range []map[string]any{
		{"text": "leave for the airport", "due_at_iso": "2099-05-01T06:15:00-04:00", "idempotency_key": "k"},
		{"text": "naive", "due_at_iso": "2099-05-01T06:15:00"}, {"text": "", "due_at_iso": "2099-05-01T06:15:00Z"},
		{"text": "bad", "due_at_iso": "soon"},
	} {
		for _, uid := range []any{7, nil} {
			params := map[string]any{"command_name": "reminder", "callback_name": "set_at", "data": data, "user_id": uid,
				"conversation_id": "c", "voice_command": "cb", "enabled": true}
			pyRes, _ := sc.Callback(params)
			goRes, _ := port.Callback(params)
			if a, b := reminderJSON(pyRes), reminderJSON(goRes); a != b {
				t.Errorf("set_at %v (user %v):\n python %s\n go     %s", data, uid, a, b)
			}
		}
	}
	if a, b := reminderRecords(pySt), reminderRecords(goSt); a != b {
		t.Errorf("records after set_at:\n python %s\n go     %s", a, b)
	}
	call(map[string]any{"action": "delete", "scope": "all"}, &nine)
	call(map[string]any{"action": "delete", "scope": "all"}, &seven)
	call(map[string]any{"action": "delete", "scope": "all"}, &seven)

	var records []nodesettings.Object
	for _, r := range []map[string]any{
		{"text": "call mom", "due_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, {"text": "meds", "due_at": "2099-01-01T08:00:00+00:00", "recurrence": "daily"},
		{"recurrence": "weekly"}, {"text": "", "due_at": "not a date"}, {},
	} {
		o, _ := nodesettings.ObjectOf(r)
		records = append(records, o)
	}
	pySum, _ := sc.Summaries(ctx, "reminder", records)
	goSum, _ := port.Summaries(ctx, "reminder", records)
	if a, b := asJSON(pySum), asJSON(goSum); a != b {
		t.Errorf("summaries:\n python %s\n go     %s", a, b)
	}
	want := asJSON(sc.DataCommands())
	for _, d := range port.DataCommands() {
		if !strings.Contains(want, asJSON(d)) {
			t.Errorf("browser settings %s not among python's %s", asJSON(d), want)
		}
	}
	var pySecrets []nodesettings.SecretSpec
	for _, c := range sc.Commands() {
		if c.Name == "reminder" {
			pySecrets = c.Secrets
		}
	}
	if a, b := asJSON(pySecrets), asJSON(port.Secrets("reminder")); a != b {
		t.Errorf("secrets:\n python %s\n go     %s", a, b)
	}
	checkFastPaths(t, py, map[string]string{"reminder_command": "ReminderCommand"}, []string{
		"what reminders do I have?", "list my reminders", "show me my reminders", "do I have any upcoming reminders",
		"any reminders for today", "reminders today", "do i have reminders today?", "show me my recurring reminders",
		"recurring reminders", "delete all my reminders", "clear all of my reminders.", "snooze", "snooze that",
		"snooze the reminder", "snooze for 15 minutes", "snooze it for 2 hours", "snooze for 1 hr", "snooze the mom reminder for 5 minutes",
		"remind me to call mom at 3", "any reminders", "any reminder for",
	}, builtins.ReminderMatch)
}
