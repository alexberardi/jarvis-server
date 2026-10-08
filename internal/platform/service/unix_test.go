//go:build unix

package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeRun records commands and answers from a script keyed by the command line.
type fakeRun struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error  // command line → error
	out   map[string]string // command line → output
	hook  func(line string)
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		hook(line)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return []byte(f.out[line]), f.fail[line]
}

type chownCall struct {
	path     string
	uid, gid int
}

func testSystemd(t *testing.T, userMode bool, euid int) (*systemd, *fakeRun, *[]chownCall, string) {
	t.Helper()
	root := t.TempDir()
	f := &fakeRun{fail: map[string]error{}, out: map[string]string{}}
	var chowns []chownCall
	accountExists := false
	f.hook = func(line string) {
		if strings.HasPrefix(line, "useradd ") {
			accountExists = true
		}
	}
	s := &systemd{
		user: userMode, out: &bytes.Buffer{}, run: f.run,
		unitPath: filepath.Join(root, "unit", unitName), envDir: filepath.Join(root, "etc-jarvisd"),
		lingerMark: filepath.Join(root, "config", Name, "linger-enabled"),
		euid: func() int { return euid },
		lookupUser: func(n string) (*user.User, error) {
			if n == Name && accountExists {
				return &user.User{Username: Name, Uid: "990", Gid: "985"}, nil
			}
			return nil, user.UnknownUserError(n)
		},
		lookupGroup: func(g string) (*user.Group, error) {
			if g == "render" {
				return &user.Group{Name: g}, nil
			}
			return nil, user.UnknownGroupError(g)
		},
		chown: func(p string, uid, gid int) error {
			chowns = append(chowns, chownCall{p, uid, gid})
			return nil
		},
		current: func() (*user.User, error) { return &user.User{Username: "alex"}, nil },
		getenv:  func(string) string { return "" },
	}
	return s, f, &chowns, root
}

func TestSystemdInstallSystem(t *testing.T) {
	s, f, chowns, root := testSystemd(t, false, 0)
	home := filepath.Join(root, "var-lib-jarvisd")
	ctx := context.Background()
	if err := s.Install(ctx, InstallOptions{Binary: "/usr/local/bin/jarvisd", Home: home}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"useradd --system --user-group --home-dir " + home + " --no-create-home --shell ", // shell varies
		"usermod --append --groups render jarvisd",
		"systemctl daemon-reload",
		"systemctl enable jarvisd.service",
		"systemctl restart jarvisd.service",
	}
	if len(f.calls) != len(want) {
		t.Fatalf("calls %q", f.calls)
	}
	for i, w := range want {
		if !strings.HasPrefix(f.calls[i], w) {
			t.Errorf("call %d = %q, want prefix %q", i, f.calls[i], w)
		}
	}
	if st, err := os.Stat(home); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("home %v %v", st, err)
	}
	env := filepath.Join(s.envDir, "jarvisd.env")
	if st, err := os.Stat(env); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("env file %v %v", st, err)
	}
	if st, err := os.Stat(s.unitPath); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("unit %v %v", st, err)
	}
	if !slices.Contains(*chowns, chownCall{home, 990, 985}) || !slices.Contains(*chowns, chownCall{env, 0, 985}) ||
		!slices.Contains(*chowns, chownCall{s.envDir, 0, 985}) {
		t.Errorf("chowns %v", *chowns)
	}
	if got := s.InstalledHome(); got != home {
		t.Errorf("installed home %q", got)
	}

	// Reinstall: the account exists, the edited env file is kept, the service restarts.
	os.WriteFile(env, []byte("JARVIS_LOG_LEVEL=debug\n"), 0o640)
	f.calls = nil
	if err := s.Install(ctx, InstallOptions{Binary: "/usr/local/bin/jarvisd", Home: home}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(f.calls[0], "useradd") {
		t.Error("useradd on reinstall")
	}
	if b, _ := os.ReadFile(env); string(b) != "JARVIS_LOG_LEVEL=debug\n" {
		t.Error("reinstall overwrote the env file")
	}

	// Status parses systemctl show.
	f.out["systemctl show jarvisd.service -p ActiveState -p SubState -p MainPID -p NRestarts"] = "ActiveState=active\nSubState=running\nMainPID=4242\nNRestarts=2\n"
	st, err := s.Status(ctx)
	if err != nil || !st.Running || st.PID != 4242 || st.Home != home || !strings.Contains(st.Detail, "restarts 2") {
		t.Fatalf("status %+v %v", st, err)
	}

	f.calls = nil
	if err := s.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("unit not removed")
	}
	if _, err := os.Stat(env); err != nil {
		t.Error("uninstall must keep the env file")
	}
	if _, err := os.Stat(home); err != nil {
		t.Error("uninstall must keep the data")
	}
	if f.calls[0] != "systemctl disable --now jarvisd.service" {
		t.Errorf("calls %q", f.calls)
	}
	if err := s.Uninstall(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("second uninstall: %v", err)
	}
	if err := s.Start(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("start after uninstall: %v", err)
	}

	// --purge: the home, /etc/jarvisd and the account.
	plan := s.PurgePlan(home)
	if plan.Home != home || !slices.Equal(plan.Extra, []string{s.envDir}) || plan.Account != Name {
		t.Fatalf("plan %+v", plan)
	}
	f.calls = nil
	if err := s.Purge(ctx, plan); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{home, s.envDir} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s not purged", p)
		}
	}
	if !slices.Equal(f.calls, []string{"userdel jarvisd"}) {
		t.Errorf("calls %q", f.calls)
	}
	// User mode: the home only.
	u, _, _, _ := testSystemd(t, true, 1000)
	if p := u.PurgePlan(home); p.Extra != nil || p.Account != "" {
		t.Errorf("user plan %+v", p)
	}
}

