package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// props is `loginctl show-session` output for alice's session.
func props(active, remote, class, typ string) string {
	return "Active=" + active + "\nRemote=" + remote + "\nClass=" + class + "\nType=" + typ + "\nName=alice\nUser=1000\nDisplay=:0\n"
}

func TestParseDesktopSession(t *testing.T) {
	s, ok := parseDesktopSession("2", props("yes", "no", "user", "wayland"))
	if !ok || s != (desktopSession{ID: "2", User: "alice", UID: 1000, Type: "wayland", Display: ":0"}) {
		t.Fatalf("%+v %v", s, ok)
	}
	for name, p := range map[string]string{
		"inactive (another seat user, or switched away)": props("no", "no", "user", "x11"),
		"remote (ssh)":    props("yes", "yes", "user", "x11"),
		"the greeter":     props("yes", "no", "greeter", "wayland"),
		"a text console":  props("yes", "no", "user", "tty"),
		"an ssh session":  props("yes", "no", "user", "unspecified"),
		"root":            strings.Replace(props("yes", "no", "user", "x11"), "User=1000", "User=0", 1),
		"no user":         strings.Replace(props("yes", "no", "user", "x11"), "User=1000", "User=", 1),
		"empty (no info)": "",
	} {
		if s, ok := parseDesktopSession("3", p); ok {
			t.Errorf("%s: counted as a desktop session: %+v", name, s)
		}
	}
}

// fakeDesktop answers loginctl from a table and records every other command.
type fakeDesktop struct {
	sessions string
	props    map[string]string
	fail     map[string]error // by program name
	ran      [][]string
}

func (f *fakeDesktop) run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "loginctl" {
		if args[0] == "list-sessions" {
			return []byte(f.sessions), nil
		}
		p, ok := f.props[args[1]]
		if !ok {
			return nil, errors.New("no such session")
		}
		return []byte(p), nil
	}
	f.ran = append(f.ran, append([]string{name}, args...))
	// The program runuser runs through env: fail by the first one after env's variables.
	for _, a := range args {
		if err, ok := f.fail[a]; ok {
			return nil, err
		}
	}
	return nil, nil
}

func (f *fakeDesktop) opener() desktopOpener {
	return desktopOpener{
		run:        f.run,
		runtimeDir: func(uid int) string { return "/run/user/1000" },
		glob: func(string) ([]string, error) {
			return []string{"/run/user/1000/wayland-0.lock", "/run/user/1000/wayland-0"}, nil
		},
	}
}

const link = "http://192.168.1.5:7710/setup#token=secret"

func TestOpenInDesktopSession(t *testing.T) {
	f := &fakeDesktop{
		// The greeter's session first, then the user's: the user's is chosen.
		sessions: "c1 120 gdm seat0 tty1 active no -\n2 1000 alice seat0 tty2 active no -\n",
		props: map[string]string{
			"c1": "Active=no\nRemote=no\nClass=greeter\nType=wayland\nName=gdm\nUser=120\n",
			"2":  props("yes", "no", "user", "wayland"),
		},
	}
	var out bytes.Buffer
	if !f.opener().open(context.Background(), link, &out) {
		t.Fatalf("not opened: %s", out.String())
	}
	if len(f.ran) != 1 {
		t.Fatalf("ran %q", f.ran)
	}
	got := strings.Join(f.ran[0], " ")
	want := "runuser -u alice -- env XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus " +
		"systemd-run --user --collect --quiet --description=Open the Jarvis Automation setup page --property=KillMode=process " +
		"--setenv=DISPLAY=:0 --setenv=WAYLAND_DISPLAY=wayland-0 xdg-open " + link
	if got != want {
		t.Fatalf("ran\n  %s\nwant\n  %s", got, want)
	}
	if !strings.Contains(out.String(), "alice's browser") {
		t.Errorf("output %q", out.String())
	}
}

func TestOpenFallsBackWithoutAUserManager(t *testing.T) {
	f := &fakeDesktop{
		sessions: "2 1000 alice seat0 tty2 active no -\n",
		props:    map[string]string{"2": props("yes", "no", "user", "x11")},
		fail:     map[string]error{"systemd-run": errors.New("Failed to connect to bus")},
	}
	var out bytes.Buffer
	if !f.opener().open(context.Background(), link, &out) {
		t.Fatalf("not opened: %s", out.String())
	}
	if len(f.ran) != 2 {
		t.Fatalf("ran %q", f.ran)
	}
	got := strings.Join(f.ran[1], " ")
	want := "runuser -u alice -- env XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus DISPLAY=:0 setsid xdg-open " + link
	if got != want {
		t.Fatalf("fallback ran\n  %s\nwant\n  %s", got, want)
	}
}

func TestOpenWithoutADesktop(t *testing.T) {
	for name, f := range map[string]*fakeDesktop{
		"no sessions (a headless box)": {},
		"only ssh": {
			sessions: "5 1000 alice - pts/0 active no -\n",
			props:    map[string]string{"5": props("yes", "yes", "user", "tty")},
		},
	} {
		var out bytes.Buffer
		if f.opener().open(context.Background(), link, &out) || len(f.ran) > 0 || out.Len() > 0 {
			t.Errorf("%s: ran %q, said %q", name, f.ran, out.String())
		}
	}
	// Both ways fail: says so, never an error for the install.
	f := &fakeDesktop{
		sessions: "2 1000 alice seat0 tty2 active no -\n",
		props:    map[string]string{"2": props("yes", "no", "user", "x11")},
		fail:     map[string]error{"systemd-run": errors.New("no bus"), "setsid": errors.New("no xdg-open")},
	}
	var out bytes.Buffer
	if f.opener().open(context.Background(), link, &out) || !strings.Contains(out.String(), "couldn't open a browser") {
		t.Fatalf("said %q", out.String())
	}
	if strings.Contains(out.String(), "secret") {
		t.Errorf("the message repeats the token: %q", out.String())
	}
}

func TestIsSetupLink(t *testing.T) {
	if !isSetupLink(link) || isSetupLink("http://192.168.1.5:7710/") {
		t.Fatal("isSetupLink")
	}
}
