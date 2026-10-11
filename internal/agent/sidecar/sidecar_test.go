package sidecar

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	if len(tools) != 2 || tools[0].Name != "deferred" || tools[1].Name != "shout" || tools[1].Command["command_name"] != "shout" {
		t.Fatalf("tools = %+v", tools)
	}
	uid := int64(7)
	out, ok := sc.Call(ctx, "shout", map[string]any{"text": "hello"}, node.Request{UserID: &uid})
	// The SDK's success_response defaults wait_for_input to true.
	if sig := node.TakeSignals(out); !sig.WaitForInput || sig.ClearHistory || sig.OnResponseComplete != nil {
		t.Fatalf("shout signals = %+v", sig)
	}
	if !ok || out["success"] != true || out["message"] != "HELLO" || out["user_id"] != 7.0 || len(out) != 3 {
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

func TestSidecarSignalsAndDeferredStep(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	sc, err := Start(ctx, py, "testdata/commands", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	marker := filepath.Join(t.TempDir(), "played")
	out, ok := sc.Call(ctx, "deferred", map[string]any{"marker": marker}, node.Request{})
	if !ok || out["success"] != true || out["message"] != "Playing now." {
		t.Fatalf("call = %v, %v", out, ok)
	}
	sig := node.TakeSignals(out)
	if sig.WaitForInput || !sig.ClearHistory || sig.OnResponseComplete == nil {
		t.Fatalf("signals = %+v", sig)
	}
	if _, has := out[node.SignalsKey]; has {
		t.Fatal("TakeSignals left the signals in the output")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the deferred step ran before it was asked to")
	}
	if err := sig.OnResponseComplete(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "played" {
		t.Fatalf("deferred step: %q, %v", b, err)
	}
	// One run per call.
	if err := sig.OnResponseComplete(); err == nil {
		t.Fatal("a deferred step ran twice")
	}
}
