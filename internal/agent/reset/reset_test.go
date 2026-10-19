package reset

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// jarvisd is command-center's two factory-reset routes (internal/modules/cc/reset.go): the
// tracked task's status, which takes only the task's token, and the legacy verify-reset.
type jarvisd struct {
	token, task string

	mu      sync.Mutex
	states  []string
	errMsg  string
	verify  int
	apiKeys []string
}

func (j *jarvisd) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v0/nodes/factory-reset/{task}/status", func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.apiKeys = append(j.apiKeys, r.Header.Get("X-API-Key"))
		if r.Header.Get("X-Reset-Token") != j.token {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"detail":"Invalid or expired reset token"}`)
			return
		}
		if r.PathValue("task") != j.task {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var b struct {
			State        string `json:"state"`
			ErrorMessage string `json:"error_message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		j.states = append(j.states, b.State)
		j.errMsg = b.ErrorMessage
		io.WriteString(w, `{"task_id":"`+j.task+`","state":"`+b.State+`"}`)
	})
	mux.HandleFunc("POST /api/v0/nodes/verify-reset", func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.verify++
		var b struct {
			NodeID    string `json:"node_id"`
			RequestID string `json:"request_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b.RequestID != j.token || b.NodeID != "n1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"verified":true}`)
	})
	return mux
}

// stateDir is a node's state dir with every file a reset clears and the ones it keeps.
func stateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"node.json", "k2.json", "node-settings.json", "timers.json", "bluetooth.json",
		"packages/pkgs/weather/jarvis_package.yaml", "packages/pkgs/weather.previous/jarvis_package.yaml",
		"packages/commands.json",
		// kept
		"agent.json", "volume.json", "update.json", "packages/venv/bin/python", "packages/builtins.txt"} {
		p = filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func exists(dir, p string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p)))
	return err == nil
}

func assertWiped(t *testing.T, dir string) {
	t.Helper()
	for _, p := range []string{"node.json", "k2.json", "node-settings.json", "timers.json", "bluetooth.json", "packages/pkgs", "packages/commands.json"} {
		if exists(dir, p) {
			t.Errorf("%s survived the reset", p)
		}
	}
	for _, p := range []string{"agent.json", "volume.json", "update.json", "packages/venv/bin/python", "packages/builtins.txt"} {
		if !exists(dir, p) {
			t.Errorf("%s was removed; a reset keeps it", p)
		}
	}
}

func assertUntouched(t *testing.T, dir string) {
	t.Helper()
	for _, p := range []string{"node.json", "k2.json", "node-settings.json", "packages/pkgs/weather"} {
		if !exists(dir, p) {
			t.Errorf("%s was removed, but nothing should have been wiped", p)
		}
	}
}

type rig struct {
	j       *jarvisd
	cc      *node.CC
	h       *Handler
	dir     string
	reboots int
	exits   int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	g := &rig{j: &jarvisd{token: "tok-1", task: "task-1"}, dir: stateDir(t)}
	srv := httptest.NewServer(g.j.handler())
	t.Cleanup(srv.Close)
	g.cc = node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "secret"})
	g.h = &Handler{NodeID: "n1", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Wipe:   func(context.Context) ([]string, error) { return Wipe(g.dir) },
		Reboot: func() error { g.reboots++; return nil },
		Exit:   func() { g.exits++ }}
	return g
}

func msg(reqID, nodeID, taskID string) []byte {
	b, _ := json.Marshal(map[string]string{"request_id": reqID, "node_id": nodeID, "task_id": taskID})
	return b
}

func TestTrackedResetReportsWipesAndReboots(t *testing.T) {
	g := newRig(t)
	g.h.Handle(context.Background(), g.cc, msg("tok-1", "n1", "task-1"))
	if !slices.Equal(g.j.states, []string{"in_progress", "success"}) {
		t.Fatalf("states reported = %v, want in_progress then success", g.j.states)
	}
	assertWiped(t, g.dir)
	if g.reboots != 1 || g.exits != 0 {
		t.Fatalf("reboots %d exits %d, want one reboot", g.reboots, g.exits)
	}
	for _, k := range g.j.apiKeys {
		if k != "" {
			t.Fatalf("the status call sent the node key %q; the reset token is its only credential", k)
		}
	}
}

func TestRejectedTokenWipesNothing(t *testing.T) {
	g := newRig(t)
	g.h.Handle(context.Background(), g.cc, msg("forged", "n1", "task-1"))
	assertUntouched(t, g.dir)
	if len(g.j.states) != 0 || g.reboots != 0 || g.exits != 0 {
		t.Fatalf("states %v reboots %d exits %d after a rejected token", g.j.states, g.reboots, g.exits)
	}
}

func TestUnreachableJarvisdWipesNothing(t *testing.T) {
	g := newRig(t)
	g.cc.BaseURL = "http://127.0.0.1:1"
	g.h.Handle(context.Background(), g.cc, msg("tok-1", "n1", "task-1"))
	assertUntouched(t, g.dir)
}

