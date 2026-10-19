package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
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
	sc, err := Start(ctx, py, "testdata/commands", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
	sc, err := Start(ctx, py, "testdata/commands", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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

// A package reads the settings the household set from the mobile app through JarvisStorage, as
// on a Pi: the integration secret for everyone, a user secret for the requesting user, and its
// command data, all from the agent's store (DESIGN §16).
func TestSidecarStorageReachesTheAgentsSettings(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	seven, eight := int64(7), int64(8)
	_ = st.SetSecret("TEST_API_KEY", "abc123", "integration", "", nil)
	_ = st.SetSecret("NICKNAME", "Ace", "user", "", &seven)
	sc, err := Start(ctx, py, "testdata/storage", slog.New(slog.NewTextHandler(io.Discard, nil)), st)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	cmds := sc.Commands()
	if len(cmds) != 1 || cmds[0].Name != "secretive" || len(cmds[0].Secrets) != 2 {
		t.Fatalf("commands = %+v", cmds)
	}
	if s := cmds[0].Secrets[1]; s.Key != "NICKNAME" || s.Scope != "user" || s.IsSensitive || s.FriendlyName != "Nickname" {
		t.Fatalf("secret spec = %+v", s)
	}
	out, ok := sc.Call(ctx, "secretive", map[string]any{"note": "hello"}, node.Request{UserID: &seven})
	node.TakeSignals(out)
	if !ok || out["api_key"] != "abc123" || out["nickname"] != "Ace" || out["last"] != "hello" {
		t.Fatalf("user 7: %v", out)
	}
	if d, _ := st.Data("secretive", "last", time.Now()); d.Value("note") != "hello" {
		t.Fatalf("command data = %v", d)
	}
	// Another user has no nickname; the user is reset after each call.
	out, _ = sc.Call(ctx, "secretive", nil, node.Request{UserID: &eight})
	if out["api_key"] != "abc123" || out["nickname"] != nil {
		t.Fatalf("user 8: %v", out)
	}
	out, _ = sc.Call(ctx, "secretive", nil, node.Request{})
	if out["nickname"] != nil {
		t.Fatalf("no user: %v", out)
	}
}

// describe carries each command's data-browser settings (a command whose settings fail stays
// out), and get_all hands a command each record's key and expiry, as the Pi's repository does.
func TestSidecarDataBrowserSettingsAndGetAll(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	var rec nodesettings.Object
	_ = json.Unmarshal([]byte(`{"zeta":"z","alpha":"a"}`), &rec)
	_ = st.SaveData("journal", "j1", rec, &exp)
	_ = st.SaveData("journal", "j0", rec, nil)
	sc, err := Start(ctx, py, "testdata/browser", slog.New(slog.NewTextHandler(io.Discard, nil)), st)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	got := map[string]nodesettings.DataCommand{}
	for _, c := range sc.DataCommands() {
		got[c.Name] = c
	}
	if a := got["alarm"]; len(got) != 5 || a.Mode != "readonly" || a.StorageName != "alarm_store" || a.SupportsCreate ||
		string(a.Fields) != `[{"name": "at", "type": "datetime"}]` || got["vault"].Mode != "disabled" || !got["notes"].SupportsCreate {
		t.Fatalf("data commands = %+v", got)
	}
	out, _ := sc.Call(ctx, "journal", nil, node.Request{})
	b, _ := json.Marshal(out["rows"])
	if string(b) != `[["j1","2099-01-02T03:04:05+00:00",["zeta","alpha","_data_key","_expires_at"]],["j0",null,["zeta","alpha","_data_key","_expires_at"]]]` {
		t.Fatalf("get_all = %s", b)
	}
	// The store's copy is untouched by what get_all added.
	if d, _ := st.Data("journal", "j1", time.Now()); d.Len() != 2 {
		t.Fatalf("stored record = %v", d.Keys())
	}
}

// takeoverHost records the takeover hook's calls.
type takeoverHost struct{ calls []string }

func (h *takeoverHost) Answer(context.Context, string, json.RawMessage) (any, error) { return nil, nil }
func (h *takeoverHost) BeforeCall(_ context.Context, command, declared string, args map[string]any) {
	h.calls = append(h.calls, command+"|"+declared+"|"+fmt.Sprint(args["action"]))
}

// N:utils/command_execution_service.py _maybe_take_over_music: the hook sees each call before
// it runs, with the command's declared player.
func TestSidecarTakeoverHook(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	h := &takeoverHost{}
	sc, err := StartWith(ctx, py, "testdata/commands", slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Host: h})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	marker := filepath.Join(t.TempDir(), "played")
	if out, _ := sc.Call(ctx, "deferred", map[string]any{"marker": marker, "action": "play"}, node.Request{}); out["success"] != true {
		t.Fatalf("call = %v", out)
	}
	sc.Call(ctx, "shout", map[string]any{"text": "hi"}, node.Request{})
	sc.Call(ctx, "nope", nil, node.Request{})
	if want := []string{"deferred|mpv|play", "shout||<nil>"}; !slices.Equal(h.calls, want) {
		t.Fatalf("hook calls %q, want %q", h.calls, want)
	}
}
