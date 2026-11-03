package packages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The user's report (2026-10-15): installing a Pantry package on the desktop agent timed out,
// because the agent had no package installer. An install now lands the package, installs its
// pip dependencies with the SDK, and serves its commands from a fresh sidecar.
func TestInstallServesThePackage(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "alexberardi/jarvis-cmd-meteo-weather", "HEAD", weather("1.0.0"))
	m, fr, sc := newTestManager(t, r)
	ctx := context.Background()

	details, err := m.Install(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"package_name": "get_weather_meteo", "version": "1.0.0", "components": 2,
		"skipped_components": []string{"agent:weather_context"}}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("details = %v", details)
	}
	// uv made the venv once, then installed the SDK and the manifest's requirements into it.
	if len(fr.calls) != 2 || fr.calls[0][1] != "venv" || fr.calls[0][3] != pythonVersion {
		t.Fatalf("uv calls = %v", fr.calls)
	}
	pip := strings.Join(fr.calls[1], " ")
	if !strings.HasPrefix(pip, "uv pip install --python "+m.Python.Interpreter()+" --quiet "+SDKRequirement+" httpx tzdata==2024.1") {
		t.Fatalf("pip = %s", pip)
	}
	if got := sc.livePackages(); !reflect.DeepEqual(got, []string{"get_weather_meteo"}) {
		t.Fatalf("in service: %v", got)
	}
	list, _ := readList(filepath.Join(m.Root, "commands.json"))
	if len(list) != 1 || list[0].Root != filepath.Join(m.Root, "pkgs", "get_weather_meteo") ||
		!reflect.DeepEqual(list[0].Commands, []string{"commands/get_weather/command.py"}) {
		t.Fatalf("commands.json = %+v", list)
	}
	if rep := m.Report(); !reflect.DeepEqual(rep, []map[string]any{{"name": "get_weather_meteo", "version": "1.0.0", "health": "ok"}}) {
		t.Fatalf("report = %v", rep)
	}
	// Staging leaves nothing behind.
	if e, _ := os.ReadDir(filepath.Join(m.Root, "staging")); len(e) != 0 {
		t.Fatalf("staging = %v", e)
	}

	// A second install reuses the venv: no second "uv venv".
	r.add(t, "alexberardi/jarvis-cmd-meteo-weather", "v2", weather("2.0.0"))
	if _, err := m.Install(ctx, repo, "v2"); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 3 || fr.calls[2][1] != "pip" {
		t.Fatalf("uv calls = %v", fr.calls)
	}
	// The update keeps 1.0.0 for revert, and the app sees it.
	if rep := m.Report(); rep[0]["version"] != "2.0.0" || rep[0]["previous_version"] != "1.0.0" {
		t.Fatalf("report = %v", rep)
	}
	b, _ := os.ReadFile(filepath.Join(m.Root, "pkgs", "get_weather_meteo", "commands", "get_weather", "command.py"))
	if string(b) != "# 2.0.0" {
		t.Fatalf("live command = %q", b)
	}
	// Every change got its own sidecar; the one before was closed.
	if len(sc.started) != 2 || !sc.started[0].closed || sc.started[1].closed {
		t.Fatalf("sidecars = %+v", sc.started)
	}

	// Revert brings 1.0.0 back and drops the snapshot it used.
	name, version, err := m.Revert(ctx, "get_weather_meteo")
	if err != nil || name != "get_weather_meteo" || version != "1.0.0" {
		t.Fatalf("revert = %q %q %v", name, version, err)
	}
	if rep := m.Report(); rep[0]["version"] != "1.0.0" || rep[0]["previous_version"] != nil {
		t.Fatalf("report after revert = %v", rep)
	}
	if _, _, err := m.Revert(ctx, "get_weather_meteo"); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("second revert = %v", err)
	}

	// The app's uninstall names the command; the package goes, and with it the sidecar.
	name, err = m.Uninstall(ctx, "get_weather_meteo")
	if err != nil || name != "get_weather_meteo" {
		t.Fatalf("uninstall = %q %v", name, err)
	}
	if sc.live != nil || len(m.Report()) != 0 {
		t.Fatalf("after uninstall: live %v report %v", sc.live, m.Report())
	}
	if _, err := m.Uninstall(ctx, "get_weather_meteo"); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Fatalf("second uninstall = %v", err)
	}
}

