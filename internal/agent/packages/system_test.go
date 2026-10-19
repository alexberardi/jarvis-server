package packages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// sudoCall is one command the Pi profile ran through sudo, with its stdin.
type sudoCall struct {
	argv  []string
	stdin map[string]any
	// What was true when it ran: the package's files in place, and uv calls so far.
	installed bool
	uvCalls   int
}

// fakeSudo records the wrapper calls; fail makes the calls whose argv contains the key fail.
type fakeSudo struct {
	mu    sync.Mutex
	m     *Manager
	fr    *fakeRunner
	calls []sudoCall
	fail  map[string]string
}

func (f *fakeSudo) run(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := sudoCall{argv: append([]string{name}, args...), installed: exists(f.m.dir("music"))}
	f.fr.mu.Lock()
	c.uvCalls = len(f.fr.calls)
	f.fr.mu.Unlock()
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &c.stdin); err != nil {
			return nil, err
		}
	}
	f.calls = append(f.calls, c)
	for k, why := range f.fail {
		if strings.Contains(strings.Join(c.argv, " "), k) {
			return []byte(why), errors.New("exit status 2")
		}
	}
	return nil, nil
}

func (f *fakeSudo) argvs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c.argv, " "))
	}
	return out
}

// newPiManager is newTestManager with the Pi profile's System: the wrappers in a temp sbin,
// systemd's drop-ins in a temp dir, and sudo recorded.
func newPiManager(t *testing.T, r *repos) (*Manager, *fakeRunner, *fakeSidecars, *fakeSudo) {
	t.Helper()
	m, fr, sc := newTestManager(t, r)
	sbin := t.TempDir()
	for _, w := range []string{AptInstallWrapper, AptSourceWrapper, PostInstallWrapper} {
		if err := os.WriteFile(filepath.Join(sbin, w), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeSudo{m: m, fr: fr}
	m.System = &System{Run: fs.run, User: "pi", UID: 1000, Home: "/home/pi", Sbin: sbin,
		DropinRoot: t.TempDir(), Log: quiet}
	return m, fr, sc, fs
}

// musicAssistant is alexberardi/jarvis-cmd-music-assistant's manifest (shairport-sync through
// Pulse as the node's user), with an apt source added.
func musicAssistant(version string) map[string]string {
	return map[string]string{
		"jarvis_package.yaml": `name: "music"
display_name: "Music Assistant"
version: "` + version + `"
platforms: ["darwin", "linux"]
components:
  - type: command
    name: music
    path: commands/music/command.py
packages:
  - name: music-assistant-client
    version: ">=1.3.0"
apt_sources:
  - name: raspotify
    key_url: https://dtcooper.github.io/raspotify/key.asc
    repo: "deb https://dtcooper.github.io/raspotify raspotify main"
apt_packages:
  - shairport-sync
post_install:
  - type: set_config_file_value
    file: /etc/shairport-sync.conf
    format: libconfig
    section: general
    key: output_backend
    value: pa
  - type: configure_systemd_service
    service: shairport-sync
    run_as: jarvis_user
    group: audio
    environment:
      XDG_RUNTIME_DIR: "/run/user/${UID}"
      PULSE_RUNTIME_PATH: "/run/user/${UID}/pulse"
      MUSIC_HOME: "${HOME}/music as ${USER}"
    wants:
      - "user@${UID}.service"
    after:
      - "user@${UID}.service"
    restart: on-failure
    restart_sec: 5
`,
		"commands/music/command.py":  "# " + version,
		"commands/music/__init__.py": "",
	}
}

// The ask (#174): the Pi profile runs a package's apt sources, apt packages and post_install
// ops through the root-owned wrappers, as the Python node does: sources, then apt, then pip,
// then the ops once the files are in place, before the commands are loaded.
func TestPiInstallRunsSystemSetup(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "alexberardi/jarvis-cmd-music-assistant", "HEAD", musicAssistant("1.2.0"))
	m, _, sc, fs := newPiManager(t, r)

	if _, err := m.Install(context.Background(), repo, ""); err != nil {
		t.Fatal(err)
	}
	w := func(name string) string { return filepath.Join(m.System.Sbin, name) }
	want := []string{
		"sudo -n " + w(AptSourceWrapper) + " raspotify https://dtcooper.github.io/raspotify/key.asc deb https://dtcooper.github.io/raspotify raspotify main",
		"sudo -n " + w(AptInstallWrapper) + " shairport-sync",
		"sudo -n " + w(PostInstallWrapper) + " --package music set-config-file-value",
		"sudo -n " + w(PostInstallWrapper) + " --package music configure-systemd-service",
	}
	if got := fs.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sudo calls:\n%s", strings.Join(got, "\n"))
	}
	// apt before pip (uv's venv + pip are the first two uv calls); the ops after the files.
	if fs.calls[1].uvCalls != 0 || fs.calls[2].uvCalls != 2 || fs.calls[1].installed || !fs.calls[2].installed {
		t.Fatalf("order: %+v", fs.calls)
	}
	if got := fs.calls[2].stdin; !reflect.DeepEqual(got, map[string]any{"file": "/etc/shairport-sync.conf",
		"format": "libconfig", "section": "general", "key": "output_backend", "value": "pa"}) {
		t.Fatalf("set-config-file-value stdin = %v", got)
	}
	got, _ := json.Marshal(fs.calls[3].stdin)
	if string(got) != `{"after":["user@1000.service"],"environment":{"MUSIC_HOME":"/home/pi/music as pi","PULSE_RUNTIME_PATH":"/run/user/1000/pulse",`+
		`"XDG_RUNTIME_DIR":"/run/user/1000"},"group":"audio","restart":"on-failure","restart_sec":5,"run_as":"pi","service":"shairport-sync","wants":["user@1000.service"]}` {
		t.Fatalf("configure-systemd-service stdin = %s", got)
	}
	if got := sc.livePackages(); !reflect.DeepEqual(got, []string{"music"}) {
		t.Fatalf("in service: %v", got)
	}
}

