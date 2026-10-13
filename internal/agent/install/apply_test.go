package install

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeReg is HKCU in a map: key -> value name -> data.
type fakeReg struct {
	mu   sync.Mutex
	keys map[string]map[string]string
}

func (r *fakeReg) SetString(key, name, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = map[string]map[string]string{}
	}
	if r.keys[key] == nil {
		r.keys[key] = map[string]string{}
	}
	r.keys[key][name] = value
	return nil
}

func (r *fakeReg) GetString(key, name string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.keys[key][name]
	return v, ok, nil
}

func (r *fakeReg) DeleteValue(key, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys[key], name)
	if len(r.keys[key]) == 0 && key == RunKey {
		delete(r.keys, key)
	}
	return nil
}

func (r *fakeReg) DeleteTree(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.keys {
		if k == key || strings.HasPrefix(k, key+`\`) {
			delete(r.keys, k)
		}
	}
	return nil
}

func (r *fakeReg) dump() []string {
	var out []string
	for k, vs := range r.keys {
		for n, v := range vs {
			out = append(out, k+"|"+n+"="+v)
		}
	}
	slices.Sort(out)
	return out
}

type fakeLogin struct{ registered map[string]bool }

func (l *fakeLogin) Register(n string) error {
	if l.registered == nil {
		l.registered = map[string]bool{}
	}
	l.registered[n] = true
	return nil
}
func (l *fakeLogin) Unregister(n string) error         { delete(l.registered, n); return nil }
func (l *fakeLogin) Registered(n string) (bool, error) { return l.registered[n], nil }

// fakeWeb writes a manifest where browser.Register would, so uninstall has something to remove.
type fakeWeb struct{ path string }

func (w fakeWeb) Register(exe string) error { return writeFile(w.path, []byte(exe+"\n"), 0o644) }
func (w fakeWeb) Unregister() error         { return remove(w.path, "") }

type fakeSys struct {
	cmds    []string
	spawned []string
	// active answers systemctl is-active graphical-session.target.
	active bool
	reg    fakeReg
	login  fakeLogin
}

func (f *fakeSys) sys(webManifest string) Sys {
	return Sys{
		Run: func(_ context.Context, args ...string) error {
			f.cmds = append(f.cmds, strings.Join(args, " "))
			if slices.Contains(args, "is-active") && !f.active {
				return errors.New("inactive")
			}
			if len(args) > 1 && args[0] == "launchctl" && args[1] == "kickstart" && !slices.Contains(args, "-k") {
				return errors.New("no such job")
			}
			return nil
		},
		Spawn:  func(args ...string) error { f.spawned = append(f.spawned, strings.Join(args, " ")); return nil },
		Reg:    &f.reg,
		Login:  &f.login,
		Web:    fakeWeb{webManifest},
		Portal: func() (bool, error) { return false, nil },
	}
}

// tree lists every file and link under root with its content or target and mode; directories
// only when empty.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, _ := d.Info()
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			out = append(out, rel+" -> "+strings.ReplaceAll(l, root, "~"))
		case d.IsDir():
			if ents, _ := os.ReadDir(p); len(ents) == 0 && p != root {
				out = append(out, rel+"/")
			}
		default:
			b, _ := os.ReadFile(p)
			out = append(out, rel+" "+info.Mode().Perm().String()+" "+strings.ReplaceAll(string(b), root, "~"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLinuxInstallTwiceUninstallLeavesNothing applies the real plan to a scratch HOME.
func TestLinuxInstallTwiceUninstallLeavesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks and Unix modes")
	}
	home := t.TempDir()
	exe := filepath.Join(home, "Downloads", "jarvis-agent")
	if err := writeFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The user's own things, which must survive: other default apps, a Firefox profile dir.
	mime := filepath.Join(home, ".config", "mimeapps.list")
	if err := writeFile(mime, []byte("[Default Applications]\ntext/html=firefox.desktop;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".mozilla", "Desktop"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	before := tree(t, home)
	e := Env{GOOS: "linux", Home: home, Exe: exe, Getenv: func(string) string { return "" }}
	var f fakeSys
	web := filepath.Join(home, ".mozilla", "native-messaging-hosts", "io.jarvis.agent.browser.json")
	o := Options{Browser: true, DesktopShortcut: true, Start: true}
	ops, err := Plan(e, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), ops, f.sys(web), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	once := tree(t, home)
	// Twice: same files, nothing duplicated (mimeapps.list keeps one line).
	if err := Apply(context.Background(), ops, f.sys(web), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if twice := tree(t, home); !slices.Equal(once, twice) {
		t.Fatalf("second install changed things:\n%s\n---\n%s", strings.Join(once, "\n"), strings.Join(twice, "\n"))
	}
	all := strings.Join(once, "\n")
	for _, want := range []string{
		".local/lib/jarvis-agent/jarvis-agent -rwxr-xr-x #!/bin/sh",
		".config/systemd/user/graphical-session.target.wants/jarvis-agent.service -> ~/.config/systemd/user/jarvis-agent.service",
		"ExecStart=~/.local/lib/jarvis-agent/jarvis-agent run",
		".config/mimeapps.list -rw-r--r-- [Default Applications]\ntext/html=firefox.desktop;\nx-scheme-handler/jarvis-agent=net.jarvisautomation.agent.desktop;\n",
		"Desktop/net.jarvisautomation.agent.desktop -rwxr-xr-x",
		".local/share/applications/net.jarvisautomation.agent.desktop -rw-r--r--",
		".config/autostart/net.jarvisautomation.agent.desktop -rw-r--r--",
		".mozilla/native-messaging-hosts/io.jarvis.agent.browser.json -rw-r--r-- ~/.local/lib/jarvis-agent/jarvis-agent",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	// Not in a graphical session's systemd: spawned from this session, twice (one per install).
	if len(f.spawned) != 2 || f.spawned[0] != filepath.Join(home, ".local/lib/jarvis-agent/jarvis-agent")+" run" {
		t.Fatalf("spawned %q", f.spawned)
	}

	un, _ := UninstallPlan(e, UninstallOptions{})
	if err := Apply(context.Background(), un, f.sys(web), ApplyOptions{BestEffort: true}); err != nil {
		t.Fatal(err)
	}
	after := tree(t, home)
	// Nothing of ours is left: no file or link that wasn't there before. Shared directories
	// (applications/, autostart/, systemd/user/) may stay behind, empty.
	var extra []string
	for _, l := range after {
		if !slices.Contains(before, l) && !strings.HasSuffix(l, "/") {
			extra = append(extra, l)
		}
	}
	if len(extra) > 0 {
		t.Fatalf("uninstall left:\n%s", strings.Join(extra, "\n"))
	}
	for _, l := range before {
		if strings.HasSuffix(l, "/") {
			if _, err := os.Stat(filepath.Join(home, l)); err != nil {
				t.Errorf("uninstall removed the user's %q", l)
			}
		} else if !slices.Contains(after, l) {
			t.Errorf("uninstall removed the user's %q", l)
		}
	}
	// The agent's own directories do go.
	for _, gone := range []string{".local/lib/jarvis-agent", ".config/systemd/user/graphical-session.target.wants"} {
		if _, err := os.Stat(filepath.Join(home, gone)); !os.IsNotExist(err) {
			t.Errorf("left %s", gone)
		}
	}
}

func TestNoDesktopFolderNoShortcut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	home := t.TempDir()
	exe := filepath.Join(home, "jarvis-agent")
	_ = writeFile(exe, []byte("x"), 0o755)
	e := Env{GOOS: "linux", Home: home, Exe: exe}
	var f fakeSys
	ops, _ := Plan(e, Options{DesktopShortcut: true})
	if err := Apply(context.Background(), ops, f.sys(filepath.Join(home, "m.json")), ApplyOptions{FilesOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "Desktop")); !os.IsNotExist(err) {
		t.Fatal("created a Desktop folder")
	}
}

func TestLinuxStartsThroughSystemdInAGraphicalSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	home := t.TempDir()
	exe := filepath.Join(home, "jarvis-agent")
	_ = writeFile(exe, []byte("x"), 0o755)
	e := Env{GOOS: "linux", Home: home, Exe: exe}
	f := fakeSys{active: true}
	ops, _ := Plan(e, Options{Start: true})
	if err := Apply(context.Background(), ops, f.sys(filepath.Join(home, "m.json")), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.spawned) != 0 || !slices.Contains(f.cmds, "systemctl --user restart jarvis-agent.service") {
		t.Fatalf("cmds %q spawned %q", f.cmds, f.spawned)
	}
	// start: systemd starts it; at login it already did.
	f.cmds = nil
	if err := Start(context.Background(), e, f.sys(""), true); err != nil || slices.Contains(f.cmds, "systemctl --user start jarvis-agent.service") {
		t.Fatal(err, f.cmds)
	}
	if err := Start(context.Background(), e, f.sys(""), false); err != nil || !slices.Contains(f.cmds, "systemctl --user start jarvis-agent.service") {
		t.Fatal(err, f.cmds)
	}
	// Outside one (no systemd, plain compositor): this session's own process.
	f.active = false
	if err := Start(context.Background(), e, f.sys(""), true); err != nil || len(f.spawned) != 1 {
		t.Fatal(err, f.spawned)
	}
}

func TestFilesOnlyTouchesNoSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	home := t.TempDir()
	exe := filepath.Join(home, "jarvis-agent")
	_ = writeFile(exe, []byte("x"), 0o755)
	e := Env{GOOS: "linux", Home: home, Exe: exe}
	var f fakeSys
	ops, _ := Plan(e, Options{Start: true, Browser: true})
	if err := Apply(context.Background(), ops, f.sys(filepath.Join(home, "m.json")), ApplyOptions{FilesOnly: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.cmds) != 0 || len(f.spawned) != 0 {
		t.Fatalf("files-only ran %q, spawned %q", f.cmds, f.spawned)
	}
}

func TestWindowsInstallTwiceUninstallLeavesNothing(t *testing.T) {
	home := t.TempDir()
	e := Env{GOOS: "windows", Home: home, Exe: `C:\Users\u\AppData\Local\Programs\Jarvis Agent\jarvis-agent.exe`,
		Getenv: func(k string) string {
			return map[string]string{"APPDATA": filepath.Join(home, "roaming"), "LOCALAPPDATA": filepath.Join(home, "local")}[k]
		}}
	var f fakeSys
	f.reg.keys = map[string]map[string]string{RunKey: {"OneDrive": `"C:\od.exe" /background`}}
	web := filepath.Join(home, "roaming", "jarvis-agent", "browser", "native-host-firefox.json")
	ops, _ := Plan(e, Options{Browser: true, DesktopShortcut: true, Start: true})
	for range 2 {
		if err := Apply(context.Background(), ops, f.sys(web), ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got := f.reg.dump()
	exe := `"C:\Users\u\AppData\Local\Programs\Jarvis Agent\jarvis-agent.exe"`
	want := []string{
		RunKey + `|Jarvis Automation Agent=` + exe + ` run`,
		RunKey + `|OneDrive="C:\od.exe" /background`,
		URLKey + `|=URL:Jarvis Automation Agent`,
		URLKey + `|URL Protocol=`,
		URLKey + `\DefaultIcon|=` + exe + `,0`,
		URLKey + `\shell\open\command|=` + exe + ` start "%1"`,
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("registry:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var lnk int
	for _, c := range f.cmds {
		if strings.Contains(c, "CreateShortcut") {
			lnk++
			if !strings.Contains(c, "$l.Arguments = 'start'") || !strings.Contains(c, "Jarvis Automation Agent.lnk") {
				t.Errorf("shortcut script: %s", c)
			}
		}
	}
	if lnk != 4 { // Programs + Desktop, twice
		t.Errorf("%d shortcut scripts", lnk)
	}
	un, _ := UninstallPlan(e, UninstallOptions{Purge: true})
	if err := Apply(context.Background(), un, f.sys(web), ApplyOptions{BestEffort: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.reg.dump(); !slices.Equal(got, []string{RunKey + `|OneDrive="C:\od.exe" /background`}) {
		t.Fatalf("uninstall left %q", got)
	}
	if runtime.GOOS == "windows" { // the plan's paths are Windows paths
		if _, err := os.Stat(filepath.Join(home, "roaming", "jarvis-agent")); !os.IsNotExist(err) {
			t.Fatal("purge kept the data dir")
		}
	}
}

func TestDarwinInstallFromTheApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths")
	}
	home := t.TempDir()
	app := filepath.Join(home, "Applications", "Jarvis Automation Agent.app")
	exe := filepath.Join(app, "Contents", "MacOS", "jarvis-agent")
	_ = writeFile(exe, []byte("x"), 0o755)
	e := Env{GOOS: "darwin", Home: home, UID: 501, Exe: exe, SMAppService: true}
	var f fakeSys
	ops, _ := Plan(e, Options{Start: true})
	if err := Apply(context.Background(), ops, f.sys(""), ApplyOptions{}); err == nil {
		t.Fatal("registered an app with no LaunchAgent inside")
	}
	_ = writeFile(filepath.Join(app, "Contents", "Library", "LaunchAgents", BundlePlistName), []byte("x"), 0o644)
	// A dev install's plain LaunchAgent and the old app id's are replaced.
	old := filepath.Join(home, "Library", "LaunchAgents", "io.jarvis.agent.plist")
	plain := filepath.Join(home, "Library", "LaunchAgents", "net.jarvisautomation.agent.plist")
	_ = writeFile(old, []byte("x"), 0o644)
	_ = writeFile(plain, []byte("x"), 0o644)
	f = fakeSys{}
	if err := Apply(context.Background(), ops, f.sys(""), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if !f.login.registered[BundlePlistName] {
		t.Fatal("not registered")
	}
	for _, p := range []string{old, plain} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s kept", p)
		}
	}
	for _, want := range []string{"launchctl bootout gui/501/io.jarvis.agent", "launchctl bootout gui/501/net.jarvisautomation.agent",
		lsregister + " -f " + app, "launchctl kickstart -k gui/501/net.jarvisautomation.agent"} {
		if !slices.Contains(f.cmds, want) {
			t.Errorf("didn't run %q (ran %q)", want, f.cmds)
		}
	}
	un, _ := UninstallPlan(e, UninstallOptions{})
	if err := Apply(context.Background(), un, f.sys(""), ApplyOptions{BestEffort: true}); err != nil {
		t.Fatal(err)
	}
	if f.login.registered[BundlePlistName] {
		t.Fatal("still registered")
	}
}
