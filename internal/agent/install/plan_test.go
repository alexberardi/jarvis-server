package install

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// goldenEnvs are fixed machines, one per OS: the plans must not depend on the host.
func goldenEnvs() map[string]Env {
	return map[string]Env{
		"linux": {GOOS: "linux", Home: "/home/u", UID: 1000, Exe: "/home/u/Downloads/jarvis-agent",
			Getenv: func(string) string { return "" }},
		"darwin": {GOOS: "darwin", Home: "/Users/u", UID: 501, SMAppService: true,
			Exe: "/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent"},
		"windows": {GOOS: "windows", Home: `C:\Users\u`, Exe: `C:\Users\u\AppData\Local\Programs\Jarvis Agent\jarvis-agent.exe`,
			Getenv: func(k string) string {
				return map[string]string{"APPDATA": `C:\Users\u\AppData\Roaming`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`}[k]
			}},
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s (go test -update rewrites it):\n%s", name, p, got)
	}
}

func TestGoldenPlans(t *testing.T) {
	for goos, e := range goldenEnvs() {
		ops, err := Plan(e, Options{Browser: true, DesktopShortcut: true, Start: true})
		if err != nil {
			t.Fatal(goos, err)
		}
		golden(t, goos+"-install", Dump(e, ops))
		un, err := UninstallPlan(e, UninstallOptions{Purge: true})
		if err != nil {
			t.Fatal(goos, err)
		}
		golden(t, goos+"-uninstall-purge", Dump(e, un))
	}
}

// The Windows installer puts the windowless launcher beside the agent: the login item,
// shortcuts and URL handler go through it, the start right after install and the browsers'
// host don't (they have no window to hide, or need the console program's pipes).
func TestWindowsLauncherPlan(t *testing.T) {
	e := goldenEnvs()["windows"]
	e.Launcher = `C:\Users\u\AppData\Local\Programs\Jarvis Agent\` + LauncherName
	ops, err := Plan(e, Options{Browser: true, DesktopShortcut: true, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "windows-install-launcher", Dump(e, ops))
}

func TestDarwinLaunchAgentPlan(t *testing.T) {
	e := goldenEnvs()["darwin"]
	e.Exe = "/Users/u/src/jarvis-server/dist/jarvis-agent" // a bare binary: no bundle
	ops, err := Plan(e, Options{Browser: true, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "darwin-install-launchagent", Dump(e, ops))
	if _, err := Plan(e, Options{LoginItem: LoginSMAppService}); err == nil {
		t.Fatal("SMAppService without a bundle")
	}
	e = goldenEnvs()["darwin"]
	e.SMAppService = false // macOS 12
	if ops, err := Plan(e, Options{}); err != nil || !strings.Contains(Dump(e, ops), "LaunchAgents/net.jarvisautomation.agent.plist") {
		t.Fatal("auto on macOS 12 should fall back to a LaunchAgent", err)
	}
	if _, err := Plan(e, Options{LoginItem: "cron"}); err == nil {
		t.Fatal("unknown login item kind accepted")
	}
}

func TestLinuxOptionalPieces(t *testing.T) {
	e := goldenEnvs()["linux"]
	ops, _ := Plan(e, Options{})
	d := Dump(e, ops)
	for _, absent := range []string{"Desktop/", "browser-hosts", "start [activate]"} {
		if strings.Contains(d, absent) {
			t.Errorf("%q in a plan without that option:\n%s", absent, d)
		}
	}
	// Running from the installed copy: nothing to copy.
	e.Exe = LinuxBin(e)
	if ops, _ := Plan(e, Options{}); strings.Contains(Dump(e, ops), "copy-exe") {
		t.Error("copies the binary onto itself")
	}
}

func TestXDGDirsAreHonoured(t *testing.T) {
	e := goldenEnvs()["linux"]
	e.Getenv = func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/data", "XDG_DESKTOP_DIR": "/home/u/Schreibtisch"}[k]
	}
	ops, _ := Plan(e, Options{DesktopShortcut: true})
	d := Dump(e, ops)
	for _, want := range []string{`"/cfg/systemd/user/jarvis-agent.service"`, `"/cfg/autostart/net.jarvisautomation.agent.desktop"`,
		`"/data/applications/net.jarvisautomation.agent.desktop"`, `"~/Schreibtisch/net.jarvisautomation.agent.desktop"`, `"/cfg/mimeapps.list"`} {
		if !strings.Contains(d, want) {
			t.Errorf("no %s in\n%s", want, d)
		}
	}
}

func TestUnsupported(t *testing.T) {
	if _, err := Plan(Env{GOOS: "plan9", Home: "/", Exe: "/x"}, Options{}); err == nil {
		t.Fatal("plan9")
	}
	if _, err := Plan(Env{GOOS: "linux"}, Options{}); err == nil {
		t.Fatal("no exe/home")
	}
}

func TestBundleOf(t *testing.T) {
	for exe, want := range map[string]string{
		"/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent": "/Applications/Jarvis Automation Agent.app",
		"/Users/u/Applications/X.app/Contents/MacOS/jarvis-agent":               "/Users/u/Applications/X.app",
		"/usr/local/bin/jarvis-agent":                                           "",
		"/A.app/Contents/MacOS/sub/jarvis-agent":                                "",
	} {
		if got := BundleOf(exe); got != want {
			t.Errorf("%s: %q", exe, got)
		}
	}
}

func TestDesktopExecQuoting(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.local/lib/jarvis-agent/jarvis-agent": "/home/u/.local/lib/jarvis-agent/jarvis-agent",
		"/home/my user/bin/jarvis-agent":               `"/home/my user/bin/jarvis-agent"`,
		"/home/u/100%/jarvis-agent":                    "/home/u/100%%/jarvis-agent",
		`/home/u/a"b $c/jarvis-agent`:                  `"/home/u/a\\"b \\$c/jarvis-agent"`,
	} {
		if got := desktopExecArg(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	if got := systemdExecArg("/home/my user/x%"); got != `"/home/my user/x%%"` {
		t.Error(got)
	}
}

func TestCheckStartURL(t *testing.T) {
	for _, ok := range []string{"jarvis-agent://start", "jarvis-agent://start/", "jarvis-agent:start", "JARVIS-AGENT://start", " jarvis-agent://start\n"} {
		if err := CheckStartURL(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "start", "jarvis-agent://stop", "jarvis-agent://start?x=1", "jarvis-agent://start#x",
		"jarvis-agent://start/../../x", "jarvis-agent://start/run", "jarvis-agent://user@start", "jarvis-agent://start:80",
		"jarvis-agent:start?x", "jarvis-agent://", "https://start", "jarvis-agent://start?", "jarvis-agent:run",
		"jarvis-agent://START.evil"} {
		if err := CheckStartURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The .deb's system-wide menu entry (scripts/packaging/deb/agent, decided Q5) is the one
// install writes for a per-user copy, for the package's /usr/lib copy: the same Exec (`start`,
// which sets the user up on first launch), URL handler, icon and file name, so a person's own
// entry, once written, replaces it without changing anything they see.
func TestDebMenuEntryIsInstalls(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "packaging", "deb", "agent", appid.DesktopFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := MenuEntry("/usr/lib/jarvis-agent/jarvis-agent"); string(b) != want {
		t.Fatalf("scripts/packaging/deb/agent/%s:\n%s\nwant (install.MenuEntry):\n%s", appid.DesktopFile, b, want)
	}
	e := Env{GOOS: "linux", Home: "/home/u", Exe: "/usr/lib/jarvis-agent/jarvis-agent"}
	if !SetsUpOnFirstLaunch(e) {
		t.Fatal("the package's copy doesn't set a user up on its first launch")
	}
}