// An update whose commands don't load is undone: the old version is back on disk, the old
// sidecar keeps serving, and commands.json lists what is in service.
func TestInstallThatDoesNotLoadIsUndone(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/weather", "HEAD", weather("1.0.0"))
	r.add(t, "o/weather", "bad", weather("2.0.0"))
	m, _, sc := newTestManager(t, r)
	ctx := context.Background()
	if _, err := m.Install(ctx, repo, ""); err != nil {
		t.Fatal(err)
	}
	inService := sc.live
	sc.fail = map[string]string{"get_weather_meteo": "commands/get_weather/command.py: ImportError: no httpx"}

	_, err := m.Install(ctx, repo, "bad")
	if err == nil || !strings.Contains(err.Error(), "didn't load: commands/get_weather/command.py: ImportError: no httpx") {
		t.Fatalf("err = %v", err)
	}
	if sc.live != inService || inService.(*fakeSidecar).closed || !sc.started[1].closed {
		t.Fatal("a broken update replaced the sidecar in service")
	}
	b, _ := os.ReadFile(filepath.Join(m.Root, "pkgs", "get_weather_meteo", "commands", "get_weather", "command.py"))
	if string(b) != "# 1.0.0" {
		t.Fatalf("live command = %q", b)
	}
	if rep := m.Report(); rep[0]["version"] != "1.0.0" || rep[0]["previous_version"] != nil {
		t.Fatalf("report = %v", rep)
	}
	if list, _ := readList(filepath.Join(m.Root, "commands.json")); len(list) != 1 {
		t.Fatalf("commands.json = %v", list)
	}

	// A first install that doesn't load leaves nothing.
	sc.fail = map[string]string{"other": "boom"}
	r.add(t, "o/other", "HEAD", map[string]string{"command.py": "", "jarvis_command.yaml": "name: other\n"})
	if _, err := m.Install(ctx, r.srv.URL+"/o/other", ""); err == nil {
		t.Fatal("a broken first install succeeded")
	}
	if _, err := os.Stat(filepath.Join(m.Root, "pkgs", "other")); !os.IsNotExist(err) {
		t.Fatalf("other left behind: %v", err)
	}

	// A sidecar that doesn't start at all is the same.
	sc.fail = nil
	sc.startErr = errors.New("python crashed")
	if _, err := m.Install(ctx, r.srv.URL+"/o/other", ""); err == nil || !strings.Contains(err.Error(), "python crashed") {
		t.Fatalf("err = %v", err)
	}
	if len(m.List()) != 1 {
		t.Fatalf("installed = %v", m.List())
	}
}

// What a desktop refuses, before anything is downloaded into place or installed.
func TestInstallRefusals(t *testing.T) {
	r := newRepos(t)
	m, fr, sc := newTestManager(t, r)
	ctx := context.Background()
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"other OS": {map[string]string{"jarvis_package.yaml": "name: mac_only\nplatforms: [darwin]\n", "command.py": ""},
			"supports darwin, not linux"},
		"pi target": {map[string]string{"jarvis_package.yaml": "name: pi_only\ntargets: [node]\n", "command.py": ""},
			"made for Pi nodes"},
		"apt": {map[string]string{"jarvis_package.yaml": "name: needs_apt\napt_packages: [mpv]\n", "command.py": ""},
			"only a Pi node runs"},
		"no commands": {map[string]string{"jarvis_package.yaml": "name: jobs\ncomponents:\n  - {type: agent, name: jobs, path: agents/jobs/agent.py}\n",
			"agents/jobs/agent.py": ""}, "no commands a computer can run"},
		"no manifest": {map[string]string{"README.md": ""}, "not a Jarvis package"},
		"missing file": {map[string]string{"jarvis_package.yaml": "name: gone\ncomponents:\n  - {type: command, name: gone, path: commands/gone/command.py}\n"},
			"commands/gone/command.py is missing"},
		"pip flag": {map[string]string{"jarvis_package.yaml": "name: sneaky\npackages:\n  - name: \"--index-url=https://evil\"\n", "command.py": ""},
			"invalid Python package name"},
	}
	for name, c := range cases {
		repo := r.add(t, "o/"+strings.ReplaceAll(name, " ", "-"), "HEAD", c.files)
		if _, err := m.Install(ctx, repo, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if len(fr.calls) != 0 || len(sc.started) != 0 || len(m.List()) != 0 {
		t.Fatalf("a refused install did something: uv %v, sidecars %d, installed %v", fr.calls, len(sc.started), m.List())
	}

	// A pip failure installs nothing and says why.
	fr.pipFail = errors.New("exit status 1")
	repo := r.add(t, "o/weather", "HEAD", weather("1.0.0"))
	if _, err := m.Install(ctx, repo, ""); err == nil || !strings.Contains(err.Error(), "resolution failed: no such package") {
		t.Fatalf("err = %v", err)
	}
	if len(m.List()) != 0 || len(sc.started) != 0 {
		t.Fatal("a failed pip install left a package")
	}

	// Not a repository URL, or no such archive.
	for _, bad := range []string{"http://github.com/o/r", "https://github.com/o", "file:///etc"} {
		if _, err := m.Install(ctx, bad, ""); err == nil {
			t.Errorf("%s: installed", bad)
		}
	}
	if _, err := m.Install(ctx, r.srv.URL+"/o/missing", ""); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("missing = %v", err)
	}
}

