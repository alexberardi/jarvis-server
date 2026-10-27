package sidecar_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	. "github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// nodeCommandsPython is an interpreter (JARVIS_SIDECAR_PYTHON, default python3) with
// jarvis-node-setup's commands installed (CI pins it to packages.NodeCommandsRequirement's
// commit); the test skips without one. An external test package: packages imports sidecar.
func nodeCommandsPython(t *testing.T) string {
	py := os.Getenv("JARVIS_SIDECAR_PYTHON")
	if py == "" {
		py = "python3"
	}
	if err := exec.Command(py, "-c", "import commands.timer_command, jarvis_command_sdk.timer").Run(); err != nil {
		t.Skipf("%s has no jarvis-node-setup commands (pip install %q): %v", py, packages.NodeCommandsRequirement, err)
	}
	return py
}

// The Python node's own built-in commands, unchanged, on the Go Pi: loaded from the venv as
// the Pi profile lists them, each family reaching the agent's real backends (the timers, the
// alert queue, the volume, command-center's LLM, the settings store).
func TestSidecarRunsTheNodesBuiltins(t *testing.T) {
	py := nodeCommandsPython(t)
	ctx := context.Background()
	dir := t.TempDir()

	var mu sync.Mutex
	var mixer []string
	var pushes []string
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/node/push-notification" {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			pushes = append(pushes, string(b))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":"You have one storm warning."}`))
	}))
	defer llm.Close()
	h := &sdkhost.Host{
		Timers: &timers.Service{Path: filepath.Join(dir, "timers.json")},
		Alerts: &sdkhost.Alerts{},
		Audio: &sdkhost.Audio{Path: filepath.Join(dir, "volume.json"), Run: func(_ context.Context, name string, args ...string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			mixer = append(mixer, name+" "+strings.Join(args, " "))
			return "Volume: front-left: 32768 /  50% / -18.00 dB", nil
		}},
	}
	h.SetCC(node.NewCC(llm.URL, node.Creds{NodeID: "n1", NodeKey: "k"}))
	h.Timers.Start()
	defer h.Timers.Close()
	st, err := nodesettings.OpenStore(dir, "n1")
	if err != nil {
		t.Fatal(err)
	}
	bi := packages.PiBuiltins("")
	b, _ := json.Marshal(map[string]any{"packages": []any{}, "builtins": map[string]any{"package": bi.Package, "skip": bi.Skip}})
	listing := filepath.Join(dir, "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := StartWith(ctx, py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Storage: st, Host: h})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if f := sc.Failed(); len(f) != 0 {
		t.Fatalf("built-ins that didn't load: %v", f)
	}
	names := map[string]bool{}
	for _, tl := range sc.Available(ctx) {
		names[tl.Name] = true
	}
	for _, want := range []string{"calculate", "check_alerts", "shopping_list",
		"todo_list", "control_node", "tell_joke", "answer_question", "convert_measurement", "reminder"} {
		if !names[want] {
			t.Errorf("no %s among %v", want, names)
		}
	}
	// The Go agent serves these itself (packages.PortedToGo); a Python copy would shadow it.
	for _, skipped := range []string{"control_device", "routine", "send_link", "set_timer", "check_timers", "cancel_timer", "get_current_time"} {
		if names[skipped] {
			t.Errorf("%s is offered by the sidecar; the Go agent serves it", skipped)
		}
	}

	uid := int64(7)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out, ok := sc.Call(ctx, name, args, node.Request{UserID: &uid, VoiceCommand: name})
		node.TakeSignals(out)
		if !ok {
			t.Fatalf("%s not claimed", name)
		}
		t.Logf("%s %s -> %s", name, asJSON(args), asJSON(out))
		return out
	}

	if out := call("calculate", map[string]any{"num1": 6, "num2": 7, "operation": "multiply"}); out["success"] != true || out["result"] != 42.0 {
		t.Errorf("calculate = %v", out)
	}

	// The timer commands are Go now (TestTimersMatchPython runs the Python ones against them).

	// The volume, through the Go mixer.
	if out := call("control_node", map[string]any{"action": "volume_up"}); out["success"] != true || out["volume_percent"] == nil {
		t.Errorf("volume_up = %v", out)
	}
	if out := call("control_node", map[string]any{"action": "mute"}); out["success"] != true {
		t.Errorf("mute = %v", out)
	}
	mu.Lock()
	m := strings.Join(mixer, "\n")
	mu.Unlock()
	if !strings.Contains(m, "pactl set-sink-volume @DEFAULT_SINK@") || !strings.Contains(m, "pactl set-sink-mute @DEFAULT_SINK@ 1") {
		t.Errorf("mixer calls:\n%s", m)
	}

	// What's up: the Go alert queue, composed by command-center's LLM.
	now := time.Now()
	h.Alerts.Add(sdkhost.Alert{ID: "a1", SourceAgent: "weather", Title: "Storm warning", Summary: "Storm tonight", Priority: 3,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), Category: "weather"})
	if out := call("check_alerts", nil); out["message"] != "You have one storm warning." || h.Alerts.Count() != 0 {
		t.Errorf("check_alerts = %v, %d left", out, h.Alerts.Count())
	}

	// The shopping list, in the agent's settings store.
	if out := call("shopping_list", map[string]any{"action": "add", "items": []any{"milk"}}); out["success"] != true {
		t.Errorf("shopping add = %v", out)
	}
	if out := call("shopping_list", map[string]any{"action": "read"}); !strings.Contains(asJSON(out), "milk") {
		t.Errorf("shopping read = %v", out)
	}
	if out := call("tell_joke", nil); out["success"] != true {
		t.Errorf("tell_joke = %v", out)
	}

	// Reminders: set by the command into the agent's store, nothing due yet.
	out := call("reminder", map[string]any{"action": "set", "text": "check the oven", "relative_minutes": 30})
	if out["success"] != true {
		t.Fatalf("reminder set = %v", out)
	}
	due, pending, err := sc.RemindersDue(ctx)
	if err != nil || len(due) != 0 || pending != 1 {
		t.Fatalf("RemindersDue before it is due = %v, %d, %v", due, pending, err)
	}
	// Half an hour on: the record's time moved back in the store, read by a new sidecar (as
	// after a restart or a package install), which restores it and fires it once.
	recs := st.Records("set_reminder", time.Now())
	if len(recs) != 1 {
		t.Fatalf("stored reminders: %v", recs)
	}
	rec := recs[0].Data
	rec.Set("due_at", json.RawMessage(`"`+time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)+`"`))
	if err := st.SaveData("set_reminder", recs[0].Key, rec, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSecret("REMINDER_PUSH_NOTIFICATIONS", "true", "integration", "string", nil); err != nil {
		t.Fatal(err)
	}
	sc.Close()
	sc2, err := StartWith(ctx, py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Storage: st, Host: h})
	if err != nil {
		t.Fatal(err)
	}
	defer sc2.Close()
	due, pending, err = sc2.RemindersDue(ctx)
	if err != nil || len(due) != 1 || due[0].Text != "check the oven" || due[0].ID != recs[0].Key || pending != 1 ||
		due[0].UserID == nil || *due[0].UserID != uid {
		t.Fatalf("RemindersDue once due = %+v, %d, %v", due, pending, err)
	}
	if again, pending, err := sc2.RemindersDue(ctx); err != nil || len(again) != 0 || pending != 0 {
		t.Errorf("RemindersDue again = %v, %d, %v; want it said once", again, pending, err)
	}
	if d, _ := st.Data("set_reminder", recs[0].Key, time.Now()); d.Value("announced") != true {
		t.Errorf("stored after firing: %v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pushes) != 1 || !strings.Contains(pushes[0], `"body":"check the oven"`) || !strings.Contains(pushes[0], `"target_type":"user"`) {
		t.Errorf("pushes: %v", pushes)
	}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
