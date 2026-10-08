package service

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may have turned the golden file's newlines into CRLF.
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); string(got) != w {
		t.Errorf("%s differs from the golden file (run go test -update):\n%s", name, got)
	}
}

func TestRenderSystemdGolden(t *testing.T) {
	sys, err := RenderSystemd(Unit{Binary: "/usr/local/bin/jarvisd", Home: "/var/lib/jarvisd"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "jarvisd.service", sys)
	usr, err := RenderSystemd(Unit{Binary: "/home/alex/.local/bin/jarvisd", Home: "/home/alex/.jarvisd", User: true})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "jarvisd-user.service", usr)
}

func TestRenderLaunchdGolden(t *testing.T) {
	p, err := RenderLaunchd(Plist{Binary: "/usr/local/bin/jarvisd", Home: "/Users/alex/.jarvisd", UserName: "alex", UserHome: "/Users/alex"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, LaunchdLabel+".plist", p)
}

// The updater LaunchDaemon (ID11): root (no UserName), its log and working directory outside
// the user-writable home, the system PATH, woken by the request queue.
func TestRenderLaunchdHelperGolden(t *testing.T) {
	p, err := RenderLaunchdHelper(HelperPlist{Binary: "/usr/local/bin/jarvisd", Home: "/Users/alex/.jarvisd", UserName: "alex"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, HelperLabel+".plist", p)
	for _, bad := range []string{"<key>UserName</key>", "/Users/alex/.jarvisd/logs", "<key>KeepAlive</key>"} {
		if strings.Contains(string(p), bad) {
			t.Errorf("helper plist has %s", bad)
		}
	}
	// Odd characters in the home are escaped, not markup.
	q, _ := RenderLaunchdHelper(HelperPlist{Binary: "/b", Home: "/Users/a&b/<x>", UserName: "a"})
	if !strings.Contains(string(q), "<string>/Users/a&amp;b/&lt;x&gt;/updates/requests</string>") {
		t.Errorf("escaping:\n%s", q)
	}
}

// The Windows updater's security descriptor: the SCM defaults plus query+start (only) for
// jarvisd's service SID.
func TestHelperSDDL(t *testing.T) {
	const sid = "S-1-5-80-1-2-3-4-5"
	got := HelperSDDL(sid)
	if !strings.HasSuffix(got, "(A;;CCLCRP;;;"+sid+")") {
		t.Fatal(got)
	}
	// Nothing but SYSTEM and Administrators may stop (WP), reconfigure (DC), delete (SD) or
	// change permissions (WD/WO).
	for _, ace := range strings.Split(strings.TrimPrefix(got, "D:"), ")(") {
		ace = strings.Trim(ace, "()")
		f := strings.Split(ace, ";")
		trustee, rights := f[5], f[2]
		if trustee == "SY" || trustee == "BA" {
			continue
		}
		for _, r := range []string{"WP", "DC", "SD", "WD", "WO"} {
			if strings.Contains(rights, r) {
				t.Errorf("%s gets %s: %s", trustee, r, ace)
			}
		}
	}
}

// Odd paths survive the round trip through the definition, which is how the CLI finds the
// service's home.
func TestRenderQuotingRoundTrip(t *testing.T) {
	home := `/srv/my "jarvis" data\dir`
	u, err := RenderSystemd(Unit{Binary: "/opt/jar vis/jarvisd", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(u), "\n") {
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			args := splitUnitArgs(v)
			if !slices.Equal(args, []string{"/opt/jar vis/jarvisd", "serve", "--home", home}) {
				t.Fatalf("args %q", args)
			}
		}
	}
	p, err := RenderLaunchd(Plist{Binary: "/b<in>", Home: "/Users/a&b/.jarvisd", UserName: "a&b", UserHome: "/Users/a&b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p), "<string>/Users/a&amp;b/.jarvisd</string>") || strings.Contains(string(p), "<in>") {
		t.Fatalf("plist not escaped:\n%s", p)
	}
}

func TestHomeFromArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"jarvisd", "serve", "--home", "/x"}, "/x"},
		{[]string{"jarvisd", "serve", "--home=/y"}, "/y"},
		{[]string{"jarvisd", "serve", "--home"}, ""},
		{[]string{"jarvisd", "serve"}, ""},
	} {
		if got := homeFromArgs(tc.args); got != tc.want {
			t.Errorf("%q: %q", tc.args, got)
		}
	}
}

func TestOpenLogRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", LogFile)
	f, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("small\n"))
	f.Close()
	if f, err = OpenLog(path); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("rotated a small log")
	}
	for i := range 5 {
		os.WriteFile(path, make([]byte, logMaxBytes+1), 0o600)
		f, err := OpenLog(path)
		if err != nil {
			t.Fatal(i, err)
		}
		f.Close()
	}
	for _, n := range []string{".1", ".2", ".3"} {
		if _, err := os.Stat(path + n); err != nil {
			t.Errorf("missing %s", n)
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Error("kept more than 3")
	}
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Error("current log not fresh")
	}
}