// A failing op aborts the install, as on the Python node: the package isn't installed, and the
// drop-ins the earlier ops wrote are removed.
func TestPiPostInstallFailureRollsBack(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/music", "HEAD", musicAssistant("1.2.0"))
	m, _, sc, fs := newPiManager(t, r)
	fs.fail = map[string]string{"configure-systemd-service": "jarvis-post-install: invalid service name"}

	_, err := m.Install(context.Background(), repo, "")
	if err == nil || !strings.Contains(err.Error(), "post_install[1] (configure_systemd_service) failed: jarvis-post-install: invalid service name") {
		t.Fatalf("err = %v", err)
	}
	if exists(m.dir("music")) || len(m.List()) != 0 || len(sc.started) != 0 {
		t.Fatalf("the package stayed: %v", m.List())
	}
	if got := fs.argvs(); got[len(got)-1] != "sudo -n "+filepath.Join(m.System.Sbin, PostInstallWrapper)+" --package music remove-managed-dropins" {
		t.Fatalf("sudo calls:\n%s", strings.Join(got, "\n"))
	}

	// A failing apt install stops it before pip.
	fs.fail = map[string]string{AptInstallWrapper: "E: Unable to locate package shairport-sync"}
	fs.calls = nil
	_, err = m.Install(context.Background(), repo, "")
	if err == nil || !strings.Contains(err.Error(), "apt install failed: E: Unable to locate package shairport-sync") {
		t.Fatalf("err = %v", err)
	}
	if len(fs.calls) != 2 || exists(m.dir("music")) {
		t.Fatalf("sudo calls: %v", fs.argvs())
	}
}

// Without the wrappers (install-pi.sh not rerun) the install says so instead of failing in sudo.
func TestPiInstallNeedsTheWrappers(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/music", "HEAD", musicAssistant("1.2.0"))
	m, _, _, fs := newPiManager(t, r)
	m.System.Sbin = t.TempDir()
	_, err := m.Install(context.Background(), repo, "")
	if err == nil || !strings.Contains(err.Error(), "install-pi.sh") || len(fs.calls) != 0 {
		t.Fatalf("err = %v, calls %v", err, fs.argvs())
	}
}

