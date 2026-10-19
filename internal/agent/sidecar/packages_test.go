package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// packagesFile writes the agent's installed-packages list over testdata/packages.
func packagesFile(t *testing.T, names ...string) string {
	t.Helper()
	type entry struct {
		Name     string   `json:"name"`
		Root     string   `json:"root"`
		Commands []string `json:"commands"`
	}
	cmds := map[string]string{"alpha": "commands/greet/command.py", "beta": "commands/greet/command.py",
		"broken": "commands/boom/command.py", "dup": "commands/hello/command.py"}
	var list []entry
	for _, n := range names {
		root, err := filepath.Abs(filepath.Join("testdata", "packages", n))
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, entry{Name: n, Root: root, Commands: []string{cmds[n]}})
	}
	b, _ := json.Marshal(map[string]any{"packages": list})
	p := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Two installed packages with the same layout (both have commands/greet/) load side by side:
// each is its own top-level package, so relative imports and shared code at the root work.
// A package whose command can't import is reported, and the others still load.
func TestSidecarLoadsInstalledPackages(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	sc, err := Start(ctx, py, packagesFile(t, "alpha", "beta", "broken"), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	var names []string
	for _, tl := range sc.Available(ctx) {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "alpha_greet,beta_greet" {
		t.Fatalf("tools = %v", names)
	}
	if sc.Package("alpha_greet") != "alpha" || sc.Package("beta_greet") != "beta" || sc.Package("nope") != "" {
		t.Fatalf("packages = %q %q", sc.Package("alpha_greet"), sc.Package("beta_greet"))
	}
	f := sc.Failed()
	if len(f) != 1 || !strings.Contains(f["broken"], "a_module_that_is_not_installed") {
		t.Fatalf("failed = %v", f)
	}
	for name, want := range map[string]string{"alpha_greet": "hello from alpha", "beta_greet": "hello from beta"} {
		out, ok := sc.Call(ctx, name, nil, node.Request{})
		node.TakeSignals(out)
		if !ok || out["message"] != want {
			t.Fatalf("%s = %v", name, out)
		}
	}
}

// A Pi's built-in commands (testdata/builtins/fakenode stands in for jarvis-node-setup's
// commands package) load next to the packages: every *_command module but the skipped ones.
// A module that can't import is reported alone, and a package can't take a built-in's name.
func TestSidecarLoadsTheBuiltins(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	root, _ := filepath.Abs(filepath.Join("testdata", "builtins"))
	t.Setenv("PYTHONPATH", root)
	listing := packagesFile(t, "alpha", "dup")
	var doc map[string]any
	b, _ := os.ReadFile(listing)
	_ = json.Unmarshal(b, &doc)
	doc["builtins"] = map[string]any{"package": "fakenode", "skip": []string{"skipped_command"}}
	b, _ = json.Marshal(doc)
	_ = os.WriteFile(listing, b, 0o600)

	sc, err := Start(ctx, py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	var names []string
	for _, tl := range sc.Available(ctx) {
		names = append(names, tl.Name)
	}
	if sort.Strings(names); strings.Join(names, ",") != "alpha_greet,hello" {
		t.Fatalf("tools = %v", names)
	}
	if sc.Package("hello") != "" {
		t.Fatalf("a built-in has a package: %q", sc.Package("hello"))
	}
	f := sc.Failed()
	if len(f) != 2 || !strings.Contains(f["builtin broken_command"], "needs the Python node") ||
		!strings.Contains(f["dup"], "command hello is already provided by the node") {
		t.Fatalf("failed = %v", f)
	}
	out, _ := sc.Call(ctx, "hello", nil, node.Request{})
	if out["message"] != "hi" {
		t.Fatalf("hello = %v", out)
	}
}

// Live serves whichever sidecar is in service, and nothing while none is.
func TestLiveSwapsSidecars(t *testing.T) {
	ctx := context.Background()
	var l Live
	if l.Available(ctx) != nil || l.Commands() != nil || l.DataCommands() != nil || l.Failed() != nil {
		t.Fatal("an empty Live offers something")
	}
	if _, ok := l.Call(ctx, "alpha_greet", nil, node.Request{}); ok {
		t.Fatal("an empty Live claimed a call")
	}
	if _, err := l.Summaries(ctx, "x", nil); err != ErrNoPackages {
		t.Fatalf("summaries = %v", err)
	}
	if _, _, err := l.Create(ctx, "x", nodesettings.Object{}, ""); err != ErrNoPackages {
		t.Fatalf("create = %v", err)
	}

	py := python(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := Start(ctx, py, packagesFile(t, "alpha"), log, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Swap(a)
	if tl := l.Available(ctx); len(tl) != 1 || tl[0].Name != "alpha_greet" {
		t.Fatalf("tools = %v", tl)
	}
	b, err := Start(ctx, py, packagesFile(t, "alpha", "beta"), log, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Swap(b) // closes a
	if _, ok := a.Call(ctx, "alpha_greet", nil, node.Request{}); !ok {
		t.Fatal("a forgot its tools")
	} else if out, _ := a.Call(ctx, "alpha_greet", nil, node.Request{}); out["success"] != false {
		t.Fatalf("a call on the closed sidecar = %v", out)
	}
	if out, ok := l.Call(ctx, "beta_greet", nil, node.Request{}); !ok || out["message"] != "hello from beta" {
		t.Fatalf("beta = %v", out)
	}
	_ = l.Close()
	if l.Current() != nil {
		t.Fatal("Close left a sidecar in service")
	}
}
