package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// attachHost answers inbox.post, reading each attachment's file while the child waits (it is
// gone once the post returns).
type attachHost struct {
	mu    sync.Mutex
	posts []map[string]any
	files map[string][]byte
}

func (h *attachHost) Answer(_ context.Context, method string, raw json.RawMessage) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	h.posts = append(h.posts, p)
	atts, _ := p["attachments"].([]any)
	for _, a := range atts {
		a := a.(map[string]any)
		b, err := os.ReadFile(a["path"].(string))
		if err != nil {
			return nil, err
		}
		if h.files == nil {
			h.files = map[string][]byte{}
		}
		h.files[a["filename"].(string)] = b
	}
	return "ok", nil
}

// startAgents starts a Pi sidecar over testdata/packages/watcher (agents ticker and flaky) and
// badagent (an agent.py without an agent).
func startAgents(t *testing.T, h Host) (*Sidecar, *nodesettings.Store) {
	t.Helper()
	py := python(t)
	abs := func(n string) string {
		p, err := filepath.Abs(filepath.Join("testdata", "packages", n))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{
		map[string]any{"name": "watcher", "root": abs("watcher"),
			"agents": []string{"agents/ticker/agent.py", "agents/flaky/agent.py"}},
		map[string]any{"name": "badagent", "root": abs("badagent"), "agents": []string{"agents/bad/agent.py"}},
	}})
	listing := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := StartWith(context.Background(), py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Storage: st, Host: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return sc, st
}

// The user's QA (2026-10-23): comfyui's comfyui_jobs agent never ran on the Go node. A Pi
// sidecar loads a package's agents and describes their schedules; agent_run runs one, which
// reaches storage and posts an inbox item with its attachment through the host, and returns
// its alerts. A failing run is an error; an agent.py without an agent fails its package.
func TestSidecarRunsPackageAgents(t *testing.T) {
	h := &attachHost{}
	sc, st := startAgents(t, h)
	ctx := context.Background()

	want := []AgentDef{
		{Name: "flaky", Package: "watcher", Interval: 300 * time.Second},
		{Name: "ticker", Package: "watcher", Interval: 20 * time.Second, RunOnStartup: true},
	}
	if got := sc.Agents(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("agents = %+v", got)
	}
	if f := sc.Failed(); len(f) != 1 || !strings.Contains(f["badagent"], "no IJarvisAgent") {
		t.Fatalf("failed = %v", f)
	}

	job := nodesettings.Object{}
	job.Set("request", json.RawMessage(`"a fox running"`))
	if err := st.SaveData("ticker_jobs", "job1", job, nil); err != nil {
		t.Fatal(err)
	}
	alerts, err := sc.RunAgent(ctx, "ticker")
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		SourceAgent string `json:"source_agent"`
		Title       string `json:"title"`
		Summary     string `json:"summary"`
		DedupeKey   string `json:"dedupe_key"`
		ExpiresAt   string `json:"expires_at"`
	}
	if len(alerts) != 1 || json.Unmarshal(alerts[0], &a) != nil || a.SourceAgent != "ticker" || a.Title != "run 1" ||
		a.Summary != "ok" || a.DedupeKey != "ticker:1" || a.ExpiresAt == "" {
		t.Fatalf("alerts = %s", alerts)
	}
	if len(h.posts) != 1 {
		t.Fatalf("posts = %v", h.posts)
	}
	p := h.posts[0]
	atts, _ := p["attachments"].([]any)
	if p["title"] != "Your picture is ready" || p["summary"] != "a fox running" || p["user_id"] != 7.0 ||
		p["target_type"] != "user" || p["create_push_notification"] != true || len(atts) != 1 ||
		atts[0].(map[string]any)["mime"] != "image/png" || !strings.HasPrefix(string(h.files["dot.png"]), "\x89PNG") {
		t.Fatalf("post = %v, files %v", p, h.files)
	}
	if _, err := os.Stat(atts[0].(map[string]any)["path"].(string)); !os.IsNotExist(err) {
		t.Fatalf("the attachment's temp file is left: %v", err)
	}

	if _, err := sc.RunAgent(ctx, "flaky"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("flaky = %v", err)
	}
	if _, err := sc.RunAgent(ctx, "nope"); err == nil {
		t.Fatal("ran an agent that isn't loaded")
	}
	// The sidecar still serves after a failed run.
	if alerts, err := sc.RunAgent(ctx, "ticker"); err != nil || len(alerts) != 1 {
		t.Fatalf("second run = %v, %v", alerts, err)
	}
}