// Uninstall asks the wrapper to remove the package's drop-ins; revert reruns the previous
// version's ops.
func TestPiUninstallAndRevert(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/music", "HEAD", musicAssistant("1.2.0"))
	r.add(t, "o/music", "v2", musicAssistant("1.3.0"))
	m, _, _, fs := newPiManager(t, r)
	ctx := context.Background()
	if _, err := m.Install(ctx, repo, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, repo, "v2"); err != nil {
		t.Fatal(err)
	}
	fs.calls = nil
	if _, v, err := m.Revert(ctx, "music"); err != nil || v != "1.2.0" {
		t.Fatalf("revert = %q %v", v, err)
	}
	post := "sudo -n " + filepath.Join(m.System.Sbin, PostInstallWrapper) + " --package music "
	if got := fs.argvs(); !reflect.DeepEqual(got, []string{post + "set-config-file-value", post + "configure-systemd-service"}) {
		t.Fatalf("revert's sudo calls:\n%s", strings.Join(got, "\n"))
	}
	fs.calls = nil
	if _, err := m.Uninstall(ctx, "music"); err != nil {
		t.Fatal(err)
	}
	if got := fs.argvs(); !reflect.DeepEqual(got, []string{post + "remove-managed-dropins"}) {
		t.Fatalf("uninstall's sudo calls:\n%s", strings.Join(got, "\n"))
	}
}

// The desktop still refuses system setup; the Pi refuses a package made for computers only.
func TestSystemSetupProfiles(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/music", "HEAD", musicAssistant("1.2.0"))
	m, _, _ := newTestManager(t, r)
	if _, err := m.Install(context.Background(), repo, ""); err == nil || !strings.Contains(err.Error(), "only a Pi node runs") {
		t.Fatalf("desktop: %v", err)
	}
	files := weather("1.0.0")
	files["jarvis_package.yaml"] = strings.Replace(files["jarvis_package.yaml"], "components:", "targets: [desktop]\ncomponents:", 1)
	repo = r.add(t, "o/weather", "HEAD", files)
	pm, _, _, _ := newPiManager(t, r)
	if _, err := pm.Install(context.Background(), repo, ""); err == nil || !strings.Contains(err.Error(), "made for computers") {
		t.Fatalf("pi: %v", err)
	}
	for _, bad := range []string{"name: x\napt_packages: ['shairport-sync; reboot']\n", "name: x\napt_sources: [{name: r}]\n",
		"name: x\npost_install: [{type: run_shell, cmd: reboot}]\n"} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// At boot the Pi profile removes drop-ins whose package is gone (N:services/post_install_dropins.py):
// a factory reset or a hand-deleted package leaves them pointing at nothing.
func TestPiBootHealsOrphanDropins(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/music", "HEAD", musicAssistant("1.2.0"))
	m, _, _, fs := newPiManager(t, r)
	ctx := context.Background()
	if _, err := m.Install(ctx, repo, ""); err != nil {
		t.Fatal(err)
	}
	for svc, body := range map[string]string{
		"shairport-sync": "# managed-by: jarvis-package music\n[Service]\n",
		"mpd":            "# managed-by: jarvis-package audacy\n[Service]\n",
		"spotifyd":       "# a user's own\n[Service]\n",
		"evil":           "# managed-by: jarvis-package ../x\n",
	} {
		d := filepath.Join(m.System.DropinRoot, svc+".service.d")
		_ = os.MkdirAll(d, 0o755)
		if err := os.WriteFile(filepath.Join(d, "jarvis.conf"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs.calls = nil
	if err := m.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fs.argvs(); !reflect.DeepEqual(got, []string{"sudo -n " + filepath.Join(m.System.Sbin, PostInstallWrapper) + " --package audacy remove-managed-dropins"}) {
		t.Fatalf("sudo calls:\n%s", strings.Join(got, "\n"))
	}
}
