package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeUI answers the dialogs from a script and records them.
type fakeUI struct {
	install, firewall bool
	answers           []string // button labels, in order
	seen              []string // titles
}

func (f *fakeUI) confirmInstall(title, _, _ string) (bool, bool) {
	f.seen = append(f.seen, title)
	return f.install, f.firewall
}

func (f *fakeUI) choose(title, _ string, buttons ...string) int {
	f.seen = append(f.seen, title)
	if len(f.answers) == 0 {
		return len(buttons) - 1
	}
	want := f.answers[0]
	f.answers = f.answers[1:]
	for i, b := range buttons {
		if b == want {
			return i
		}
	}
	return len(buttons) - 1
}

type world struct {
	installed bool
	status    string // JSON from service status
	versions  map[string]string
	link      string
	adminOut  string
	adminErr  error

	scripts []string
	runs    []string
	opened  []string
}

func newApp(w *world, ui *fakeUI) *app {
	return &app{
		bundled:   "/Applications/Jarvis Automation Server.app/Contents/MacOS/jarvisd",
		installed: "/usr/local/bin/jarvisd",
		plist:     "/Library/LaunchDaemons/net.jarvisautomation.jarvisd.plist",
		user:      "alice",
		ui:        ui,
		exists:    func(string) bool { return w.installed },
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			line := name + " " + strings.Join(args, " ")
			w.runs = append(w.runs, line)
			switch {
			case len(args) > 0 && args[0] == "version":
				return []byte(w.versions[name] + "\n"), nil
			case strings.Contains(line, "service status --json"):
				return []byte(w.status), nil
			case strings.Contains(line, "setup-link --url"):
				return []byte(w.link + "\n"), nil
			}
			return nil, nil
		},
		admin: func(_ context.Context, _, script string) (string, error) {
			w.scripts = append(w.scripts, script)
			return w.adminOut, w.adminErr
		},
		open:     func(url string) error { w.opened = append(w.opened, url); return nil },
		dataSize: func(string) int64 { return 3_200_000_000 },
		log:      io.Discard,
	}
}

const bundled = `'/Applications/Jarvis Automation Server.app/Contents/MacOS/jarvisd'`

