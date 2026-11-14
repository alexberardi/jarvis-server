package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// daemonHost answers a daemon's cc.post calls. The slow one is answered only once the quick
// one has arrived, so the test passes only if the daemon's requests overlap.
type daemonHost struct {
	quick chan struct{}
	paths chan string
}

func (h *daemonHost) Answer(_ context.Context, method string, raw json.RawMessage) (any, error) {
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &p)
	switch p.Path {
	case "/api/v0/node/slow":
		<-h.quick
	case "/api/v0/node/quick":
		close(h.quick)
	}
	h.paths <- method + " " + p.Path
	return map[string]any{"ok": true}, nil
}

func startTestDaemon(t *testing.T, name string, h Host) (*Daemon, *syncWriter) {
	t.Helper()
	py := python(t)
	root, err := filepath.Abs("testdata/daemons")
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncWriter{w: &strings.Builder{}}
	d, err := StartDaemon(py, root, "bridge", "daemons/"+name+"/daemon.py", slog.New(slog.NewTextHandler(logs, nil)), Options{Host: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	return d, logs
}

// A daemon stays up, reaches the node's backends with overlapping requests, and ends cleanly
// when the agent stops it.
func TestDaemonStaysUpAndStopsWhenTold(t *testing.T) {
	h := &daemonHost{quick: make(chan struct{}), paths: make(chan string, 4)}
	d, _ := startTestDaemon(t, "steady", h)
	got := []string{<-h.paths, <-h.paths, <-h.paths}
	if got[2] != "cc.post /api/v0/node/up" {
		t.Fatalf("calls = %v", got)
	}
	select {
	case <-d.Done():
		t.Fatalf("ended on its own: %v", d.Err())
	default:
	}
	d.Stop()
	if err := d.Err(); err != nil {
		t.Fatalf("a daemon told to stop ended with %v", err)
	}
}

// A daemon whose run() raises or returns has died: the process ends with an error, and why is
// in the agent's log.
func TestDaemonThatDiesEndsWithAnError(t *testing.T) {
	for name, want := range map[string]string{"crashy": "RuntimeError: no token", "returns": "run() returned"} {
		d, logs := startTestDaemon(t, name, nil)
		<-d.Done()
		if d.Err() == nil {
			t.Errorf("%s: ended without an error", name)
		}
		if !strings.Contains(logs.String(), want) {
			t.Errorf("%s: log lacks %q:\n%s", name, want, logs.String())
		}
	}
}

// A daemon that isn't there (or isn't one) ends with an error too.
func TestDaemonThatDoesNotLoad(t *testing.T) {
	py := python(t)
	root, _ := filepath.Abs("testdata/daemons")
	d, err := StartDaemon(py, root, "bridge", "daemons/missing/daemon.py", slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	<-d.Done()
	if d.Err() == nil {
		t.Fatal("ended without an error")
	}
}

// The node's commands sidecar never runs a daemon, but it describes one: the app's settings
// screen lists it with the settings it needs (a bot's token), where an agent's are.
func TestTheCommandsSidecarDescribesADaemonsSettings(t *testing.T) {
	py := python(t)
	root, _ := filepath.Abs("testdata/daemons")
	b, _ := json.Marshal(map[string]any{"packages": []any{map[string]any{"name": "bridge", "root": root,
		"daemons": []string{"daemons/steady/daemon.py"}}}})
	listing := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := Start(context.Background(), py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if f := sc.Failed(); len(f) != 0 {
		t.Fatalf("failed = %v", f)
	}
	agents := sc.Devices().Agents
	if len(agents) != 1 || agents[0].Entry["agent_name"] != "steady" || agents[0].Entry["daemon"] != true ||
		len(agents[0].Secrets) != 1 || agents[0].Secrets[0].Key != "BRIDGE_TOKEN" {
		t.Fatalf("agents = %+v", agents)
	}
}
