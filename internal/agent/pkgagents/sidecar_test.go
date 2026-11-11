package pkgagents

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
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// python is an interpreter that imports jarvis_command_sdk (JARVIS_SIDECAR_PYTHON, default
// python3), as the sidecar's tests use; the test skips without one.
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

// The whole path of the user's QA (2026-10-23), with a fake command-center: the scheduler runs
// a package agent in a real sidecar on start, the agent's inbox post with a picture goes out
// as this node (the upload, then the item with its id, for the user's web and mobile inbox),
// and its alert lands in the Pi's queue.
func TestAPackageAgentPostsToTheInboxThroughTheSidecar(t *testing.T) {
	py := python(t)
	var mu sync.Mutex
	var uploads []string
	var item map[string]any
	cc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/v0/node/inbox-attachment":
			uploads = append(uploads, r.Header.Get("Content-Type")+" "+r.Header.Get("X-API-Key")+" "+string(b[:4]))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"att-1"}`))
		case "/api/v0/node/inbox-item":
			_ = json.Unmarshal(b, &item)
			_, _ = w.Write([]byte(`{"sent":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cc.Close()

	host := &sdkhost.Host{Alerts: &sdkhost.Alerts{}}
	host.SetCC(node.NewCC(cc.URL, node.Creds{NodeID: "n1", NodeKey: "k"}))
	root, err := filepath.Abs(filepath.Join("..", "sidecar", "testdata", "packages", "watcher"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{map[string]any{"name": "watcher", "root": root,
		"agents": []string{"agents/ticker/agent.py"}}}})
	listing := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sc, err := sidecar.StartWith(context.Background(), py, listing, log, sidecar.Options{Storage: st, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	live := &sidecar.Live{}
	live.Swap(sc)
	defer live.Close()

	s := &Scheduler{Sidecar: live, Alerts: host.Alerts, Log: log}
	s.Tick(context.Background())

	mu.Lock()
	defer mu.Unlock()
	ids, _ := json.Marshal(item["attachment_ids"])
	if strings.Join(uploads, ",") != "image/png n1:k \x89PNG" || string(ids) != `["att-1"]` ||
		item["title"] != "Your picture is ready" || item["user_id"] != 7.0 || item["create_push_notification"] != true {
		t.Fatalf("uploads %q, item %v", uploads, item)
	}
	if got := host.Alerts.Pending(); len(got) != 1 || got[0].Title != "run 1" || got[0].Summary != "ok" ||
		!got[0].ExpiresAt.After(time.Now()) {
		t.Fatalf("alerts = %+v", got)
	}
	if f := s.Failing(); len(f) != 0 {
		t.Fatalf("failing = %v", f)
	}
}

// A Forge test install's agents on a desktop (TestsOnly): they run at once, though crashy's
// schedule says an hour and neither is an installed package's, and each run is a test event
// for Send to Forge: ticker's with its alert, crashy's with the end of its traceback.
func TestATestInstallsAgentsRunAtOnceAsTestEvents(t *testing.T) {
	py := python(t)
	dir, err := filepath.Abs(filepath.Join("..", "sidecar", "testdata", "packages"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{
		map[string]any{"name": "test:watcher", "root": filepath.Join(dir, "watcher"), "agents": []string{"agents/ticker/agent.py"}},
		map[string]any{"name": "test:crashy", "root": filepath.Join(dir, "crashy"), "agents": []string{"agents/crashy/agent.py"}},
	}})
	listing := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// No command-center: ticker's inbox post fails inside its run, which still raises its alert.
	sc, err := sidecar.StartWith(context.Background(), py, listing, log, sidecar.Options{Storage: st, Host: &sdkhost.Host{}})
	if err != nil {
		t.Fatal(err)
	}
	live := &sidecar.Live{}
	live.Swap(sc)
	defer live.Close()

	(&Scheduler{Sidecar: live, Log: log, TestsOnly: true}).Tick(context.Background())

	events := func(pkg string) []map[string]any {
		t.Helper()
		out, err := live.DeviceCall("test_events", map[string]any{"package": pkg})
		if err != nil {
			t.Fatal(err)
		}
		var evs []map[string]any
		for _, e := range out["events"].([]any) {
			if ev := e.(map[string]any); ev["kind"] == "agent" {
				evs = append(evs, ev)
			}
		}
		return evs
	}
	ok := events("test:watcher")
	if len(ok) != 1 || ok[0]["agent"] != "ticker" || ok[0]["ok"] != true || ok[0]["alert_count"] != 1.0 {
		t.Fatalf("ticker events = %v", ok)
	}
	if al := ok[0]["alerts"].([]any); len(al) != 1 || al[0].(map[string]any)["title"] != "run 1" {
		t.Fatalf("ticker alerts = %v", al)
	}
	bad := events("test:crashy")
	if len(bad) != 1 || bad[0]["agent"] != "crashy" || bad[0]["ok"] != nil {
		t.Fatalf("crashy events = %v", bad)
	}
	if e, _ := bad[0]["error"].(string); !strings.Contains(e, "KeyError: 'state'") || !strings.Contains(e, "agent.py") || len(e) > 1500 {
		t.Fatalf("crashy error = %q", e)
	}
}
