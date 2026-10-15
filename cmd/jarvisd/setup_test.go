package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/qrterm"
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
	// No token and jarvisd answering: setup is done (A10c: it used to say "Setup is done (or
	// jarvisd hasn't started yet)" whichever it was).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(p)
	cfg := config.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{config.ListenerAdmin: 7710, config.ListenerConfig: port}}
	var out bytes.Buffer
	if err := printSetupLink(cfg, &out, setupLinkOptions{}); err != nil || out.String() != "jarvisd is set up. The admin is at http://127.0.0.1:7710/\n" {
		t.Fatalf("no token, running: %v %q", err, out.String())
	}
	// No token and nothing answering: not started yet.
	srv.Close()
	out.Reset()
	if err := printSetupLink(cfg, &out, setupLinkOptions{}); err != nil || !strings.Contains(out.String(), "isn't answering yet") ||
		!strings.Contains(out.String(), "jarvisd setup-link") || !strings.Contains(out.String(), "http://127.0.0.1:7710/\n") {
		t.Fatalf("no token, not running: %v %q", err, out.String())
	}
	os.WriteFile(filepath.Join(home, "setup-token"), []byte("abc123\n"), 0o600)
	out.Reset()
	if err := printSetupLink(cfg, &out, setupLinkOptions{}); err != nil || !strings.Contains(out.String(), "http://127.0.0.1:7710/setup#token=abc123") ||
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

// install plan 1.2: --url is the URL alone (installers open it), --qr adds a QR code of it.
func TestSetupLinkURLAndQR(t *testing.T) {
	home := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(p)
	cfg := config.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{config.ListenerAdmin: 7710, config.ListenerConfig: port}}

	opt, err := parseSetupLinkArgs([]string{"--url"}, io.Discard)
	if err != nil || !opt.urlOnly || opt.qr {
		t.Fatalf("parse: %+v %v", opt, err)
	}
	var out bytes.Buffer
	if err := printSetupLink(cfg, &out, opt); err != nil || out.String() != "http://127.0.0.1:7710/\n" {
		t.Fatalf("set up, --url: %v %q", err, out.String())
	}
	// Set up, --qr: the admin URL as a code too (#105: "the URL" is the setup link while a
	// token exists, the admin URL afterwards), so a phone can open the admin after setup.
	qrOpt, _ := parseSetupLinkArgs([]string{"--qr"}, io.Discard)
	out.Reset()
	if err := printSetupLink(cfg, &out, qrOpt); err != nil {
		t.Fatal(err)
	}
	var admin bytes.Buffer
	if err := qrterm.Write(&admin, "http://127.0.0.1:7710/", qrterm.DarkOnLight); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.HasPrefix(s, "jarvisd is set up. The admin is at http://127.0.0.1:7710/\n") ||
		!strings.Contains(s, "scan this with a phone") || !strings.HasSuffix(s, admin.String()) {
		t.Fatalf("set up, --qr:\n%s", s)
	}
	os.WriteFile(filepath.Join(home, "setup-token"), []byte("abc123\n"), 0o600)
	out.Reset()
	if err := printSetupLink(cfg, &out, opt); err != nil || out.String() != "http://127.0.0.1:7710/setup#token=abc123\n" {
		t.Fatalf("token, --url: %v %q", err, out.String())
	}

	// --qr: the usual text, then the code, drawn with the style chosen for the terminal.
	opt, _ = parseSetupLinkArgs([]string{"--qr"}, io.Discard)
	out.Reset()
	if err := printSetupLink(cfg, &out, opt); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := qrterm.Write(&want, "http://127.0.0.1:7710/setup#token=abc123", qrterm.DarkOnLight); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.HasPrefix(s, "Finish setup in a browser:") || !strings.Contains(s, "scan this with a phone") || !strings.HasSuffix(s, want.String()) {
		t.Fatalf("--qr:\n%s", s)
	}
	// --url --qr: the URL line, a blank line, the code.
	opt, _ = parseSetupLinkArgs([]string{"--url", "--qr"}, io.Discard)
	out.Reset()
	if err := printSetupLink(cfg, &out, opt); err != nil || out.String() != "http://127.0.0.1:7710/setup#token=abc123\n\n"+want.String() {
		t.Fatalf("--url --qr: %v\n%s", err, out.String())
	}

	if _, err := parseSetupLinkArgs([]string{"extra"}, io.Discard); err == nil {
		t.Fatal("a stray argument should fail")
	}
	if _, err := parseSetupLinkArgs([]string{"--nope"}, io.Discard); err == nil {
		t.Fatal("an unknown flag should fail")
	}
	// Not a terminal: plain blocks.
	if s := qrStyleFor(&bytes.Buffer{}); s != qrterm.DarkOnLight {
		t.Fatalf("style for a buffer: %v", s)
	}
}
