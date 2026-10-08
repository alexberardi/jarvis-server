package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The release workflow writes the tag into the install scripts it publishes (A10b): a script
// fetched from a release's own URL installs that release. Without it, a prerelease could only
// be installed with --version (GitHub's releases/latest skips prereleases, and here it pointed
// at a whisper-engines release), and the admin's install command ran the bare script.

// stampLines are the placeholders release.yml rewrites; each script has exactly one.
var stampLines = map[string]string{
	"install.sh":  `RELEASE_VERSION=""`,
	"install.ps1": `$ReleaseVersion = ''`,
}

func TestScriptsCarryAReleaseStamp(t *testing.T) {
	wf, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for f, line := range stampLines {
		b, err := os.ReadFile(filepath.Join("..", "..", "scripts", f))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimRight(l, "\r") == line {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d lines %q, want exactly 1 (release.yml stamps it)", f, n, line)
		}
		if !strings.Contains(string(wf), f) || !strings.Contains(string(wf), stampSedPattern(line)) {
			t.Errorf("release.yml does not stamp %s (want a sed on %q)", f, stampSedPattern(line))
		}
	}
}

// The closing "Manage:" line names an uninstall command that exists: under `curl | sh` there is
// no install.sh to re-run, so it gives the script's URL (A10b).
func TestInstallShManageLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(string(src), "# Under `curl | sh` there is no install.sh")
	if !ok {
		t.Fatal("Manage block not found")
	}
	_, block, _ := strings.Cut(tail, "\n")
	block, _, _ = strings.Cut(block, "\nsay \"Manage:")
	block += "\nsay \"Manage: x | $uninst\"\n"
	// url as in the script (defined far above; TestInstallShUsesItsStamp covers it).
	harness := "url() { echo \"https://github.com/alexberardi/jarvis-server/releases/download/$VERSION/$1\"; }\n" +
		"say() { printf '%s\\n' \"$*\"; }\nVERSION=v0.1.0-rc1 SVC=--user RUN=\n" + block
	file := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(file, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", file).CombinedOutput()
	if want := "Manage: x | sh " + file + " --uninstall --user"; err != nil || strings.TrimSpace(string(out)) != want {
		t.Errorf("from a file: %q %v, want %q", out, err, want)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(harness)
	out, err = cmd.CombinedOutput()
	want := "Manage: x | curl -fsSL https://github.com/alexberardi/jarvis-server/releases/download/v0.1.0-rc1/install.sh | sh -s -- --uninstall --user"
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Errorf("piped: %q %v, want %q", out, err, want)
	}
}

// stampSedPattern is the line as it appears in release.yml's sed expression.
func stampSedPattern(line string) string {
	return "^" + strings.NewReplacer("$", `\$`, `"`, `\"`).Replace(line) + "$"
}

// install.sh asks for the stamped release unless --version or --base-url says otherwise. A fake
// curl records the first URL and fails, so nothing is installed.
func TestInstallShUsesItsStamp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh")
	}
	if _, err := os.Stat("/run/systemd/system"); runtime.GOOS == "linux" && err != nil {
		t.Skip("install.sh refuses to run without systemd")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "urls")
	fake := "#!/bin/sh\nfor a; do u=$a; done\necho \"$u\" >> " + log + "\nexit 22\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	stamped := strings.Replace(string(src), "\n"+stampLines["install.sh"]+"\n", "\nRELEASE_VERSION=\"v9.9.9-rc1\"\n", 1)
	if stamped == string(src) {
		t.Fatal("stamp placeholder not found")
	}
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	plain, withStamp := script("plain.sh", string(src)), script("stamped.sh", stamped)
	const rel = "https://github.com/alexberardi/jarvis-server/releases/"
	for _, c := range []struct {
		name   string
		script string
		args   []string
		want   string
	}{
		{"stamped", withStamp, nil, rel + "download/v9.9.9-rc1/SHA256SUMS"},
		{"stamped, --version wins", withStamp, []string{"--version", "v1.2.3"}, rel + "download/v1.2.3/SHA256SUMS"},
		{"stamped, --base-url wins", withStamp, []string{"--base-url", "http://mirror.invalid/rel"}, "http://mirror.invalid/rel/SHA256SUMS"},
		{"unstamped: latest", plain, nil, rel + "latest/download/SHA256SUMS"},
	} {
		_ = os.Remove(log)
		cmd := exec.Command("sh", append([]string{c.script, "--user"}, c.args...)...)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+dir, "JARVISD_RELEASE_BASE=")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("%s: install.sh succeeded with a failing curl:\n%s", c.name, out)
		}
		got, _ := os.ReadFile(log)
		if first, _, _ := strings.Cut(string(got), "\n"); first != c.want {
			t.Errorf("%s: fetched %q, want %q\n%s", c.name, first, c.want, out)
		}
	}
}

// A10b R1: the documented one-liner (install.sh's header) fails loudly when the download does:
// piped, a 404 handed sh an empty script and the command exited 0 having done nothing. It is
// run here with a fake curl that 404s (exit 22, as curl -f does) and must exit non-zero.
func TestDocumentedOneLinerFailsOnA404(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(l, "#   curl ") {
			line = strings.TrimPrefix(l, "#   ")
			break
		}
	}
	if !strings.Contains(line, "releases/latest/download/install.sh") || strings.Contains(line, "| sh") ||
		!strings.Contains(line, "curl -fsSL") {
		t.Fatalf("documented command %q: want a curl -f download of releases/latest, then sh", line)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\necho 'curl: (22) The requested URL returned error: 404' >&2\nexit 22\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%q exited 0 after a 404:\n%s", line, out)
	}
	// The pipe form this replaced really does exit 0, which is why it is not documented.
	pipe := exec.Command("sh", "-c", "curl -fsSL https://example.invalid/install.sh | sh")
	pipe.Env = cmd.Env
	if err := pipe.Run(); err != nil {
		t.Fatalf("expected the piped form to exit 0 on a 404 (the bug), got %v", err)
	}
}

// A10b R1: engine-build releases are prereleases and never "latest", so releases/latest keeps
// pointing at jarvisd (GitHub falls back to the newest full release even with --latest=false).
func TestEngineReleasesAreNeverLatest(t *testing.T) {
	wf, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "whisper-builds.yml"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range strings.Split(string(wf), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "gh release create") || strings.HasPrefix(l, "gh release edit") {
			n++
			if !strings.Contains(l, "--prerelease") || !strings.Contains(l, "--latest=false") {
				t.Errorf("whisper-builds.yml: %q lacks --prerelease --latest=false", l)
			}
		}
	}
	if n < 2 {
		t.Fatalf("found %d gh release create/edit lines, want the create and the overwrite edit", n)
	}
}
