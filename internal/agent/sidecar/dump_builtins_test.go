package sidecar_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// TestDumpPythonBuiltins writes each of the pinned jarvis-node-setup built-ins' tool definitions
// (client_tools and available_commands entries, as the sidecar renders them) to
// internal/agent/builtins/defs, which the Go ports embed as their own definitions, so
// command-center sees exactly what the Python command offered. It loads the
// built-ins as the Pi first did (only control_device, routine and send_link skipped), so a
// command already ported to Go still gets its Python fixture. Run it after moving
// packages.NodeCommandsRequirement:
//
//	JARVIS_DUMP_BUILTINS=1 JARVIS_SIDECAR_PYTHON=<venv python> go test -run TestDumpPythonBuiltins ./internal/agent/sidecar
func TestDumpPythonBuiltins(t *testing.T) {
	if os.Getenv("JARVIS_DUMP_BUILTINS") == "" {
		t.Skip("set JARVIS_DUMP_BUILTINS=1 to rewrite the Python built-ins' fixtures")
	}
	py := nodeCommandsPython(t)
	dir := t.TempDir()
	st, err := nodesettings.OpenStore(dir, "n1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{}, "builtins": map[string]any{"package": packages.PiBuiltins("").Package,
		"skip": []string{"control_device_command", "routine_command", "send_link_command"}}})
	listing := filepath.Join(dir, "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := sidecar.StartWith(context.Background(), py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), sidecar.Options{Storage: st, Host: &sdkhost.Host{
		Timers: &timers.Service{Path: filepath.Join(dir, "timers.json")}, Alerts: &sdkhost.Alerts{},
		Audio: &sdkhost.Audio{Path: filepath.Join(dir, "volume.json")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	out := filepath.Join("..", "builtins", "defs")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range sc.Available(context.Background()) {
		b, err := json.MarshalIndent(map[string]any{"client_tool": d.ClientTool, "command": d.Command}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, d.Name+".json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		n++
	}
	// The data browser's settings for the commands that keep records.
	for _, d := range sc.DataCommands() {
		var fields []any
		_ = json.Unmarshal(d.Fields, &fields)
		if d.Mode == "enabled" && d.StorageName == d.Name && len(fields) == 0 && !d.SupportsCreate {
			continue // the default: a command that keeps no records
		}
		b, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, d.Name+".browser.json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		n++
	}
	t.Logf("wrote %d fixtures to %s", n, out)
}
