package packages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/sysinfo"
)

// The root-owned wrappers install-pi.sh puts in /usr/local/sbin (N:scripts/, byte for byte), each
// with a NOPASSWD sudoers line for the node's user. They shape-check what they get; what a
// package may ask for is Pantry's allow-list, checked when the package is submitted.
const (
	AptInstallWrapper  = "jarvis-apt-install"
	AptSourceWrapper   = "jarvis-apt-source"
	PostInstallWrapper = "jarvis-post-install"
)

// Timeouts as the Python node's (N:services/command_store_service.py).
const (
	aptTimeout         = 300 * time.Second
	aptSourceTimeout   = 60 * time.Second
	postInstallTimeout = 300 * time.Second
	removeTimeout      = 30 * time.Second
	// aptMinFree is the free space on / an apt install needs.
	aptMinFree = 500 << 20
)

// postInstallOps maps a manifest's post_install op type to the wrapper's subcommand.
var postInstallOps = map[string]string{
	"configure_systemd_service": "configure-systemd-service",
	"set_config_file_value":     "set-config-file-value",
}

// managedByPrefix starts every drop-in jarvis-post-install writes (its _MANAGED_BY_PREFIX).
const managedByPrefix = "# managed-by: jarvis-package "

// SudoRunner runs one program with stdin and returns its combined output.
type SudoRunner func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)

func execSudo(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

// System runs a package's Pi system setup (apt sources, apt packages, post_install ops) through
// the wrappers, as the Python node does (N:services/command_store_service.py:538-830), and
// removes the systemd drop-ins a package's ops wrote. Only the Pi profile has one.
type System struct {
	Run        SudoRunner
	User       string // the node's user: run_as jarvis_user and ${USER}
	UID        int    // ${UID}
	Home       string // ${HOME}
	Sbin       string // where the wrappers are; "" is /usr/local/sbin
	DropinRoot string // systemd's unit directory; "" is /etc/systemd/system
	// FreeBytes is the free space on the filesystem holding path (0: unknown); nil skips the check.
	FreeBytes func(path string) uint64
	Log       *slog.Logger
}

// NewSystem is the System of the node running this process: its own user (the unit's User=).
func NewSystem(log *slog.Logger) *System {
	home, _ := os.UserHomeDir()
	user := os.Getenv("USER")
	if user == "" {
		user = filepath.Base(home)
	}
	return &System{Run: execSudo, User: user, UID: os.Getuid(), Home: home, FreeBytes: sysinfo.DiskFree, Log: log}
}

func (s *System) wrapper(name string) string {
	dir := s.Sbin
	if dir == "" {
		dir = "/usr/local/sbin"
	}
	return filepath.Join(dir, name)
}

func (s *System) dropinRoot() string {
	if s.DropinRoot == "" {
		return "/etc/systemd/system"
	}
	return s.DropinRoot
}

// sudo runs a wrapper as root without a prompt (sudoers' NOPASSWD line, or it fails at once).
func (s *System) sudo(ctx context.Context, timeout time.Duration, stdin []byte, wrapper string, args ...string) (string, error) {
	path := s.wrapper(wrapper)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s is missing: rerun install-pi.sh to put it in place", path)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := s.Run(ctx, stdin, "sudo", append([]string{"-n", path}, args...)...)
	text := strings.TrimSpace(string(out))
	if len(text) > 500 {
		text = text[:500]
	}
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, errors.New(text)
	}
	return text, nil
}

// Prepare registers a package's apt sources and installs its apt packages: before pip, so a
// Python dependency can build against them.
func (s *System) Prepare(ctx context.Context, man *Manifest) error {
	for _, src := range man.AptSources {
		s.Log.Info("packages: registering an apt source", "package", man.Name, "source", src.Name, "repo", src.Repo)
		if _, err := s.sudo(ctx, aptSourceTimeout, nil, AptSourceWrapper, src.Name, src.KeyURL, src.Repo); err != nil {
			return fmt.Errorf("apt-source registration failed: %w (source: %s)", err, src.Name)
		}
	}
	if len(man.AptPackages) == 0 {
		return nil
	}
	if s.FreeBytes != nil {
		if free := s.FreeBytes("/"); free > 0 && free < aptMinFree {
			return fmt.Errorf("insufficient disk: need ≥500MB free, have %dMB", free>>20)
		}
	}
	s.Log.Info("packages: installing apt packages", "package", man.Name, "apt", man.AptPackages)
	if _, err := s.sudo(ctx, aptTimeout, nil, AptInstallWrapper, man.AptPackages...); err != nil {
		return fmt.Errorf("apt install failed: %w (packages: %s)", err, strings.Join(man.AptPackages, " "))
	}
	return nil
}

