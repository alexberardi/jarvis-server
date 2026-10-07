package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

func TestSetupLink(t *testing.T) {
	cfg := config.Config{Host: "127.0.0.1", Ports: map[string]int{config.ListenerAdmin: 7710}}
	if got := setupLink(cfg, "tok"); got != "http://127.0.0.1:7710/setup#token=tok" {
		t.Fatal(got)
	}
	cfg.Host = "0.0.0.0" // all interfaces: the LAN address, else localhost
	if got := setupLink(cfg, "tok"); !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, ":7710/setup#token=tok") {
		t.Fatal(got)
	}
}

func TestPrintSetupLink(t *testing.T) {
	home := t.TempDir()
	cfg := config.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{config.ListenerAdmin: 7710}}
	var out bytes.Buffer
	if err := printSetupLink(cfg, &out); err != nil || !strings.Contains(out.String(), "admin is at http://127.0.0.1:7710/\n") {
		t.Fatalf("no token: %v %q", err, out.String())
	}
	os.WriteFile(filepath.Join(home, "setup-token"), []byte("abc123\n"), 0o600)
	out.Reset()
	if err := printSetupLink(cfg, &out); err != nil || !strings.Contains(out.String(), "http://127.0.0.1:7710/setup#token=abc123") ||
		!strings.Contains(out.String(), "Setup token: abc123") {
		t.Fatalf("token: %v %q", err, out.String())
	}
}

func TestAnnounceSetupPrintsTokenAndLink(t *testing.T) {
	var out bytes.Buffer
	cfg := config.Config{Host: "127.0.0.1", Ports: map[string]int{config.ListenerAdmin: 7710}}
	announceSetup(&out, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, "abc123", "/home/x/setup-token", false)
	s := out.String()
	for _, want := range []string{"http://127.0.0.1:7710/setup#token=abc123", "Setup token: abc123", "/home/x/setup-token"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
}

func TestInteractiveDesktop(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	// A redirected stderr (a file, a pipe, a service's journal) is never interactive.
	if interactiveDesktop(env("DISPLAY", ":0"), f) {
		t.Error("a file is not a terminal")
	}
	if interactiveDesktop(env("INVOCATION_ID", "x", "DISPLAY", ":0"), os.Stderr) {
		t.Error("systemd unit")
	}
	if interactiveDesktop(env("SSH_CONNECTION", "1 2 3 4", "DISPLAY", ":0"), os.Stderr) {
		t.Error("ssh session")
	}
}
