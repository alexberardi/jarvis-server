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