func TestCheckPurgeHome(t *testing.T) {
	dir := t.TempDir()
	userHome, _ := os.UserHomeDir()
	other := filepath.Join(dir, "data")
	os.Mkdir(other, 0o700)
	for _, bad := range []string{"", "relative/jarvisd", "/", userHome, filepath.Join(dir, ".jarvis"), other} {
		if err := CheckPurgeHome(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	os.WriteFile(filepath.Join(other, "jarvis.db"), nil, 0o600)
	for _, ok := range []string{"/var/lib/jarvisd", filepath.Join(dir, ".jarvisd"), other} {
		if err := CheckPurgeHome(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestSystemdInstallRefusals(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := testSystemd(t, false, 1000)
	if err := s.Install(ctx, InstallOptions{Binary: "/usr/local/bin/jarvisd"}); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Errorf("non-root: %v", err)
	}
	s, f, _, root := testSystemd(t, false, 0)
	for _, bin := range []string{"/home/alex/Downloads/jarvisd", "/tmp/jarvisd", "/root/jarvisd"} {
		if err := s.Install(ctx, InstallOptions{Binary: bin, Home: filepath.Join(root, "h")}); err == nil {
			t.Errorf("%s: want refusal", bin)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("refusals ran %q", f.calls)
	}
	u, _, _, _ := testSystemd(t, true, 0)
	if err := u.Install(ctx, InstallOptions{Binary: "/x"}); err == nil {
		t.Error("--user as root: want refusal")
	}
}

func TestSystemdInstallUser(t *testing.T) {
	s, f, chowns, root := testSystemd(t, true, 1000)
	home := filepath.Join(root, "dot-jarvisd")
	f.fail["loginctl enable-linger alex"] = errors.New("polkit says no")
	if err := s.Install(context.Background(), InstallOptions{Binary: "/home/alex/.local/bin/jarvisd", Home: home}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable jarvisd.service",
		"systemctl --user restart jarvisd.service",
		"loginctl show-user alex -p Linger --value",
		"loginctl enable-linger alex",
	}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls %q", f.calls)
	}
	if _, err := os.Stat(s.lingerMark); err == nil {
		t.Error("lingering wasn't turned on, but it was recorded as such")
	}
	if len(*chowns) != 0 {
		t.Errorf("user mode chowned %v", *chowns)
	}
	if st, err := os.Stat(filepath.Join(home, "jarvisd.env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("env %v %v", st, err)
	}
	if !strings.Contains(s.out.(*bytes.Buffer).String(), "sudo loginctl enable-linger alex") {
		t.Errorf("linger failure not explained: %s", s.out)
	}
	b, _ := os.ReadFile(s.unitPath)
	if strings.Contains(string(b), "User=") || !strings.Contains(string(b), "WantedBy=default.target") {
		t.Errorf("user unit:\n%s", b)
	}
}

// A10c U8: `service uninstall --user` left lingering on although install had turned it on. It
// now turns it off again, but only when install was what turned it on.
func TestSystemdUserLinger(t *testing.T) {
	ctx := context.Background()
	const show = "loginctl show-user alex -p Linger --value"
	for _, tc := range []struct {
		before      string // Linger before install
		wantDisable bool
	}{
		{"no", true},
		{"yes", false},
		{"", false}, // unknown: don't claim it
	} {
		s, f, _, root := testSystemd(t, true, 1000)
		f.out[show] = tc.before + "\n"
		if tc.before == "" {
			f.fail[show] = errors.New("no logind")
		}
		home := filepath.Join(root, "h")
		if err := s.Install(ctx, InstallOptions{Binary: "/home/alex/.local/bin/jarvisd", Home: home}); err != nil {
			t.Fatal(err)
		}
		// A reinstall sees lingering on (we turned it on) and must keep the mark.
		f.out[show] = "yes\n"
		delete(f.fail, show)
		if err := s.Install(ctx, InstallOptions{Binary: "/home/alex/.local/bin/jarvisd", Home: home}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(s.lingerMark); (err == nil) != tc.wantDisable {
			t.Errorf("before %q: mark present = %v", tc.before, err == nil)
		}
		f.calls = nil
		if err := s.Uninstall(ctx); err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(f.calls, "loginctl disable-linger alex"); got != tc.wantDisable {
			t.Errorf("before %q: disable-linger %v, calls %q", tc.before, got, f.calls)
		}
		if _, err := os.Stat(s.lingerMark); err == nil {
			if tc.wantDisable {
				t.Errorf("before %q: mark left after uninstall", tc.before)
			}
		}
	}

	// disable-linger failing: explained, and the mark kept for a retry.
	s, f, _, root := testSystemd(t, true, 1000)
	f.out["loginctl show-user alex -p Linger --value"] = "no\n"
	if err := s.Install(ctx, InstallOptions{Binary: "/x/jarvisd", Home: filepath.Join(root, "h")}); err != nil {
		t.Fatal(err)
	}
	f.fail["loginctl disable-linger alex"] = errors.New("polkit says no")
	if err := s.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.out.(*bytes.Buffer).String(), "loginctl disable-linger alex") {
		t.Errorf("failure not explained: %s", s.out)
	}
	if _, err := os.Stat(s.lingerMark); err != nil {
		t.Error("mark dropped although lingering is still on")
	}
}

func TestUserUnitPath(t *testing.T) {
	home := func() (string, error) { return "/home/a", nil }
	if p := userUnitPath(func(string) string { return "" }, home); p != "/home/a/.config/systemd/user/jarvisd.service" {
		t.Error(p)
	}
	if p := userUnitPath(func(k string) string { return map[string]string{"XDG_CONFIG_HOME": "/cfg"}[k] }, home); p != "/cfg/systemd/user/jarvisd.service" {
		t.Error(p)
	}
}

func testLaunchd(t *testing.T, euid int, sudoUser string) (*launchd, *fakeRun, *[]chownCall, string) {
	t.Helper()
	root := t.TempDir()
	f := &fakeRun{fail: map[string]error{}, out: map[string]string{}}
	var chowns []chownCall
	l := &launchd{
		out: &bytes.Buffer{}, run: f.run, plistPath: filepath.Join(root, "LaunchDaemons", LaunchdLabel+".plist"),
		euid:   func() int { return euid },
		getenv: func(k string) string { return map[string]string{"SUDO_USER": sudoUser}[k] },
		lookupUser: func(n string) (*user.User, error) {
			if n == "alex" {
				return &user.User{Username: n, Uid: "501", Gid: "20", HomeDir: filepath.Join(root, "Users", "alex")}, nil
			}
			return nil, user.UnknownUserError(n)
		},
		chown: func(p string, uid, gid int) error {
			chowns = append(chowns, chownCall{p, uid, gid})
			return nil
		},
		settle: 0,
	}
	return l, f, &chowns, root
}

func TestLaunchdInstall(t *testing.T) {
	l, f, chowns, root := testLaunchd(t, 0, "alex")
	ctx := context.Background()
	loaded := false
	f.hook = func(line string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasPrefix(line, "launchctl bootstrap"):
			loaded = true
		case strings.HasPrefix(line, "launchctl bootout"):
			loaded = false
		}
		if loaded {
			delete(f.fail, "launchctl print "+target)
			f.out["launchctl print "+target] = "system/net.jarvisautomation.jarvisd = {\n\tstate = running\n\tpid = 777\n\tlast exit code = (never exited)\n}\n"
		} else {
			f.fail["launchctl print "+target] = errors.New("Could not find service")
		}
	}
	f.fail["launchctl print "+target] = errors.New("Could not find service")
	if err := l.Install(ctx, InstallOptions{Binary: "/usr/local/bin/jarvisd"}); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "Users", "alex", ".jarvisd")
	want := []string{
		"launchctl print " + target,
		"launchctl print " + target,
		"launchctl bootstrap system " + l.plistPath,
		"launchctl enable " + target,
		"launchctl kickstart " + target,
	}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls\n%q\nwant\n%q", f.calls, want)
	}
	if !slices.Contains(*chowns, chownCall{home, 501, 20}) || !slices.Contains(*chowns, chownCall{filepath.Join(home, "jarvisd.env"), 501, 20}) {
		t.Errorf("chowns %v", *chowns)
	}
	b, _ := os.ReadFile(l.plistPath)
	if !strings.Contains(string(b), "<string>alex</string>") || l.InstalledHome() != home {
		t.Errorf("plist:\n%s", b)
	}
	st, err := l.Status(ctx)
	if err != nil || !st.Running || st.PID != 777 {
		t.Fatalf("status %+v %v", st, err)
	}

	// Reinstall boots the loaded job out before bootstrapping again.
	f.calls = nil
	if err := l.Install(ctx, InstallOptions{Binary: "/usr/local/bin/jarvisd"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "launchctl bootout "+target) {
		t.Errorf("reinstall calls %q", f.calls)
	}

	f.calls = nil
	if err := l.Restart(ctx); err != nil || !slices.Contains(f.calls, "launchctl kickstart -k "+target) {
		t.Errorf("restart %v %q", err, f.calls)
	}
	if err := l.Stop(ctx); err != nil || loaded {
		t.Errorf("stop %v loaded=%v", err, loaded)
	}
	if err := l.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.plistPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("plist not removed")
	}
	if _, err := os.Stat(home); err != nil {
		t.Error("uninstall must keep the data")
	}
}

func TestLaunchdRefusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		euid int
		sudo string
		opts InstallOptions
	}{
		{501, "alex", InstallOptions{Binary: "/b"}},
		{0, "", InstallOptions{Binary: "/b"}},
		{0, "root", InstallOptions{Binary: "/b"}},
		{0, "alex", InstallOptions{Binary: "/b", User: true}},
		{0, "nobody-here", InstallOptions{Binary: "/b"}},
	} {
		l, f, _, _ := testLaunchd(t, tc.euid, tc.sudo)
		if err := l.Install(ctx, tc.opts); err == nil {
			t.Errorf("%+v: want refusal", tc)
		}
		if len(f.calls) != 0 {
			t.Errorf("%+v ran %q", tc, f.calls)
		}
	}
	// --run-as names the account without sudo's help.
	l, _, _, _ := testLaunchd(t, 0, "")
	l.run = (&fakeRun{fail: map[string]error{"launchctl print " + target: errors.New("no")}}).run
	if err := l.Install(ctx, InstallOptions{Binary: "/b", RunAs: "alex", NoStart: true}); err != nil {
		t.Fatal(err)
	}
}
