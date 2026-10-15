package install

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// placeFixture is an installer's payload (src) and a program folder (dst); versions are read
// from the files' contents, as `version` would print them.
func placeFixture(t *testing.T, srcVersion, dstVersion string) (src, dst string) {
	t.Helper()
	dir := t.TempDir()
	src = filepath.Join(dir, "tmp", "jarvis-agent.exe")
	dst = filepath.Join(dir, "Programs", "Jarvis Automation Agent", "jarvis-agent.exe")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(srcVersion), 0o755); err != nil {
		t.Fatal(err)
	}
	if dstVersion != "" {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(dstVersion), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return src, dst
}

func fileVersion(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if string(b) == "broken" {
		return "", errors.New("exit status 0xc000007b")
	}
	return string(b), nil
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPlace(t *testing.T) {
	for _, tc := range []struct {
		name, src, dst string
		want           string // the version at dst afterwards
		prev           string // jarvis-agent.prev.exe afterwards ("" = none)
		stopped        bool
		say            string
	}{
		{name: "fresh", src: "v0.2.0", want: "v0.2.0", say: "installed jarvis-agent v0.2.0"},
		{name: "same version", src: "v0.2.0", dst: "v0.2.0", want: "v0.2.0", say: "already"},
		{name: "older installed", src: "v0.2.0", dst: "v0.1.0", want: "v0.2.0", prev: "v0.1.0", stopped: true, say: "replacing"},
		{name: "prerelease before its release", src: "v0.2.0", dst: "v0.2.0-rc1", want: "v0.2.0", prev: "v0.2.0-rc1", stopped: true},
		// The agent updated itself past this installer: an older installer keeps it.
		{name: "self-updated newer", src: "v0.2.0", dst: "v0.3.0", want: "v0.3.0", say: "keeping jarvis-agent v0.3.0"},
		{name: "release over its prerelease installer", src: "v0.3.0-rc1", dst: "v0.3.0", want: "v0.3.0", say: "keeping"},
		// A development build can't be ordered: the installer's build replaces it.
		{name: "dev build installed", src: "v0.2.0", dst: "dev", want: "v0.2.0", prev: "dev", stopped: true},
		// One that doesn't run (a half-written file) is repaired.
		{name: "broken installed", src: "v0.2.0", dst: "broken", want: "v0.2.0", prev: "broken", stopped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, dst := placeFixture(t, tc.src, tc.dst)
			stopped := false
			var out strings.Builder
			if err := Place(src, dst, tc.src, fileVersion, func() { stopped = true }, &out); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, dst); got != tc.want {
				t.Errorf("dst holds %q, want %q", got, tc.want)
			}
			prev := PrevPath(dst)
			if tc.prev == "" {
				if _, err := os.Stat(prev); err == nil {
					t.Errorf("a previous version was kept: %s", prev)
				}
			} else if got := readFile(t, prev); got != tc.prev {
				t.Errorf("previous version %q, want %q", got, tc.prev)
			}
			if stopped != tc.stopped {
				t.Errorf("stopped the running agent: %v, want %v", stopped, tc.stopped)
			}
			if !strings.Contains(out.String(), tc.say) {
				t.Errorf("output %q doesn't say %q", out.String(), tc.say)
			}
		})
	}
}

// A previous version left by an earlier update or install makes way for the new one.
func TestPlaceReplacesAnOldPrevious(t *testing.T) {
	src, dst := placeFixture(t, "v0.3.0", "v0.2.0")
	if err := os.WriteFile(PrevPath(dst), []byte("v0.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Place(src, dst, "v0.3.0", fileVersion, func() {}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, PrevPath(dst)); got != "v0.2.0" {
		t.Fatalf("previous version %q, want v0.2.0", got)
	}
}

func TestPrevPath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\P\jarvis-agent.exe`: `C:\P\jarvis-agent.prev.exe`,
		`C:\P\JARVIS-AGENT.EXE`: `C:\P\JARVIS-AGENT.prev.exe`,
		"/opt/jarvis-agent":     "/opt/jarvis-agent.prev",
	} {
		if got := PrevPath(in); got != want {
			t.Errorf("PrevPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecordVersion(t *testing.T) {
	var r fakeReg
	// No installer entry (a dev build, the archive): nothing is created.
	if ok, err := RecordVersion(&r, "v0.3.0"); ok || err != nil {
		t.Fatalf("RecordVersion without an entry = %v, %v", ok, err)
	}
	if len(r.keys) != 0 {
		t.Fatalf("created %v", r.keys)
	}
	if ok, err := RecordVersion(nil, "v0.3.0"); ok || err != nil {
		t.Fatalf("RecordVersion off Windows = %v, %v", ok, err)
	}
	_ = r.SetString(UninstallKey, "DisplayName", "Jarvis Automation Agent")
	_ = r.SetString(UninstallKey, "DisplayVersion", "0.2.0")
	if ok, err := RecordVersion(&r, "v0.3.0-rc1"); !ok || err != nil {
		t.Fatalf("RecordVersion = %v, %v", ok, err)
	}
	if v, _, _ := r.GetString(UninstallKey, "DisplayVersion"); v != "0.3.0-rc1" {
		t.Fatalf("DisplayVersion = %q", v)
	}
}

// The Windows installer's AppId names the uninstall entry the self-update rewrites, and its
// launcher's name is the one install looks for.
func TestWindowsInstallerAgrees(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "packaging", "windows", "agent.iss"))
	if err != nil {
		t.Fatal(err)
	}
	iss := string(b)
	for _, want := range []string{`#define AppId "` + appid.ID + `"`, `\` + LauncherName + `"`, `install --bin`} {
		if !strings.Contains(iss, want) {
			t.Errorf("agent.iss doesn't have %q", want)
		}
	}
	if !strings.HasSuffix(UninstallKey, `\`+appid.ID+`_is1`) {
		t.Errorf("UninstallKey %q", UninstallKey)
	}
}
