package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
	"github.com/alexberardi/jarvis-server/internal/agent/provisioning"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// The Pi profile adds the factory reset and the routines nudge to the node's topics, the
// context requests to its request filters, and a routines pull to every connect.
func TestWirePiTasks(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	creds := node.Creds{NodeID: "n1", NodeKey: "k", ConfigURL: "http://unused"}
	if err := node.SaveCreds(filepath.Join(dir, "node.json"), creds); err != nil {
		t.Fatal(err)
	}
	st, err := nodesettings.OpenStore(dir, "n1")
	if err != nil {
		t.Fatal(err)
	}
	live := &sidecar.Live{}
	tools := node.Multi{live}
	r := &node.Runner{Creds: creds, Log: log}
	wireNodeSettings(r, st, tools, nil, nil, nil, log)
	quit := make(chan struct{})
	tm := &timers.Service{Path: filepath.Join(dir, "timers.json"), Log: log}
	tm.Start()
	wirePiTasks(r, st, dir, tools, nil, tm, func() { close(quit) }, log)

	var topics, requests []string
	for k := range r.Topics {
		topics = append(topics, k)
	}
	for k := range r.Requests {
		requests = append(requests, k)
	}
	sort.Strings(topics)
	sort.Strings(requests)
	if want := "config/push,factory-reset,k2/provision,package-secrets,routines/sync,settings/request"; strings.Join(topics, ",") != want {
		t.Fatalf("topics = %v, want %s", topics, want)
	}
	if want := "command-data/+,context/+"; strings.Join(requests, ",") != want {
		t.Fatalf("requests = %v, want %s", requests, want)
	}

	// jarvisd: the routines and the reset's status; the settings backstop's calls get a 404.
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path+" "+req.Header.Get("X-Reset-Token"))
		mu.Unlock()
		switch {
		case req.URL.Path == "/api/v0/nodes/n1/routines":
			io.WriteString(w, `{"routines":{"movie_night":{"trigger_phrases":["movie time"],"steps":[]}}}`)
		case req.URL.Path == "/api/v0/nodes/factory-reset/t1/status" && req.Header.Get("X-Reset-Token") == "tok":
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	cc := node.NewCC(srv.URL, creds)

	r.OnMQTTConnect(context.Background(), cc)
	if _, ok := st.Data(nodesettings.RoutineCommand, "movie_night", time.Now()); !ok {
		t.Fatal("no routine/movie_night after a connect: the pull didn't run")
	}

	// The context requests answer even with no sidecar running.
	if topic, body := r.Requests[nodesettings.TopicContext](context.Background(), "jarvis/nodes/n1/context/operations",
		[]byte(`{"correlation_id":"c1"}`)); topic != "jarvis/nodes/n1/context/operations/response/c1" || string(body) != `{"ok":true,"providers":[]}` {
		t.Fatalf("context = %q %s", topic, body)
	}

	reboots := 0
	old := rebootPi
	rebootPi = func() error { reboots++; return nil }
	defer func() { rebootPi = old }()
	r.Topics["factory-reset"](context.Background(), cc, []byte(`{"request_id":"tok","node_id":"n1","task_id":"t1"}`))
	if _, err := os.Stat(filepath.Join(dir, "node.json")); !os.IsNotExist(err) {
		t.Fatalf("node.json after the reset: %v", err)
	}
	if reboots != 1 {
		t.Fatalf("reboots = %d", reboots)
	}
	// Sealed before the wipe: a config push or a timer before the reboot writes nothing back.
	if err := st.SetEnabled("weather", false); !errors.Is(err, nodesettings.ErrSealed) {
		t.Fatalf("SetEnabled after the reset = %v, want ErrSealed", err)
	}
	if _, err := tm.Set(60, nil); err == nil {
		t.Fatal("a timer was set after the reset")
	}
	for _, f := range []string{nodesettings.SettingsFile, "timers.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Fatalf("%s after the reset: %v", f, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var statuses []string
	for _, c := range calls {
		if strings.Contains(c, "factory-reset") {
			statuses = append(statuses, c)
		}
	}
	if want := []string{"POST /api/v0/nodes/factory-reset/t1/status tok", "POST /api/v0/nodes/factory-reset/t1/status tok"}; !slices.Equal(statuses, want) {
		t.Fatalf("status calls = %v", statuses)
	}
}

// The provisioning API's POST /api/v1/factory-reset runs the same wipe.
func TestProvisionOptionsFactoryReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")
	if err := node.SaveCreds(path, node.Creds{NodeID: "n1", NodeKey: "k", ConfigURL: "http://x"}); err != nil {
		t.Fatal(err)
	}
	o := provisionOptions(path, &provisioning.Simulated{}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if o.FactoryReset == nil || !o.Provisioned() {
		t.Fatal("no factory reset, or not provisioned to begin with")
	}
	cleared, err := o.FactoryReset(context.Background())
	if err != nil || !slices.Contains(cleared, "provisioning_marker") {
		t.Fatalf("FactoryReset = %v, %v", cleared, err)
	}
	if o.Provisioned() {
		t.Fatal("still provisioned after the reset")
	}
}

// A routines pull keeps the routines the installed Pantry packages ship: their routine components.
func TestPantryRoutines(t *testing.T) {
	root := t.TempDir()
	write := func(name, manifest string, files ...string) {
		t.Helper()
		dir := filepath.Join(root, "pkgs", name)
		for _, f := range append(files, packages.ManifestFile) {
			p := filepath.Join(dir, filepath.FromSlash(f))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("cinema", "name: cinema\nversion: \"1.0\"\ncomponents:\n"+
		"  - {type: routine, name: movie_night, path: routines/movie_night/routine.json}\n"+
		"  - {type: command, name: projector, path: commands/projector/command.py}\n",
		"routines/movie_night/routine.json", "commands/projector/command.py")
	write("plain", "name: plain\nversion: \"1.0\"\ncomponents:\n  - {type: command, name: lamp, path: commands/lamp/command.py}\n",
		"commands/lamp/command.py")
	pm := &packages.Manager{Root: root, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got := pantryRoutines(pm)(); !slices.Equal(got, []string{"movie_night"}) {
		t.Fatalf("pantryRoutines = %v, want [movie_night]", got)
	}
	if got := pantryRoutines(nil)(); got != nil {
		t.Fatalf("pantryRoutines(nil) = %v", got)
	}

	// Their definitions: the file is the manifest text here, so not a routine (skipped), until
	// it is one.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if got := pantryRoutineDefs(pm, log)(); len(got) != 0 {
		t.Fatalf("defs of a non-routine file = %v", got)
	}
	def := `{"trigger_phrases": ["movie time"], "steps": [{"command": "projector", "args": {}}]}`
	if err := os.WriteFile(filepath.Join(root, "pkgs", "cinema", "routines", "movie_night", "routine.json"), []byte(def), 0o600); err != nil {
		t.Fatal(err)
	}
	got := pantryRoutineDefs(pm, log)()
	if phrases, _ := got["movie_night"]["trigger_phrases"].([]any); len(got) != 1 || len(phrases) != 1 || phrases[0] != "movie time" {
		t.Fatalf("defs = %v", got)
	}
	// A component path that leaves the package isn't read.
	write("sneaky", "name: sneaky\nversion: \"1.0\"\ncomponents:\n  - {type: routine, name: x, path: ../cinema/routines/movie_night/routine.json}\n")
	if _, ok := pantryRoutineDefs(pm, log)()["x"]; ok {
		t.Fatal("read a routine outside its package")
	}
	if got := pantryRoutineDefs(nil, log)(); got != nil {
		t.Fatalf("pantryRoutineDefs(nil) = %v", got)
	}
}