func TestWipeErrorReportsFailed(t *testing.T) {
	g := newRig(t)
	g.h.Wipe = func(context.Context) ([]string, error) { return []string{"k2_key"}, errors.New("disk on fire") }
	g.h.Handle(context.Background(), g.cc, msg("tok-1", "n1", "task-1"))
	if !slices.Equal(g.j.states, []string{"in_progress", "failed"}) || g.j.errMsg != "disk on fire" {
		t.Fatalf("states %v error %q, want in_progress then failed with the wipe's error", g.j.states, g.j.errMsg)
	}
	if g.reboots != 0 || g.exits != 0 {
		t.Fatal("a failed reset rebooted")
	}
}

func TestIgnoredMessages(t *testing.T) {
	for name, payload := range map[string][]byte{
		"another node":       msg("tok-1", "n2", "task-1"),
		"no request id":      msg("", "n1", "task-1"),
		"no node id":         msg("tok-1", "", "task-1"),
		"not json":           []byte("{"),
		"legacy, wrong node": msg("tok-1", "n2", ""),
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			g.h.Handle(context.Background(), g.cc, payload)
			assertUntouched(t, g.dir)
			if len(g.j.states) != 0 || g.j.verify != 0 || g.reboots != 0 {
				t.Fatalf("states %v verify %d reboots %d for an ignored message", g.j.states, g.j.verify, g.reboots)
			}
		})
	}
}

func TestLegacyResetVerifiesThenWipes(t *testing.T) {
	g := newRig(t)
	g.h.Handle(context.Background(), g.cc, msg("tok-1", "n1", ""))
	if g.j.verify != 1 || len(g.j.states) != 0 {
		t.Fatalf("verify %d states %v, want one verify-reset and no status reports", g.j.verify, g.j.states)
	}
	assertWiped(t, g.dir)
	if g.reboots != 1 {
		t.Fatal("no reboot after a legacy reset")
	}
}

func TestLegacyResetRejected(t *testing.T) {
	g := newRig(t)
	g.h.Handle(context.Background(), g.cc, msg("forged", "n1", ""))
	assertUntouched(t, g.dir)
	if g.reboots != 0 {
		t.Fatal("rebooted after a rejected legacy reset")
	}
}

func TestRebootFailureQuits(t *testing.T) {
	g := newRig(t)
	g.h.Reboot = func() error { return errors.New("sudo: a password is required") }
	g.h.Handle(context.Background(), g.cc, msg("tok-1", "n1", "task-1"))
	if g.exits != 1 {
		t.Fatalf("exits %d, want the agent to quit when the reboot fails", g.exits)
	}
}

func TestWipeListsWhatItCleared(t *testing.T) {
	dir := stateDir(t)
	cleared, err := Wipe(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"k2_key", "node_database", "timers", "bluetooth_devices", "pantry:packages", "pantry:commands_list", "provisioning_marker", "config_credentials"}
	if !slices.Equal(cleared, want) {
		t.Fatalf("cleared = %v, want %v", cleared, want)
	}
	assertWiped(t, dir)
	// A second reset finds nothing and is not an error.
	if cleared, err := Wipe(dir); err != nil || len(cleared) != 0 {
		t.Fatalf("second wipe = %v, %v; want nothing and no error", cleared, err)
	}
}

func TestWipeKeepsGoingPastAFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can remove anything")
	}
	dir := stateDir(t)
	pkgs := filepath.Join(dir, "packages", "pkgs")
	if err := os.Chmod(pkgs, 0o500); err != nil { // its entries can't be removed
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(pkgs, 0o700) })
	cleared, err := Wipe(dir)
	if err == nil || !strings.Contains(err.Error(), "pantry:packages") {
		t.Fatalf("err = %v, want the packages failure", err)
	}
	if !slices.Contains(cleared, "config_credentials") || exists(dir, "node.json") {
		t.Fatalf("cleared = %v; the rest of the wipe must still run", cleared)
	}
}

// Freeze runs once jarvisd has confirmed the reset and before the wipe, so nothing the agent
// still holds (settings, timers) can write a file back between the wipe and the reboot.
func TestFreezeBeforeWipe(t *testing.T) {
	g := newRig(t)
	var order []string
	g.h.Freeze = func() { order = append(order, "freeze") }
	wipe := g.h.Wipe
	g.h.Wipe = func(ctx context.Context) ([]string, error) { order = append(order, "wipe"); return wipe(ctx) }
	g.h.Handle(context.Background(), g.cc, msg("tok-1", "n1", "task-1"))
	if !slices.Equal(order, []string{"freeze", "wipe"}) {
		t.Fatalf("order = %v, want freeze then wipe", order)
	}

	g = newRig(t)
	frozen := false
	g.h.Freeze = func() { frozen = true }
	g.h.Handle(context.Background(), g.cc, msg("forged", "n1", "task-1"))
	if frozen {
		t.Fatal("a rejected reset froze the node")
	}
}
