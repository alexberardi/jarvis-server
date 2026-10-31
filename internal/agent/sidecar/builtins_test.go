package sidecar_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

// The Pi profile's sidecar with every built-in ported to Go: the commands package still loads
// (a Pantry package's commands share the venv) and offers none of the ported ones, which would
// shadow the agent's. The Python commands themselves run against the ports in port_parity_test.go.
func TestSidecarRunsTheNodesBuiltins(t *testing.T) {
	py := nodeCommandsPython(t)
	ctx := context.Background()
	dir := t.TempDir()
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
	h := &sdkhost.Host{Timers: &timers.Service{Path: filepath.Join(dir, "timers.json")}, Alerts: &sdkhost.Alerts{}}
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
	for _, ported := range []string{"control_device", "routine", "send_link", "set_timer", "check_timers", "cancel_timer",
		"get_current_time", "calculate", "convert_measurement", "tell_joke", "answer_question", "chat",
		"control_node", "check_alerts", "act_on_items", "shopping_list", "todo_list", "export_shopping_list", "export_todo_list",
		"reminder"} {
		if names[ported] {
			t.Errorf("%s is offered by the sidecar; the Go agent serves it", ported)
		}
	}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
