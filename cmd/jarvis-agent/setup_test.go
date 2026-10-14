package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
)

func TestNeedsTypingPortal(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		goos string
		env  map[string]string
		want bool
	}{
		{"linux", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "GNOME"}, true},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "KDE"}, true},
		{"linux", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}, true},
		{"linux", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "Hyprland"}, false},
		{"linux", map[string]string{"XDG_SESSION_TYPE": "x11", "XDG_CURRENT_DESKTOP": "GNOME"}, false},
		{"darwin", map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "GNOME"}, false},
	} {
		if got := needsTypingPortal(c.goos, env(c.env)); got != c.want {
			t.Errorf("%s %v: %v", c.goos, c.env, got)
		}
	}
}

func TestPermissionCmdUsage(t *testing.T) {
	var b bytes.Buffer
	if err := permissionCmd([]string{"microphone"}, &b); err == nil {
		t.Fatal("accepted another permission")
	}
	if err := permissionCmd([]string{"screen"}, &b); err != nil || strings.TrimSpace(b.String()) == "" {
		t.Fatalf("%q %v", b.String(), err)
	}
}

func TestSetupSocketSitsNextToTheControlSocket(t *testing.T) {
	t.Setenv(browser.ControlSocketEnv, filepath.Join("/run/x", "control.sock"))
	p, err := setupSocketPath()
	if err != nil || p != filepath.Join("/run/x", "setup.sock") {
		t.Fatalf("%q %v", p, err)
	}
}

// The setup page's "Listen for Hey Jarvis" reaches the running agent over its control socket.
func TestControlSocketVoiceCommands(t *testing.T) {
	dir, err := os.MkdirTemp("", "jvc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "c.sock")
	ln, err := browser.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeVoice()
	sw := f.sw(true)
	var ready *voiceSwitch
	ctx, cancel := context.WithCancel(context.Background())
	wait := serveControl(ctx, ln, slog.New(slog.DiscardHandler), &runningPill{}, func() {}, func() *voiceSwitch { return ready })
	defer func() { cancel(); wait() }()
	dial := func() (net.Conn, error) { return browser.Dial(path) }

	if err := control.Send(dial, control.VoiceOff); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Fatalf("before the switch exists: %v", err)
	}
	ready = sw
	if err := control.Send(dial, control.VoiceOff); err == nil || !strings.Contains(err.Error(), "supported") {
		t.Fatalf("voice unsupported: %v", err)
	}
	sw.available = true
	if err := control.Send(dial, control.VoiceOff); err != nil || sw.On() {
		t.Fatalf("voice-off: %v on=%v", err, sw.On())
	}
	if err := control.Send(dial, control.VoiceOn); err != nil || !sw.On() {
		t.Fatalf("voice-on: %v on=%v", err, sw.On())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saved) != 2 || f.saved[0] || !f.saved[1] {
		t.Fatalf("saved %v", f.saved)
	}
}
