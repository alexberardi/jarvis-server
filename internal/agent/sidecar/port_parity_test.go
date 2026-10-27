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
	dir := t.TempDir()
	st, err := nodesettings.OpenStore(dir, "n1")
	if err != nil {
		t.Fatal(err)
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
	return sc
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
