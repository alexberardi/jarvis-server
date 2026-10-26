package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/evdev"
	"github.com/alexberardi/jarvis-server/internal/agent/firstrun"
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
	if err := permissionCmd([]string{"automation"}, &b); err == nil {
		t.Fatal("accepted another permission")
	}
	for _, name := range []string{"accessibility", "microphone", "screen", "input-monitoring"} {
		b.Reset()
		if err := permissionCmd([]string{name}, &b); err != nil || strings.TrimSpace(b.String()) == "" {
			t.Fatalf("%s: %q %v", name, b.String(), err)
		}
	}
}

func TestParseStatus(t *testing.T) {
	for out, want := range map[string]string{
		"granted\n": firstrun.Granted,
		"denied\n":  firstrun.Denied,
		"not_asked\nRecording the desktop needs…\n": firstrun.NotAsked,
		"":      firstrun.Unknown,
		"yes\n": firstrun.Unknown,
	} {
		if got := parseStatus(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

// The setup page reads every grant from a fresh process on each poll (a process keeps what it
// had at its start), so one granted while the page is open turns Granted on the next poll; it
// says "restart to apply" until the running agent holds it itself.
func TestMacPermissionsFollowAGrantWhileOpen(t *testing.T) {
	fresh := map[string]string{"accessibility": firstrun.NotAsked, "microphone": firstrun.NotAsked,
		"screen": firstrun.NotAsked, "input-monitoring": firstrun.NotAsked}
	var asked []string
	agent := map[string]string{"accessibility": firstrun.NotAsked, "microphone": firstrun.NotAsked}
	running := true
	perms := macPermissions(func(name string) string { asked = append(asked, name); return fresh[name] },
		func() map[string]string {
			if !running {
				return nil
			}
			return agent
		})
	byID := map[string]firstrun.Permission{}
	for _, p := range perms {
		byID[p.ID] = p
	}
	for _, id := range []string{"accessibility", "microphone", "screen", "input-monitoring"} {
		p := byID[id]
		if p.Status == nil || p.NeedsRestart == nil {
			t.Fatalf("%s: no fresh status or restart check", id)
		}
		if st := p.Status(); st != firstrun.NotAsked {
			t.Fatalf("%s before: %q", id, st)
		}
	}
	if !slices.Equal(asked, []string{"accessibility", "microphone", "screen", "input-monitoring"}) {
		t.Fatalf("asked a fresh process for %q", asked)
	}

	// Granted in System Settings between two polls.
	fresh["accessibility"], fresh["microphone"] = firstrun.Granted, firstrun.Denied
	if st := byID["accessibility"].Status(); st != firstrun.Granted {
		t.Fatalf("accessibility after the grant: %q", st)
	}
	if st := byID["microphone"].Status(); st != firstrun.Denied {
		t.Fatalf("microphone after refusing: %q", st)
	}
	if !byID["accessibility"].NeedsRestart() {
		t.Fatal("the running agent doesn't hold it yet: no restart asked")
	}
	agent["accessibility"] = firstrun.Granted // restarted
	if byID["accessibility"].NeedsRestart() {
		t.Fatal("restart asked of an agent that holds it")
	}
	running = false // or one too old to say
	if byID["screen"].NeedsRestart() {
		t.Fatal("restart asked with no agent answering")
	}
}

// The running agent answers what it holds itself, which the setup page compares with a fresh
// process's answer.
func TestControlSocketAnswersTheAgentsPermissions(t *testing.T) {
	checks := map[string]func() string{"screen": func() string { return firstrun.NotAsked },
		"accessibility": func() string { return firstrun.Granted }}
	if got := ownPermissions(checks); got != "accessibility=granted screen=not_asked" {
		t.Fatalf("%q", got)
	}
	dir, err := os.MkdirTemp("", "jvp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "c.sock")
	dial := func() (net.Conn, error) { return browser.Dial(path) }
	if m := agentPermissions(dial); m != nil {
		t.Fatalf("no agent: %v", m)
	}
	ln, err := browser.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := serveControl(ctx, ln, slog.New(slog.DiscardHandler), &runningPill{}, func() {}, func() *voiceSwitch { return nil }, func() error { return nil }, nil, nil, nil)
	defer func() { cancel(); wait() }()
	m := agentPermissions(dial)
	for name := range freshChecks {
		if m[name] == "" {
			t.Fatalf("%s missing from %v", name, m)
		}
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
	wait := serveControl(ctx, ln, slog.New(slog.DiscardHandler), &runningPill{}, func() {}, func() *voiceSwitch { return ready }, func() error { return nil }, nil, nil, nil)
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

// On Hyprland and sway the setup page shows the input group step (granted once the keyboard
// opens), with the same command the recording's refusal gives; elsewhere nothing.
func TestLinuxInputPermission(t *testing.T) {
	if p := linuxPermissions("linux", "", func() error { return nil }); p != nil {
		t.Fatalf("GNOME/KDE/X11: %+v", p)
	}
	if p := linuxPermissions("darwin", "hyprland", func() error { return nil }); p != nil {
		t.Fatalf("macOS: %+v", p)
	}
	var access error = evdev.ErrNoAccess
	p := linuxPermissions("linux", "hyprland", func() error { return access })
	if len(p) != 1 || p[0].ID != "input-monitoring" || p[0].How != inputGroupHow || p[0].Status() != firstrun.NotAsked {
		t.Fatalf("%+v", p)
	}
	access = nil
	if p[0].Status() != firstrun.Granted {
		t.Fatal("not granted once the keyboard opens")
	}
	// #571: a logout loses the user's work, and doesn't even restart the systemd user manager.
	for _, text := range []string{computer.ErrInputGroup.Error(), p[0].Why, p[0].How, inputGroupHint(false), inputGroupHint(true)} {
		low := strings.ToLower(text)
		if strings.Contains(low, "log out") || strings.Contains(low, "reboot") ||
			(text != p[0].Why && !strings.Contains(text, "jarvis-agent restart")) {
			t.Errorf("advice %q", text)
		}
	}
	for _, text := range []string{computer.ErrInputGroup.Error(), p[0].How, inputGroupHint(false)} {
		if !strings.Contains(text, inputGroupCmd) {
			t.Errorf("%q lacks %q", text, inputGroupCmd)
		}
	}
	if strings.Contains(inputGroupHint(true), "usermod") {
		t.Error("a member is told to add the group again")
	}
}
