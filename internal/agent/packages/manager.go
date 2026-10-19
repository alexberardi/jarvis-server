package packages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// Sidecar is a started package sidecar.
type Sidecar interface {
	// Failed are the packages whose commands didn't load, with why.
	Failed() map[string]string
	Close() error
}

// Sidecars starts package sidecars and puts one in service.
type Sidecars interface {
	Start(ctx context.Context, python, commandsFile string) (Sidecar, error)
	// Swap puts sc in service (nil: none) and closes the one before.
	Swap(sc Sidecar)
}

// LiveSidecars runs real sidecars behind a sidecar.Live.
type LiveSidecars struct {
	Live    *sidecar.Live
	Log     *slog.Logger
	Storage sidecar.Storage
	Host    sidecar.Host // the Pi's SDK backends; nil registers none
}

func (s LiveSidecars) Start(ctx context.Context, python, commandsFile string) (Sidecar, error) {
	sc, err := sidecar.StartWith(ctx, python, commandsFile, s.Log, sidecar.Options{Storage: s.Storage, Host: s.Host})
	if err != nil {
		return nil, err
	}
	return sc, nil
}

func (s LiveSidecars) Swap(sc Sidecar) {
	x, _ := sc.(*sidecar.Sidecar)
	s.Live.Swap(x)
}

// Manager keeps the installed packages under Root:
//
//	Root/pkgs/<name>/            a package as downloaded (jarvis_package.yaml at its root)
//	Root/pkgs/<name>.previous/   the version before the last update (what revert restores)
//	Root/commands.json           the sidecar's list, rebuilt from pkgs/ on every change
//	Root/venv, Root/python, Root/bin   the Python environment (Python)
//	Root/builtins.txt            what the venv's built-in commands were installed from (Builtins)
//	Root/tests/<name>/           a Forge test install, served for 20 minutes (TestInstall)
//
// One change runs at a time. Every change ends with a fresh sidecar on the new list; an
// install or revert whose commands don't load is undone and the old sidecar keeps serving.
type Manager struct {
	Root     string
	Python   *Python
	HTTP     *http.Client
	GOOS     string
	Sidecars Sidecars
	Log      *slog.Logger
	// Builtins, when set (the Pi profile), are the node's own commands: every sidecar loads
	// them, with or without packages.
	Builtins *Builtins
	// System, when set (the Pi profile), runs a package's apt sources, apt packages and
	// post_install ops; without it a package that needs them is refused.
	System *System

	mu     sync.Mutex
	now    func() time.Time // time.Now; tests move it
	failed map[string]string // of the sidecar in service
}

// Builtins are a node's built-in commands: a pip requirement Boot installs into the venv (once
// per requirement), and the Python package whose *_command modules the sidecar loads, less Skip.
type Builtins struct {
	Requirement string
	Package     string
	Skip        []string
}

// Installed is one installed package.
type Installed struct {
	Manifest        *Manifest
	Dir             string
	PreviousVersion string // "" when there is nothing to revert to
}

func (m *Manager) pkgs() string           { return filepath.Join(m.Root, "pkgs") }
func (m *Manager) dir(name string) string { return filepath.Join(m.pkgs(), name) }
func (m *Manager) commandsFile() string   { return filepath.Join(m.Root, "commands.json") }

// List reads the installed packages, by name. A directory without a readable manifest is
// skipped (and logged).
func (m *Manager) List() []Installed {
	entries, err := os.ReadDir(m.pkgs())
	if err != nil {
		return nil
	}
	var out []Installed
	for _, e := range entries {
		if !e.IsDir() || strings.Contains(e.Name(), ".") {
			continue
		}
		man, err := LoadManifest(m.dir(e.Name()))
		if err != nil || man.Name != e.Name() {
			m.Log.Warn("packages: skipping an unreadable package", "dir", e.Name(), "err", err)
			continue
		}
		in := Installed{Manifest: man, Dir: m.dir(e.Name())}
		if prev, err := LoadManifest(m.dir(e.Name()) + ".previous"); err == nil {
			in.PreviousVersion = prev.Version
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.Name < out[j].Manifest.Name })
	return out
}