func TestFirstOpenInstallsAndOpensTheSetupLink(t *testing.T) {
	for _, tc := range []struct {
		firewall bool
		want     string
	}{
		{true, bundled + " install-from-app --run-as alice"},
		{false, bundled + " install-from-app --run-as alice --no-firewall"},
	} {
		w := &world{adminOut: "http://192.168.1.5:7710/setup#token=abc\n"}
		ui := &fakeUI{install: true, firewall: tc.firewall}
		if err := newApp(w, ui).main(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(w.scripts) != 1 || w.scripts[0] != tc.want {
			t.Fatalf("root ran %q, want %q", w.scripts, tc.want)
		}
		if len(w.opened) != 1 || w.opened[0] != "http://192.168.1.5:7710/setup#token=abc" {
			t.Fatalf("opened %q", w.opened)
		}
	}
}

func TestFirstOpenCanceled(t *testing.T) {
	w := &world{}
	if err := newApp(w, &fakeUI{install: false}).main(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.scripts) != 0 || len(w.opened) != 0 {
		t.Fatalf("ran %q, opened %q", w.scripts, w.opened)
	}
	// The password dialog closed: nothing opens, no error dialog.
	w = &world{adminErr: errCanceled}
	ui := &fakeUI{install: true, firewall: true}
	if err := newApp(w, ui).main(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.opened) != 0 || len(ui.seen) != 1 {
		t.Fatalf("opened %q, dialogs %q", w.opened, ui.seen)
	}
}

func TestInstallFailureIsShown(t *testing.T) {
	w := &world{adminErr: errors.New("port 7700 is in use by another program")}
	ui := &fakeUI{install: true, firewall: true}
	err := newApp(w, ui).main(context.Background())
	if err == nil || !strings.Contains(err.Error(), "port 7700") {
		t.Fatalf("err = %v", err)
	}
	if len(ui.seen) != 2 || ui.seen[1] != productName+" wasn't installed" {
		t.Fatalf("dialogs %q", ui.seen)
	}
}

func TestLaterOpenOpensTheAdmin(t *testing.T) {
	w := &world{
		installed: true,
		status:    `{"installed":true,"running":true,"healthy":true,"home":"/Users/alice/.jarvisd"}`,
		versions:  map[string]string{"/usr/local/bin/jarvisd": "v0.2.0"},
		link:      "http://192.168.1.5:7710/",
	}
	w.versions[newApp(w, nil).bundled] = "v0.1.0" // an older app than the self-updated server
	ui := &fakeUI{}
	if err := newApp(w, ui).main(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ui.seen) != 0 || len(w.scripts) != 0 {
		t.Fatalf("dialogs %q, root ran %q", ui.seen, w.scripts)
	}
	if len(w.opened) != 1 || w.opened[0] != "http://192.168.1.5:7710/" {
		t.Fatalf("opened %q", w.opened)
	}
	if !contains(w.runs, "/usr/local/bin/jarvisd --home /Users/alice/.jarvisd setup-link --url") {
		t.Fatalf("runs %q", w.runs)
	}
}

func TestNewerAppOffersAnUpdate(t *testing.T) {
	w := &world{
		installed: true,
		status:    `{"installed":true,"running":true,"healthy":true,"home":"/Users/alice/.jarvisd"}`,
		versions:  map[string]string{"/usr/local/bin/jarvisd": "v0.1.0"},
		adminOut:  "http://192.168.1.5:7710/\n",
	}
	w.versions[newApp(w, nil).bundled] = "v0.2.0"
	ui := &fakeUI{answers: []string{btnUpdate}}
	if err := newApp(w, ui).main(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := bundled + " install-from-app --run-as alice --no-firewall"; len(w.scripts) != 1 || w.scripts[0] != want {
		t.Fatalf("root ran %q, want %q", w.scripts, want)
	}
	if len(w.opened) != 1 {
		t.Fatalf("opened %q", w.opened)
	}
}

func TestStoppedOffersStart(t *testing.T) {
	w := &world{
		installed: true,
		status:    `{"installed":true,"running":false,"healthy":false,"home":"/Users/alice/.jarvisd"}`,
		link:      "http://192.168.1.5:7710/",
	}
	ui := &fakeUI{answers: []string{btnStart}}
	if err := newApp(w, ui).main(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.scripts) != 1 || w.scripts[0] != "/usr/local/bin/jarvisd service start" {
		t.Fatalf("root ran %q", w.scripts)
	}
	if !contains(w.runs, "/usr/local/bin/jarvisd service status --wait 1m") || len(w.opened) != 1 {
		t.Fatalf("runs %q, opened %q", w.runs, w.opened)
	}
}

func TestUninstall(t *testing.T) {
	for _, tc := range []struct {
		data  string
		purge bool
	}{{btnKeepData, false}, {btnDropData, true}} {
		w := &world{
			installed: true,
			status:    `{"installed":true,"running":true,"healthy":true,"home":"/Users/alice/.jarvisd"}`,
		}
		ui := &fakeUI{answers: []string{btnUninstall, btnRemove, tc.data, btnOK}}
		a := newApp(w, ui)
		a.manage = true
		if err := a.main(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(w.scripts) != 1 {
			t.Fatalf("root ran %q", w.scripts)
		}
		s := w.scripts[0]
		if !strings.HasPrefix(s, "/usr/local/bin/jarvisd service uninstall") ||
			strings.Contains(s, "--purge --yes") != tc.purge ||
			!strings.Contains(s, "rm -f /usr/local/bin/jarvisd /usr/local/bin/jarvisd.prev") {
			t.Fatalf("root ran %q", s)
		}
		if ui.seen[len(ui.seen)-1] != productName+" is uninstalled" || len(w.opened) != 0 {
			t.Fatalf("dialogs %q, opened %q", ui.seen, w.opened)
		}
	}
	// Canceling the password dialog says nothing was uninstalled.
	w := &world{installed: true, status: `{"running":true,"home":"/h"}`, adminErr: errCanceled}
	ui := &fakeUI{answers: []string{btnUninstall, btnRemove, btnKeepData}}
	a := newApp(w, ui)
	a.manage = true
	if err := a.main(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ui.seen[len(ui.seen)-1] == productName+" is uninstalled" {
		t.Fatalf("dialogs %q", ui.seen)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin("/Applications/A B.app/x", "--run-as", "o'brien", "", "a=b")
	want := `'/Applications/A B.app/x' --run-as 'o'\''brien' '' a=b`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestAdminScript(t *testing.T) {
	got := adminScript(`say "hi"`, `'/A B/jarvisd' x \n`)
	want := `do shell script "'/A B/jarvisd' x \\n" with prompt "say \"hi\"" with administrator privileges without altering line endings`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestAdminError(t *testing.T) {
	if err := adminError("0:120: execution error: User canceled. (-128)\n", errors.New("exit 1")); !errors.Is(err, errCanceled) {
		t.Fatalf("cancel: %v", err)
	}
	err := adminError("0:120: execution error: install-from-app: port 7700 is held\nby something (1)\n", errors.New("exit 1"))
	if err == nil || err.Error() != "install-from-app: port 7700 is held\nby something" {
		t.Fatalf("got %v", err)
	}
	if err := adminError("weird", errors.New("exit 1")); err == nil || err.Error() != "exit 1: weird" {
		t.Fatalf("got %v", err)
	}
}

func TestNewer(t *testing.T) {
	if !newer("v0.2.0", "v0.1.0") || newer("v0.1.0", "v0.1.0") || newer("v0.1.0", "v0.2.0") || newer("dev", "v0.1.0") {
		t.Fatal("newer is wrong")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
