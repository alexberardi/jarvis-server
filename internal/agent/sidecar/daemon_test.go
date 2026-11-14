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

// channelHost plays jarvisd for a chat channel's daemon: the answers the channel routes give
// (cc: channel_turns.go), and what the daemon sent to get them.
type channelHost struct {
	mu     sync.Mutex
	bodies map[string][]json.RawMessage
	report chan json.RawMessage
}

func (h *channelHost) Answer(_ context.Context, method string, raw json.RawMessage) (any, error) {
	var p struct {
		Path string          `json:"path"`
		JSON json.RawMessage `json:"json"`
	}
	_ = json.Unmarshal(raw, &p)
	if method != "cc.post" {
		return nil, nil
	}
	h.mu.Lock()
	h.bodies[p.Path] = append(h.bodies[p.Path], p.JSON)
	h.mu.Unlock()
	switch p.Path {
	case "/api/v0/node/channels/testchat/message":
		switch {
		case strings.Contains(string(p.JSON), "ext-2"):
			return map[string]any{"linked": false}, nil
		case strings.Contains(string(p.JSON), "break"):
			return map[string]any{"linked": true, "failed": "The model is not running."}, nil
		}
		return map[string]any{"linked": true, "reply": "Hello yourself."}, nil
	case "/api/v0/node/channels/testchat/command":
		return map[string]any{"text": "Linked.", "private": true}, nil
	case "/api/v0/node/channels/testchat/outbox":
		return map[string]any{"messages": []map[string]any{{"id": "r1", "external_user_id": "ext-1", "text": "Bins out"}}}, nil
	case "/api/v0/node/report":
		h.report <- p.JSON
	}
	return map[string]any{"ok": true}, nil
}

// A daemon written with the SDK's Channel reaches the channel routes through the agent's pipe
// with the bodies those routes read, and understands their answers. The host here stands in
// for jarvisd; the routes themselves are tested in cc.
func TestAChannelDaemonTalksToJarvisThroughTheAgent(t *testing.T) {
	h := &channelHost{bodies: map[string][]json.RawMessage{}, report: make(chan json.RawMessage, 1)}
	startTestDaemon(t, "channel", h)
	var got map[string]any
	if err := json.Unmarshal(<-h.report, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"reply":    []any{true, "Hello yourself.", false},
		"stranger": []any{false, ""},
		"failed":   []any{true, "The model is not running.", true},
		"command":  []any{"Linked.", true},
		"outbox":   []any{[]any{"r1", "ext-1", "Bins out"}},
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Fatalf("the daemon understood\n %s\nwant\n %s", g, w)
	}
	sent := func(route string, i int) map[string]any {
		t.Helper()
		var m map[string]any
		h.mu.Lock()
		defer h.mu.Unlock()
		if err := json.Unmarshal(h.bodies["/api/v0/node/channels/testchat/"+route][i], &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	// "cG5nLWJ5dGVz" is base64 of the photo's bytes.
	if g, w := mustJSON(t, sent("message", 0)), `{"external_user_id":"ext-1","images":[{"data":"cG5nLWJ5dGVz","mime":"image/png"}],"place":"dm:1","text":"hello"}`; g != w {
		t.Fatalf("message body = %s", g)
	}
	if g, w := mustJSON(t, sent("command", 0)), `{"args":{"code":"ABCD2345"},"external_user_id":"ext-1","name":"link","place":"dm:1"}`; g != w {
		t.Fatalf("command body = %s", g)
	}
	if g, w := mustJSON(t, sent("outbox", 0)), `{"ack":["r0"]}`; g != w {
		t.Fatalf("outbox body = %s", g)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