// Report is the installed_packages list of a report_tools answer, as a Pi sends it: name,
// version, previous_version when there is one, and health "failed" when the package's
// commands didn't load.
func (m *Manager) Report() []map[string]any {
	m.mu.Lock()
	failed := m.failed
	m.mu.Unlock()
	out := []map[string]any{}
	for _, in := range m.List() {
		e := map[string]any{"name": in.Manifest.Name, "version": in.Manifest.Version, "health": "ok"}
		if in.PreviousVersion != "" {
			e["previous_version"] = in.PreviousVersion
		}
		if _, bad := failed[in.Manifest.Name]; bad {
			e["health"] = "failed"
		}
		out = append(out, e)
	}
	return out
}

// Boot starts the sidecar on the installed packages, if there are any, and the built-ins.
func (m *Manager) Boot(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.System != nil {
		m.System.Heal(ctx, func(name string) bool { return exists(filepath.Join(m.dir(name), ManifestFile)) || exists(filepath.Join(m.dir(name), CommandManifestFile)) })
	}
	m.removeTests(true)
	if m.Builtins != nil {
		if err := m.installBuiltins(ctx); err != nil {
			if !exists(m.builtinsStamp()) && len(m.List()) == 0 {
				return fmt.Errorf("installing the built-in commands: %w", err)
			}
			// Offline at a new pin: the venv's copy serves until the next start.
			m.Log.Warn("packages: built-in commands not updated", "err", err)
		}
	} else if len(m.List()) == 0 && len(m.tests()) == 0 {
		return nil
	}
	if !m.Python.Ready() {
		return errors.New("packages are installed but their Python environment is missing; reinstall them from the app")
	}
	sc, err := m.start(ctx)
	if err != nil {
		return err
	}
	m.commit(sc)
	for name, why := range sc.Failed() {
		m.Log.Warn("packages: a package didn't load", "package", name, "err", why)
	}
	return nil
}

func (m *Manager) builtinsStamp() string { return filepath.Join(m.Root, "builtins.txt") }

// installBuiltins pip-installs the built-ins (with the SDK) unless the venv already has this
// requirement.
func (m *Manager) installBuiltins(ctx context.Context) error {
	sdk := m.Python.SDK
	if sdk == "" {
		sdk = SDKRequirement
	}
	want := sdk + "\n" + m.Builtins.Requirement + "\n"
	if b, err := os.ReadFile(m.builtinsStamp()); err == nil && string(b) == want && m.Python.Ready() {
		return nil
	}
	m.Log.Info("packages: installing the built-in commands", "requirement", m.Builtins.Requirement)
	if err := m.Python.Install(ctx, []string{m.Builtins.Requirement}); err != nil {
		return err
	}
	return writeAtomic(m.builtinsStamp(), []byte(want))
}

// writeList rewrites commands.json from pkgs/ and returns how many packages it lists.
func (m *Manager) writeList() (int, error) {
	type entry struct {
		Name     string   `json:"name"`
		Root     string   `json:"root"`
		Commands []string `json:"commands"`
	}
	list := []entry{}
	for _, in := range m.List() {
		e := entry{Name: in.Manifest.Name, Root: in.Dir}
		for _, c := range in.Manifest.Commands() {
			e.Commands = append(e.Commands, c.Path)
		}
		list = append(list, e)
	}
	for _, tp := range m.tests() {
		list = append(list, entry{Name: testPrefix + tp.name, Root: tp.dir, Commands: tp.commands})
	}
	doc := map[string]any{"packages": list}
	if bi := m.Builtins; bi != nil {
		doc["builtins"] = map[string]any{"package": bi.Package, "skip": bi.Skip}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return 0, err
	}
	return len(list), writeAtomic(m.commandsFile(), b)
}

// start rewrites commands.json and starts a sidecar on it (not yet in service). With no
// packages and no built-ins it returns nil, nil.
func (m *Manager) start(ctx context.Context) (Sidecar, error) {
	n, err := m.writeList()
	if err != nil || (n == 0 && m.Builtins == nil) {
		return nil, err
	}
	return m.Sidecars.Start(ctx, m.Python.Interpreter(), m.commandsFile())
}

