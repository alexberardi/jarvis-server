package packages

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A Forge test install (N:services/test_install_handler.py, test_install_cleanup.py): a draft
// from Pantry's AI Forge, served next to the installed packages for testTTL and then removed.
// It runs no system setup, as on the Python node, and isn't an installed package (the app's
// list leaves it out).

// testTTL is how long a test install is served (N: _TEST_TTL_MINUTES).
const testTTL = 20 * time.Minute

// testPrefix names a test install in commands.json, apart from an installed package.
const testPrefix = "test:"

// testMeta is the file in a test install's directory that says when it goes.
const testMeta = ".test_meta.json"

// DraftFile is one file of a Forge draft, as Pantry serves it.
type DraftFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

type testPkg struct {
	name, dir string
	commands  []string
	expires   time.Time
}

func (m *Manager) testsDir() string { return filepath.Join(m.Root, "tests") }

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// readTests lists the test installs on disk, expired or not.
func (m *Manager) readTests() []testPkg {
	entries, err := os.ReadDir(m.testsDir())
	if err != nil {
		return nil
	}
	var out []testPkg
	for _, e := range entries {
		if !e.IsDir() || !nameRE.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(m.testsDir(), e.Name())
		var meta struct {
			ExpiresAt time.Time `json:"expires_at"`
			Commands  []string  `json:"commands"`
		}
		b, err := os.ReadFile(filepath.Join(dir, testMeta))
		if err != nil || json.Unmarshal(b, &meta) != nil {
			continue // not finished writing, or not ours: removeTests sweeps it
		}
		out = append(out, testPkg{name: e.Name(), dir: dir, commands: meta.Commands, expires: meta.ExpiresAt})
	}
	return out
}

// tests are the test installs still being served.
func (m *Manager) tests() []testPkg {
	var out []testPkg
	for _, tp := range m.readTests() {
		if m.clock().Before(tp.expires) {
			out = append(out, tp)
		}
	}
	return out
}

// removeTests deletes the expired test installs (all of them with all) and anything in tests/
// that isn't one. It reports whether it removed a served one.
func (m *Manager) removeTests(all bool) bool {
	entries, err := os.ReadDir(m.testsDir())
	if err != nil {
		return false
	}
	keep := map[string]bool{}
	if !all {
		for _, tp := range m.tests() {
			keep[tp.name] = true
		}
	}
	served := false
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if e.IsDir() && exists(filepath.Join(m.testsDir(), e.Name(), testMeta)) {
			served = true
		}
		_ = os.RemoveAll(filepath.Join(m.testsDir(), e.Name()))
	}
	return served
}

// ExpireTests removes the test installs whose time is up and restarts the sidecar without
// them.
func (m *Manager) ExpireTests(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.removeTests(false) {
		return
	}
	m.Log.Info("packages: test install expired")
	sc, err := m.start(ctx)
	if err != nil {
		m.Log.Warn("packages: the sidecar didn't restart after a test install expired", "err", err)
		return
	}
	m.commit(sc)
}

// A requirements.txt line uv may get: a requirement, never an option or a URL.
var requirementRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9._,-]+\])?((==|>=|<=|~=|!=|>|<)[0-9A-Za-z.*+!_-]+(,(==|>=|<=|~=|!=|>|<)[0-9A-Za-z.*+!_-]+)*)?$`)

// TestInstall serves a Forge draft as the test command name for testTTL: its files under
// tests/<name>/, its requirements.txt in the venv (best effort, as on the Python node), and a
// sidecar that loads it. A draft whose commands don't load is removed again.
func (m *Manager) TestInstall(ctx context.Context, name string, files []DraftFile) (map[string]any, error) {
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("test install: invalid package name %q", name)
	}
	for _, f := range files {
		if err := checkRelPath(f.Filename); err != nil {
			return nil, fmt.Errorf("test install: %w", err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeTests(false)
	dir := filepath.Join(m.testsDir(), name)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Filename))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(f.Content), 0o644); err != nil {
			return nil, err
		}
	}
	reqs := readRequirements(filepath.Join(dir, "requirements.txt"))
	if err := m.Python.Install(ctx, reqs); err != nil {
		m.Log.Warn("packages: test install: pip failed", "package", name, "err", err)
	}
	commands := draftCommands(dir)
	if len(commands) == 0 {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("%s has no commands", name)
	}
	meta, _ := json.Marshal(map[string]any{"package_name": name, "installed_at": m.clock().UTC(),
		"expires_at": m.clock().Add(testTTL).UTC(), "commands": commands, "pip_requirements": reqs})
	if err := os.WriteFile(filepath.Join(dir, testMeta), meta, 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := m.check(ctx, testPrefix+name); err != nil {
		_ = os.RemoveAll(dir)
		if _, err2 := m.writeList(); err2 != nil {
			m.Log.Warn("packages: couldn't rewrite the commands list", "err", err2)
		}
		return nil, err
	}
	time.AfterFunc(testTTL+time.Second, func() { m.ExpireTests(context.Background()) })
	m.Log.Info("packages: test install", "package", name, "files", len(files), "commands", commands)
	return map[string]any{"package_name": name, "files_installed": len(files), "pip_packages_added": len(reqs)}, nil
}

// readRequirements are requirements.txt's requirements; options, URLs and markers are skipped.
func readRequirements(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if requirementRE.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

// draftCommands are the command files of a draft: its manifest's commands when it has a valid
// one, else every .py file that defines an IJarvisCommand (the Python node imports every module
// in the draft and keeps the commands it finds).
func draftCommands(dir string) []string {
	if man, err := LoadManifest(dir); err == nil {
		var out []string
		for _, c := range man.Commands() {
			out = append(out, c.Path)
		}
		return out
	}
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".py") {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "IJarvisCommand") {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}
