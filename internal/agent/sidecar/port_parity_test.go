package sidecar_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