// PostInstall runs a package's post_install ops, one wrapper call each with the op's JSON on
// stdin, after `run_as: jarvis_user` and the ${UID}, ${USER} and ${HOME} tokens are resolved
// for the node's user. The first failure stops it.
func (s *System) PostInstall(ctx context.Context, man *Manifest) error {
	for i, op := range man.PostInstall {
		typ, _ := op["type"].(string)
		sub := postInstallOps[typ]
		if sub == "" {
			return fmt.Errorf("post_install[%d]: unknown op type %q", i, typ)
		}
		payload := map[string]any{}
		for k, v := range op {
			if k != "type" {
				payload[k] = v
			}
		}
		if payload["run_as"] == "jarvis_user" {
			payload["run_as"] = s.User
		}
		b, err := json.Marshal(s.expand(payload))
		if err != nil {
			return fmt.Errorf("post_install[%d]: %w", i, err)
		}
		s.Log.Info("packages: post_install op", "package", man.Name, "op", typ, "service", op["service"], "file", op["file"])
		if _, err := s.sudo(ctx, postInstallTimeout, b, PostInstallWrapper, "--package", man.Name, sub); err != nil {
			return fmt.Errorf("post_install[%d] (%s) failed: %w", i, typ, err)
		}
	}
	return nil
}

// expand substitutes the tokens in every string of an op's payload.
func (s *System) expand(v any) any {
	switch x := v.(type) {
	case string:
		return strings.NewReplacer("${UID}", strconv.Itoa(s.UID), "${USER}", s.User, "${HOME}", s.Home).Replace(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = s.expand(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = s.expand(e)
		}
		return out
	}
	return v
}

// RemoveDropins asks the wrapper to remove the drop-ins package wrote (it also disables a unit
// the package's install enabled). Best effort: an uninstall goes on without it.
func (s *System) RemoveDropins(ctx context.Context, pkg string) bool {
	if !nameRE.MatchString(pkg) {
		return false
	}
	out, err := s.sudo(ctx, removeTimeout, nil, PostInstallWrapper, "--package", pkg, "remove-managed-dropins")
	if err != nil {
		s.Log.Warn("packages: drop-ins not removed", "package", pkg, "err", err)
		return false
	}
	if out != "" {
		s.Log.Info("packages: drop-ins removed", "package", pkg, "detail", out)
	}
	return true
}

// Heal removes the drop-ins of packages that are no longer installed
// (N:services/post_install_dropins.py heal_stale_dropins): a package deleted by hand, or by a
// factory reset, leaves a unit pointing at files that are gone. A package the Python node on
// the same Pi installed (~/.jarvis/packages/<name>.json; install-pi.sh --deactivate goes back to
// it) is still installed. It only reads the drop-ins (0644); removal goes through the wrapper.
func (s *System) Heal(ctx context.Context, installed func(name string) bool) {
	files, _ := filepath.Glob(filepath.Join(s.dropinRoot(), "*.service.d", "*.conf"))
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(string(b), "\n")
		owner, ok := strings.CutPrefix(first, managedByPrefix)
		owner = strings.TrimSpace(owner)
		if !ok || !nameRE.MatchString(owner) || installed(owner) || seen[owner] || s.pythonNodeHas(owner) {
			continue
		}
		seen[owner] = true
		s.Log.Warn("packages: removing a drop-in left by a removed package", "package", owner, "dropin", f)
		if !s.RemoveDropins(ctx, owner) {
			s.Log.Warn("packages: run `sudo " + s.wrapper(PostInstallWrapper) + " --package " + owner + " remove-managed-dropins` on the node")
		}
	}
}

// pythonNodeHas reports whether the Python node's package list has name (its metadata file).
func (s *System) pythonNodeHas(name string) bool {
	return s.Home != "" && exists(filepath.Join(s.Home, ".jarvis", "packages", name+".json"))
}