// At start the agent serves what is installed; a package that doesn't load is reported as
// unhealthy, the rest still serve.
func TestBootServesInstalledPackages(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/weather", "HEAD", weather("1.0.0"))
	m, _, _ := newTestManager(t, r)
	ctx := context.Background()
	if err := m.Boot(ctx); err != nil { // nothing installed: no sidecar, no error
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, repo, ""); err != nil {
		t.Fatal(err)
	}

	// A new agent process over the same root.
	sc := &fakeSidecars{fail: map[string]string{"get_weather_meteo": "ImportError"}}
	m2 := &Manager{Root: m.Root, Python: m.Python, HTTP: m.HTTP, GOOS: "linux", Sidecars: sc, Log: quiet}
	if err := m2.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if sc.live == nil || m2.Report()[0]["health"] != "failed" {
		t.Fatalf("boot: live %v report %v", sc.live, m2.Report())
	}

	// Packages but no venv (deleted by hand): say so, serve nothing.
	if err := os.RemoveAll(filepath.Join(m.Root, "venv")); err != nil {
		t.Fatal(err)
	}
	m3 := &Manager{Root: m.Root, Python: m.Python, HTTP: m.HTTP, GOOS: "linux", Sidecars: &fakeSidecars{}, Log: quiet}
	if err := m3.Boot(ctx); err == nil || !strings.Contains(err.Error(), "Python environment is missing") {
		t.Fatalf("boot without venv = %v", err)
	}
}

