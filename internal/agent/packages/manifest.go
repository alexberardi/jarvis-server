// Package packages installs Pantry packages on a desktop agent (DESIGN.md §6, "Install/update"):
// the app asks over MQTT (package-install, -uninstall, -revert, as on a Pi), the agent checks
// the request with jarvisd, downloads the package, installs its pip dependencies into a
// uv-managed venv, and restarts the Python sidecar on the new package list. A package whose
// commands don't load is put back the way it was.
package packages

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestFile is the package manifest at the root of a Pantry package repository; a
// single-command repository may have CommandManifestFile instead.
const (
	ManifestFile        = "jarvis_package.yaml"
	CommandManifestFile = "jarvis_command.yaml"
)

// Manifest is the part of jarvis_package.yaml the agent acts on.
type Manifest struct {
	Name        string      `yaml:"name"`
	DisplayName string      `yaml:"display_name"`
	Version     string      `yaml:"version"`
	Platforms   []string    `yaml:"platforms"`
	Targets     []string    `yaml:"targets"`
	Components  []Component `yaml:"components"`
	Packages    []PipDep    `yaml:"packages"`
	// Pi-only system setup, run by the Pi profile's System: a desktop agent installs no system
	// packages and runs no post-install steps, so a package that needs them is refused there.
	AptPackages []string         `yaml:"apt_packages"`
	AptSources  []AptSource      `yaml:"apt_sources"`
	PostInstall []map[string]any `yaml:"post_install"`
}

// AptSource is a third-party apt repository a package needs (its signing key and its line).
type AptSource struct {
	Name   string `yaml:"name"`
	KeyURL string `yaml:"key_url"`
	Repo   string `yaml:"repo"`
}

// Component is one installable part of a package.
type Component struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`
	Path string `yaml:"path"`
}

// PipDep is one Python dependency: a name and an optional version ("1.2" pins it, ">=1.2" is
// a specifier), as the Pi node reads it.
type PipDep struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// Spec is the requirement uv installs.
func (d PipDep) Spec() string {
	v := strings.TrimSpace(d.Version)
	switch {
	case v == "":
		return d.Name
	case v[0] >= '0' && v[0] <= '9':
		return d.Name + "==" + v
	default:
		return d.Name + v
	}
}

var (
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	// A requirement name and an optional PEP 440 specifier, without URLs, options or markers: a
	// manifest must not be able to pass flags to uv or point it at another index.
	pipNameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9._,-]+\])?$`)
	pipVersionRE = regexp.MustCompile(`^(==|>=|<=|~=|!=|>|<)?[0-9A-Za-z][0-9A-Za-z.*+!_-]*(,(==|>=|<=|~=|!=|>|<)[0-9A-Za-z][0-9A-Za-z.*+!_-]*)*$`)
	// A Debian package name, as N:scripts/jarvis-apt-install checks it.
	aptNameRE = regexp.MustCompile(`^[a-z][a-z0-9.+-]*$`)
)

// ParseManifest reads and checks a manifest. It refuses anything that could reach outside the
// package directory or hand uv more than a requirement.
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if !nameRE.MatchString(m.Name) {
		return nil, fmt.Errorf("%s: invalid package name %q", ManifestFile, m.Name)
	}
	for _, c := range m.Components {
		if c.Type == "" || c.Name == "" {
			return nil, fmt.Errorf("%s: a component needs a type and a name", ManifestFile)
		}
		if c.Type == "command" {
			if err := checkRelPath(c.Path); err != nil {
				return nil, fmt.Errorf("%s: component %s: %w", ManifestFile, c.Name, err)
			}
			if !strings.HasSuffix(c.Path, ".py") {
				return nil, fmt.Errorf("%s: component %s: path %q is not a .py file", ManifestFile, c.Name, c.Path)
			}
		}
	}
	for _, d := range m.Packages {
		if !pipNameRE.MatchString(d.Name) {
			return nil, fmt.Errorf("%s: invalid Python package name %q", ManifestFile, d.Name)
		}
		if v := strings.TrimSpace(d.Version); v != "" && !pipVersionRE.MatchString(v) {
			return nil, fmt.Errorf("%s: invalid version %q for %s", ManifestFile, d.Version, d.Name)
		}
	}
	for _, p := range m.AptPackages {
		if !aptNameRE.MatchString(p) {
			return nil, fmt.Errorf("%s: invalid apt package name %q", ManifestFile, p)
		}
	}
	for _, src := range m.AptSources {
		if src.Name == "" || src.KeyURL == "" || src.Repo == "" {
			return nil, fmt.Errorf("%s: an apt_sources entry needs a name, a key_url and a repo", ManifestFile)
		}
	}
	for i, op := range m.PostInstall {
		if t, _ := op["type"].(string); postInstallOps[t] == "" {
			return nil, fmt.Errorf("%s: post_install[%d]: unknown op type %q", ManifestFile, i, op["type"])
		}
	}
	return &m, nil
}

