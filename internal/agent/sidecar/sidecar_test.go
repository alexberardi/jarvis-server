package sidecar

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The spike runs against a real Python with jarvis-command-sdk importable. CI installs it;
// elsewhere the test skips. JARVIS_SIDECAR_PYTHON picks the interpreter (default python3).
func python(t *testing.T) string {
	py := os.Getenv("JARVIS_SIDECAR_PYTHON")
	if py == "" {
		py = "python3"
	}
	if err := exec.Command(py, "-c", "import jarvis_command_sdk").Run(); err != nil {
		t.Skipf("%s cannot import jarvis_command_sdk: %v", py, err)
	}
	return py
}

func TestSidecarLoadsACommandAndAnswersACall(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	sc, err := Start(ctx, py, "testdata/commands", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	tools := sc.Available(ctx)
	if len(tools) != 1 || tools[0].Name != "shout" || tools[0].Command["command_name"] != "shout" {
		t.Fatalf("tools = %+v", tools)
	}
	uid := int64(7)
	out, ok := sc.Call(ctx, "shout", map[string]any{"text": "hello"}, node.Request{UserID: &uid})
	if !ok || out["success"] != true || out["message"] != "HELLO" || out["user_id"] != 7.0 {
		t.Fatalf("call = %v, %v", out, ok)
	}
	// A second call on the same warm process.
	out, _ = sc.Call(ctx, "shout", map[string]any{"text": "again"}, node.Request{})
	if out["message"] != "AGAIN" {
		t.Fatalf("second call = %v", out)
	}
	// A bad call is an output, and the process survives it.
	out, _ = sc.Call(ctx, "shout", map[string]any{}, node.Request{})
	if out["success"] != false {
		t.Fatalf("missing arg = %v", out)
	}
	if _, ok := sc.Call(ctx, "nope", nil, node.Request{}); ok {
		t.Fatal("unknown tool claimed")
	}
}