// commit puts sc in service (nil: none).
func (m *Manager) commit(sc Sidecar) {
	m.Sidecars.Swap(sc)
	m.failed = nil
	if sc != nil {
		m.failed = sc.Failed()
	}
}

// Install downloads repo at ref (a tag, branch or commit; "" for the default branch), installs
// its Python dependencies and puts it in service. An update keeps the version before it for
// revert. The details are the Pi's result details.
func (m *Manager) Install(ctx context.Context, repo, ref string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := ArchiveURL(repo, ref)
	if err != nil {
		return nil, err
	}
	stage, err := m.stage()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	src := filepath.Join(stage, "src")
	if err := Download(ctx, m.HTTP, u, src); err != nil {
		return nil, err
	}
	man, err := LoadManifest(src)
	if err != nil {
		return nil, err
	}
	if err := m.checkManifest(man); err != nil {
		return nil, err
	}
	if m.System != nil {
		if err := m.System.Prepare(ctx, man); err != nil {
			return nil, err
		}
	}
	var reqs []string
	for _, d := range man.Packages {
		reqs = append(reqs, d.Spec())
	}
	m.Log.Info("packages: installing", "package", man.Name, "version", man.Version, "requirements", reqs)
	if err := m.Python.Install(ctx, reqs); err != nil {
		return nil, err
	}
	live, prev, old := m.dir(man.Name), m.dir(man.Name)+".previous", filepath.Join(stage, "old")
	update := exists(live)
	if update {
		if err := os.Rename(live, old); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(src, live); err != nil {
		m.putBack(live, old, update)
		return nil, err
	}
	err = nil
	if m.System != nil {
		err = m.System.PostInstall(ctx, man)
	}
	if err == nil {
		err = m.check(ctx, man.Name)
	}
	if err != nil {
		m.putBack(live, old, update)
		m.undoPostInstall(ctx, man.Name, old, update)
		return nil, err
	}
	if update {
		_ = os.RemoveAll(prev)
		if err := os.Rename(old, prev); err != nil {
			m.Log.Warn("packages: couldn't keep the previous version", "package", man.Name, "err", err)
		}
	}
	m.Log.Info("packages: installed", "package", man.Name, "version", man.Version, "skipped", man.Skipped())
	details := map[string]any{"package_name": man.Name, "version": man.Version, "components": len(man.Components)}
	if s := man.Skipped(); len(s) > 0 {
		details["skipped_components"] = s
	}
	return details, nil
}

// checkManifest refuses a package this agent can't run (CheckDesktop, or CheckPi with a System).
func (m *Manager) checkManifest(man *Manifest) error {
	if m.System != nil {
		return man.CheckPi()
	}
	return man.CheckDesktop(m.GOOS)
}

// undoPostInstall follows a failed install's putBack: a fresh install's drop-ins go, an
// update's previous version (back in place) gets its own ops again. Best effort.
func (m *Manager) undoPostInstall(ctx context.Context, name, old string, update bool) {
	if m.System == nil {
		return
	}
	if !update {
		m.System.RemoveDropins(ctx, name)
		return
	}
	if prev, err := LoadManifest(m.dir(name)); err == nil && len(prev.PostInstall) > 0 {
		if err := m.System.PostInstall(ctx, prev); err != nil {
			m.Log.Warn("packages: couldn't reapply the previous version's post_install", "package", name, "err", err)
		}
	}
}

// check starts a sidecar on pkgs/ as it is now and puts it in service, unless it doesn't start
// or name's commands didn't load: then the old sidecar keeps serving.
func (m *Manager) check(ctx context.Context, name string) error {
	sc, err := m.start(ctx)
	if err != nil {
		return fmt.Errorf("the package sidecar didn't start: %w", err)
	}
	if sc == nil {
		return fmt.Errorf("%s has no commands", name)
	}
	if why, bad := sc.Failed()[name]; bad {
		_ = sc.Close()
		return fmt.Errorf("%s didn't load: %s", name, why)
	}
	m.commit(sc)
	return nil
}

// putBack undoes a change that didn't load: live goes, old (if there was one) comes back, and
// commands.json is rewritten for the sidecar still in service.
func (m *Manager) putBack(live, old string, hadOld bool) {
	_ = os.RemoveAll(live)
	if hadOld {
		if err := os.Rename(old, live); err != nil {
			m.Log.Error("packages: couldn't restore a package", "dir", live, "err", err)
		}
	}
	if _, err := m.writeList(); err != nil {
		m.Log.Warn("packages: couldn't rewrite the commands list", "err", err)
	}
}

// find is the installed package called name, or the one providing the command name (the app's
// uninstall names a command).
func (m *Manager) find(name string) (Installed, bool) {
	list := m.List()
	for _, in := range list {
		if in.Manifest.Name == name {
			return in, true
		}
	}
	for _, in := range list {
		for _, c := range in.Manifest.Components {
			if c.Name == name {
				return in, true
			}
		}
	}
	return Installed{}, false
}

// Uninstall removes the package called name (or providing command name) and its previous
// version, and restarts the sidecar without it.
func (m *Manager) Uninstall(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.find(name)
	if !ok {
		return "", fmt.Errorf("%s isn't installed on this computer", name)
	}
	stage, err := m.stage()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := os.Rename(in.Dir, filepath.Join(stage, "gone")); err != nil {
		return "", err
	}
	_ = os.RemoveAll(in.Dir + ".previous")
	if m.System != nil {
		m.System.RemoveDropins(ctx, in.Manifest.Name)
	}
	sc, err := m.start(ctx)
	if err != nil {
		m.commit(nil)
		return in.Manifest.Name, fmt.Errorf("removed %s, but the other packages didn't restart: %w", in.Manifest.Name, err)
	}
	m.commit(sc)
	m.Log.Info("packages: uninstalled", "package", in.Manifest.Name)
	return in.Manifest.Name, nil
}

// Revert puts back the version before the last update. It returns the package and the version
// now in service.
func (m *Manager) Revert(ctx context.Context, name string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.find(name)
	if !ok {
		return "", "", fmt.Errorf("%s isn't installed on this computer", name)
	}
	if in.PreviousVersion == "" {
		return in.Manifest.Name, "", fmt.Errorf("%s has no previous version to go back to", in.Manifest.Name)
	}
	stage, err := m.stage()
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(stage)
	// Its dependencies first (best effort: what the newer version added is harmless).
	prev, err := LoadManifest(in.Dir + ".previous")
	if err == nil {
		var reqs []string
		for _, d := range prev.Packages {
			reqs = append(reqs, d.Spec())
		}
		if err := m.Python.Install(ctx, reqs); err != nil {
			m.Log.Warn("packages: revert: dependencies", "package", in.Manifest.Name, "err", err)
		}
	}
	cur := filepath.Join(stage, "cur")
	if err := os.Rename(in.Dir, cur); err != nil {
		return "", "", err
	}
	if err := os.Rename(in.Dir+".previous", in.Dir); err != nil {
		m.putBack(in.Dir, cur, true)
		return "", "", err
	}
	err = nil
	if m.System != nil && prev != nil {
		// Its post_install ops again (idempotent, as on the Python node's reinstall).
		err = m.System.PostInstall(ctx, prev)
	}
	if err == nil {
		err = m.check(ctx, in.Manifest.Name)
	}
	if err != nil {
		// The previous version stays where it was, and the current one comes back.
		if err2 := os.Rename(in.Dir, in.Dir+".previous"); err2 != nil {
			m.Log.Error("packages: couldn't keep the previous version", "err", err2)
		}
		m.putBack(in.Dir, cur, true)
		return in.Manifest.Name, "", err
	}
	m.Log.Info("packages: reverted", "package", in.Manifest.Name, "version", in.PreviousVersion)
	return in.Manifest.Name, in.PreviousVersion, nil
}

// stage is a fresh directory on the same filesystem as pkgs/, so moves are renames.
func (m *Manager) stage() (string, error) {
	if err := os.MkdirAll(m.pkgs(), 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(m.Root, "staging")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "op-")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