// LoadManifest reads the manifest of the package in dir. Components it doesn't declare are
// found by the Pi's conventions (core/command_manifest.py infer_components), and every command
// component's file must exist.
func LoadManifest(dir string) (*Manifest, error) {
	var b []byte
	var err error
	for _, name := range []string{ManifestFile, CommandManifestFile} {
		if b, err = os.ReadFile(filepath.Join(dir, name)); err == nil {
			break
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("not a Jarvis package: no %s or %s", ManifestFile, CommandManifestFile)
	}
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(b)
	if err != nil {
		return nil, err
	}
	if len(m.Components) == 0 {
		m.Components = inferComponents(dir, m.Name)
	}
	if len(m.Components) == 0 {
		return nil, fmt.Errorf("%s has no components", m.label())
	}
	for _, c := range m.Commands() {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(c.Path))); err != nil {
			return nil, fmt.Errorf("%s: component %s: %s is missing", m.label(), c.Name, c.Path)
		}
	}
	return m, nil
}

// componentDirs are the Pi's conventional component directories and each one's entry file.
var componentDirs = []struct{ dir, typ, entry string }{
	{"commands", "command", "command.py"},
	{"agents", "agent", "agent.py"},
	{"device_families", "device_protocol", "protocol.py"},
	{"device_managers", "device_manager", "manager.py"},
	{"prompt_providers", "prompt_provider", "provider.py"},
	{"routines", "routine", "routine.json"},
}

// inferComponents finds a package's components by convention, as the Pi does: command.py at
// the root, then <dir>/<name>/<entry> under each conventional directory.
func inferComponents(dir, name string) []Component {
	var out []Component
	if st, err := os.Stat(filepath.Join(dir, "command.py")); err == nil && st.Mode().IsRegular() {
		out = append(out, Component{Type: "command", Name: name, Path: "command.py"})
	}
	for _, cd := range componentDirs {
		entries, err := os.ReadDir(filepath.Join(dir, cd.dir))
		if err != nil {
			continue
		}
		for _, e := range entries { // ReadDir sorts by name
			if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if st, err := os.Stat(filepath.Join(dir, cd.dir, e.Name(), cd.entry)); err == nil && st.Mode().IsRegular() {
				out = append(out, Component{Type: cd.typ, Name: e.Name(), Path: cd.dir + "/" + e.Name() + "/" + cd.entry})
			}
		}
	}
	return out
}

// checkRelPath accepts a clean relative slash path inside the package.
func checkRelPath(p string) error {
	if p == "" {
		return errors.New("no path")
	}
	if strings.Contains(p, `\`) || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("path %q is not inside the package", p)
	}
	return nil
}

// Desktop is the target name a package declares to run on a desktop agent (DESIGN.md §8).
const Desktop = "desktop"

// CheckDesktop refuses a package this desktop can't run: another OS in platforms, targets that
// leave out the desktop, or Pi system setup. A package that declares no targets is allowed: no
// Pantry package declares them yet, and the user picks the computer in the app.
func (m *Manifest) CheckDesktop(goos string) error {
	if len(m.Platforms) > 0 && !slices.Contains(m.Platforms, goos) {
		return fmt.Errorf("%s supports %s, not %s", m.label(), strings.Join(m.Platforms, ", "), goos)
	}
	if len(m.Targets) > 0 && !slices.Contains(m.Targets, Desktop) {
		return fmt.Errorf("%s is made for Pi nodes, not computers", m.label())
	}
	if len(m.AptPackages) > 0 || len(m.AptSources) > 0 || len(m.PostInstall) > 0 {
		return fmt.Errorf("%s needs system packages or setup steps that only a Pi node runs", m.label())
	}
	if len(m.Commands()) == 0 {
		return fmt.Errorf("%s has no commands a computer can run", m.label())
	}
	return nil
}

// Node is the target name a package declares to run on a Pi node (DESIGN.md §8).
const Node = "node"

// CheckPi refuses a package a Pi node can't run: another OS in platforms, targets that leave
// out the node, or no commands. Its system setup is the Pi profile's System's to run.
func (m *Manifest) CheckPi() error {
	if len(m.Platforms) > 0 && !slices.Contains(m.Platforms, "linux") {
		return fmt.Errorf("%s supports %s, not linux", m.label(), strings.Join(m.Platforms, ", "))
	}
	if len(m.Targets) > 0 && !slices.Contains(m.Targets, Node) {
		return fmt.Errorf("%s is made for computers, not Pi nodes", m.label())
	}
	if len(m.Commands()) == 0 {
		return fmt.Errorf("%s has no commands a node can run", m.label())
	}
	return nil
}

// Commands are the components the sidecar loads. Agents, device protocols and the other Pi
// component types don't run on a desktop yet.
func (m *Manifest) Commands() []Component {
	var out []Component
	for _, c := range m.Components {
		if c.Type == "command" {
			out = append(out, c)
		}
	}
	return out
}

// Skipped names the components a desktop doesn't run, as "type:name".
func (m *Manifest) Skipped() []string {
	var out []string
	for _, c := range m.Components {
		if c.Type != "command" {
			out = append(out, c.Type+":"+c.Name)
		}
	}
	return out
}

func (m *Manifest) label() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Name
}
