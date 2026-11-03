package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
)

// The agent's app catalogue, built exactly as cmd/jarvis-agent's appCatalogue builds it under
// ccsuite's baseEnv, must find "Jarvis Test Notes" once installNotesEntry ran: the model's first
// desktop step is desktop_open {target: "Jarvis Test Notes"}, and before this entry existed it
// was app_not_found on every run (#528).
func TestNotesDesktopEntryInAgentCatalogue(t *testing.T) {
	e := &Env{Dir: t.TempDir()}
	agentHome := e.path("agent-home")
	if err := installNotesEntry(agentHome, e.notesLauncher()); err != nil {
		t.Fatal(err)
	}
	// Hermetic stand-in for the fallback data dirs: unset, the host's /usr/share would leak in.
	vars := map[string]string{"XDG_DATA_DIRS": t.TempDir()}
	for _, kv := range e.baseEnv(t.TempDir()) {
		k, v, _ := strings.Cut(kv, "=")
		if _, ok := vars[k]; !ok {
			vars[k] = v
		}
	}
	list, err := apps.Build(apps.Env{GOOS: "linux", Home: agentHome, Getenv: func(k string) string { return vars[k] }}, apps.Config{}).List()
	if err != nil {
		t.Fatal(err)
	}
	p, err := apps.Find(list, "Jarvis Test Notes")
	if err != nil {
		t.Fatalf("Find(Jarvis Test Notes): %v (catalogue of %d apps)", err, len(list))
	}
	want := filepath.Join(agentHome, ".local", "share", "applications", "jarvis-test-notes.desktop")
	if p.App.Path != want {
		t.Fatalf("found %s, want %s", p.App.Path, want)
	}
	b, err := os.ReadFile(p.App.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Exec="+e.notesLauncher()+"\n") {
		t.Fatalf("the entry does not launch the launcher:\n%s", b)
	}
}

// The launcher the .desktop entry runs must be idempotent: StartNotes already runs the app per
// case, so a desktop_open while it is up is a no-op, and only a gone (or never-started) app is
// started. A stale pidfile must not stop the launch.
func TestNotesLauncherIdempotent(t *testing.T) {
	dir := t.TempDir()
	pidf := filepath.Join(dir, "notes.pid")
	marker := filepath.Join(dir, "started")
	launcher := filepath.Join(dir, "launch.sh")
	body := notesLauncherBody(pidf, "exec touch "+shQuote(marker))
	if err := os.WriteFile(launcher, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func() error {
		out, err := exec.Command("sh", launcher).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}

	// Alive (our own pid): no second start.
	if err := os.WriteFile(pidf, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("launcher with the app up: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the launcher started a second app over a running one")
	}

	// Stale pidfile (a pid above any real pid_max): start.
	if err := os.WriteFile(pidf, []byte("99999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("launcher with a stale pidfile: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the launcher did not start the app after it died")
	}

	// No pidfile at all: start.
	os.Remove(marker)
	os.Remove(pidf)
	if err := run(); err != nil {
		t.Fatalf("launcher with no pidfile: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the launcher did not start the app fresh")
	}
}