// The Pi profile: the node's built-in commands are pip-installed into the venv at boot, once
// per pinned requirement, and every sidecar loads them, with or without packages.
func TestBootInstallsAndServesTheBuiltins(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/weather", "HEAD", weather("1.0.0"))
	m, fr, sc := newTestManager(t, r)
	m.Builtins = &Builtins{Requirement: "jarvis-node-setup @ https://x/a.tar.gz", Package: "commands", Skip: []string{"routine_command"}}
	ctx := context.Background()
	if err := m.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 2 || fr.calls[0][1] != "venv" ||
		strings.Join(fr.calls[1], " ") != "uv pip install --python "+m.Python.Interpreter()+" --quiet "+SDKRequirement+" jarvis-node-setup @ https://x/a.tar.gz" {
		t.Fatalf("uv calls = %v", fr.calls)
	}
	if sc.live == nil || len(sc.livePackages()) != 0 {
		t.Fatalf("no sidecar for the built-ins alone: %v", sc.live)
	}
	b, _ := os.ReadFile(filepath.Join(m.Root, "commands.json"))
	var doc struct {
		Builtins struct {
			Package string   `json:"package"`
			Skip    []string `json:"skip"`
		} `json:"builtins"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Builtins.Package != "commands" || !reflect.DeepEqual(doc.Builtins.Skip, []string{"routine_command"}) {
		t.Fatalf("commands.json = %s", b)
	}

	// The next start doesn't reinstall them; a new pin does.
	m2 := &Manager{Root: m.Root, Python: m.Python, HTTP: m.HTTP, GOOS: "linux", Sidecars: &fakeSidecars{}, Log: quiet, Builtins: m.Builtins}
	if err := m2.Boot(ctx); err != nil || len(fr.calls) != 2 {
		t.Fatalf("second boot: %v, uv calls %v", err, fr.calls)
	}
	m2.Builtins = &Builtins{Requirement: "jarvis-node-setup @ https://x/b.tar.gz", Package: "commands"}
	if err := m2.Boot(ctx); err != nil || len(fr.calls) != 3 || !strings.HasSuffix(strings.Join(fr.calls[2], " "), "b.tar.gz") {
		t.Fatalf("new pin: %v, uv calls %v", err, fr.calls)
	}

	// Removing the last package keeps the built-ins in service.
	if _, err := m.Install(ctx, repo, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Uninstall(ctx, "get_weather_meteo"); err != nil {
		t.Fatal(err)
	}
	if sc.live == nil {
		t.Fatal("the built-ins went with the last package")
	}

	// Offline at a new pin: the venv's built-ins still serve.
	fr.pipFail = errors.New("no network")
	m2.Builtins = &Builtins{Requirement: "jarvis-node-setup @ https://x/c.tar.gz", Package: "commands"}
	if err := m2.Boot(ctx); err != nil {
		t.Fatalf("offline boot = %v", err)
	}
	// A fresh node offline: say so.
	_ = os.RemoveAll(filepath.Join(m.Root, "venv"))
	_ = os.Remove(filepath.Join(m.Root, "builtins.txt"))
	if err := m2.Boot(ctx); err == nil || !strings.Contains(err.Error(), "no network") {
		t.Fatalf("offline boot without a venv = %v", err)
	}
}

// govee is a package with a device family (declared without a path: it is at the
// conventional place) and a device manager.
func govee() map[string]string {
	return map[string]string{
		"jarvis_package.yaml": `name: "govee"
version: "1.0.0"
components:
  - type: device_protocol
    name: govee
  - type: device_manager
    name: hub
    path: device_managers/hub/manager.py
`,
		"device_families/govee/protocol.py": "",
		"device_managers/hub/manager.py":    "",
	}
}

// A package's device families and managers are listed for the sidecar and not reported as
// skipped, on a Pi (built-ins and System on) and on a computer alike: a device-only package
// installs on either (the user, 2026-10-26: devices on the desktop agent are mandatory).
func TestDeviceFamiliesOnEveryNode(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/govee", "HEAD", govee())
	pi, _, _, _ := newPiManager(t, r)
	pi.Builtins = &Builtins{Requirement: "jarvis-node-setup @ https://x/a.tar.gz", Package: "commands"}
	desk, _, _ := newTestManager(t, r)
	for name, m := range map[string]*Manager{"pi": pi, "desktop": desk} {
		details, err := m.Install(context.Background(), repo, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		list, _ := readList(filepath.Join(m.Root, "commands.json"))
		if len(list) != 1 || details["skipped_components"] != nil ||
			!reflect.DeepEqual(list[0].Families, []string{"device_families/govee/protocol.py"}) ||
			!reflect.DeepEqual(list[0].Managers, []string{"device_managers/hub/manager.py"}) {
			t.Fatalf("%s: details %v, list %+v", name, details, list)
		}
	}
}

// The user's QA (2026-10-23): comfyui's comfyui_jobs agent was logged skipped on the Go node,
// so its finished videos never reached the inbox. On a Pi a package's agents are listed for
// the sidecar and not skipped; a desktop still skips them. An agent the scheduler reports
// failing (AgentHealth) marks its package failed, as a command that didn't load does.
func TestPiRunsPackageAgents(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/weather", "HEAD", weather("1.0.0"))
	m, _, _, _ := newPiManager(t, r)
	m.Builtins = &Builtins{Requirement: "jarvis-node-setup @ https://x/a.tar.gz", Package: "commands"}
	details, err := m.Install(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := readList(filepath.Join(m.Root, "commands.json"))
	if len(list) != 1 || details["skipped_components"] != nil ||
		!reflect.DeepEqual(list[0].Agents, []string{"agents/weather_context/agent.py"}) {
		t.Fatalf("pi: details %v, list %+v", details, list)
	}
	if h := m.Report()[0]["health"]; h != "ok" {
		t.Fatalf("health = %v", h)
	}
	m.AgentHealth = func() map[string]string { return map[string]string{"get_weather_meteo": "weather_context: boom"} }
	if h := m.Report()[0]["health"]; h != "failed" {
		t.Fatalf("health with a failing agent = %v", h)
	}

	desk, _, _ := newTestManager(t, r)
	details, err = desk.Install(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	list, _ = readList(filepath.Join(desk.Root, "commands.json"))
	if !reflect.DeepEqual(details["skipped_components"], []string{"agent:weather_context"}) || list[0].Agents != nil {
		t.Fatalf("desktop: details %v, list %+v", details, list)
	}
}

// A device component's declared path must stay inside the package.
func TestManifestRefusesADeviceComponentOutside(t *testing.T) {
	_, err := ParseManifest([]byte("name: x\ncomponents:\n  - type: device_protocol\n    name: x\n    path: ../x/protocol.py\n"))
	if err == nil {
		t.Fatal("accepted a path outside the package")
	}
	for _, y := range []string{"path: ../x/agent.py", "path: agents/x/agent.sh"} {
		if _, err := ParseManifest([]byte("name: x\ncomponents:\n  - type: agent\n    name: x\n    " + y + "\n")); err == nil {
			t.Fatalf("accepted an agent at %s", y)
		}
	}
	// Without a path, the conventional one (agents/<name>/agent.py) is checked.
	if _, err := ParseManifest([]byte("name: x\ncomponents:\n  - type: agent\n    name: ../../x\n")); err == nil {
		t.Fatal("accepted an agent named outside the package")
	}
}
