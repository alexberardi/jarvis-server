// Package selfupdate is the desktop agent's self-update (01-no-cli-plan item 1.7, DESIGN §13
// "Self-update"). The agent follows its jarvisd: it installs the jarvis-server release whose tag
// is the server's own version, so a household's agents match its server.
//
// The trust chain is jarvisd's (internal/update): the release's SHA256SUMS must carry a minisign
// signature by a key compiled into the running agent, whose trusted comment names that release,
// and the agent archive must match its line in that list. Nothing unsigned, foreign-signed,
// older or equal to the running version is ever installed; a development build updates only
// when forced.
//
// One update is three steps, each its own function so every OS's path can be tested anywhere:
//
//  1. Stage (the running agent, or `jarvis-agent update`): fetch and verify the archive, unpack
//     the new binary (or, on macOS, the new app bundle), run its `version`, check a bundle's code
//     signature, and write the marker (StateStaged).
//  2. Finish (a separate process, `jarvis-agent update --finish`, outside the supervisor's kill
//     scope): verify the staged files again, Swap the new version in, restart the agent through
//     its supervisor, and wait for it to answer the control socket's health check (connected
//     to jarvisd) as the new version. Otherwise Rollback restores the previous one and restarts
//     that.
//  3. The restarted agent reads the result: "Updated to vX", or a failed update reported to
//     jarvisd's update task.
package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// BundleName is the macOS app bundle in a darwin agent archive (item 2.2 packs it, signed):
// <top>/Jarvis Automation Agent.app/.
const BundleName = appid.DisplayName + ".app"

// Marker states.
const (
	StateStaged  = "staged"
	StateSwapped = "swapped"
)

// Result outcomes.
const (
	OutcomeSucceeded  = "succeeded"
	OutcomeRolledBack = "rolled_back"
	OutcomeFailed     = "failed" // nothing was changed
)

// Who started an update (Marker.By).
const (
	ByAuto   = "auto"   // following the server's version (daily, or when it changed)
	ByServer = "server" // a household member's update request (heartbeat pending_update)
	ByCLI    = "cli"    // jarvis-agent update
)

// Errors a caller tells apart.
var (
	ErrDevBuild    = errors.New("this is a development build of jarvis-agent: it never updates itself unless forced (jarvis-agent update --force --version vX)")
	ErrNotNewer    = errors.New("not newer than the running version")
	ErrInProgress  = errors.New("an update is already in progress")
	ErrNotWritable = errors.New("this user can't replace the running agent")
)

// staleAfter is when a marker nobody finished counts as abandoned (a machine that slept or
// rebooted during an update): the next Stage records it as failed and starts afresh.
const staleAfter = 30 * time.Minute

// Paths is where the update keeps its state: the agent's cache directory.
type Paths struct{ Dir string }

// DefaultPaths is <user cache dir>/jarvis-agent/updates.
func DefaultPaths() (Paths, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{Dir: filepath.Join(d, "jarvis-agent", "updates")}, nil
}

// Staged holds the verified archive, its SHA256SUMS and signature, and the unpacked copy.
func (p Paths) Staged() string { return filepath.Join(p.Dir, "staged") }

// MarkerPath is the update in progress (Marker).
func (p Paths) MarkerPath() string { return filepath.Join(p.Dir, "update.json") }

// ResultPath is the last finished update (Result).
func (p Paths) ResultPath() string { return filepath.Join(p.Dir, "last-update.json") }

// LogPath is where `update --finish` writes when it runs detached.
func (p Paths) LogPath() string { return filepath.Join(p.Dir, "update.log") }

// Marker is an update in progress, written by Stage and read by Finish.
type Marker struct {
	State  string    `json:"state"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Exe    string    `json:"exe"`              // the running executable this replaces
	Bundle string    `json:"bundle,omitempty"` // macOS: the app bundle this replaces
	Asset  string    `json:"asset"`            // the archive's name in SHA256SUMS
	TaskID string    `json:"task_id,omitempty"`
	By     string    `json:"by"`
	Force  bool      `json:"force,omitempty"` // a development build, forced
	At     time.Time `json:"at"`
}

// Result is the last finished update.
type Result struct {
	Outcome string    `json:"outcome"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Reason  string    `json:"reason,omitempty"`
	TaskID  string    `json:"task_id,omitempty"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at"`
	// Reported: a failure was reported on jarvisd's update task (TaskID).
	Reported bool `json:"reported,omitempty"`
}

// Target is what an update replaces on this machine.
type Target struct {
	GOOS string
	// Exe is the running executable, absolute, symlinks resolved.
	Exe string
	// Bundle is the macOS app Exe runs from ("…/X.app"), "" when it is a bare binary.
	Bundle string
	// Pi: Exe is the Pi node's agent in /opt/jarvis-agent, run by its system unit
	// (install-pi.sh). The update is the same; the restart is the unit's (install.Restart).
	Pi bool
}

// Updater stages and installs agent releases.
type Updater struct {
	Paths   Paths
	Target  Target
	Current string // the running agent's version
	Source  update.Source
	// Keys verify SHA256SUMS.minisig; nil is update.TrustedKeys() (the keys built into this binary).
	Keys []update.PublicKey
	// Platform picks the archive ("linux-amd64"); "" is this one.
	Platform string
	// CheckVersion runs the new binary's `version` (default update.CheckProgramVersion).
	CheckVersion func(bin, tag string) error
	// Codesign checks a macOS bundle's signature (nil: the codesign tool). Unused elsewhere.
	Codesign Codesigner
	// Progress, when set, gets a line per step and the bytes downloaded.
	Progress func(step string, done int64)
	Now      func() time.Time
	Log      *slog.Logger
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) log() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (u *Updater) keys() ([]update.PublicKey, error) {
	if u.Keys != nil {
		return u.Keys, nil
	}
	return update.TrustedKeys()
}

func (u *Updater) platform() string {
	if u.Platform != "" {
		return u.Platform
	}
	return update.Platform()
}

func (u *Updater) progress(step string, done int64) {
	if u.Progress != nil {
		u.Progress(step, done)
	}
}

// AssetName is the agent archive for tag on this platform.
func (u *Updater) AssetName(tag string) string {
	return update.ProgramArchiveName(update.AgentProgram, tag, u.platform())
}

// IsRelease reports whether v is a release version (a development build's isn't).
func IsRelease(v string) bool {
	_, ok := update.ParseVersion(v)
	return ok
}

// Newer reports whether release a is newer than b; false when either isn't a release version.
func Newer(a, b string) bool {
	av, okA := update.ParseVersion(a)
	bv, okB := update.ParseVersion(b)
	return okA && okB && av.Compare(bv) > 0
}

// Allowed is the policy, before anything is fetched: target is a release strictly newer than
// the running version (never older or the same, forced or not), a development build only when
// forced, and this user can replace the agent.
func (u *Updater) Allowed(target string, force bool) error {
	tv, ok := update.ParseVersion(target)
	if !ok {
		return fmt.Errorf("%q is not a release version", target)
	}
	cv, ok := update.ParseVersion(u.Current)
	switch {
	case !ok && !force:
		return ErrDevBuild
	case ok && tv.Compare(cv) <= 0:
		return fmt.Errorf("%s is %w %s", target, ErrNotNewer, u.Current)
	}
	return u.writable()
}

// writable checks this user can replace what Target names: the executable's directory, or the
// directory holding the app bundle.
func (u *Updater) writable() error {
	if u.Target.Exe == "" {
		return errors.New("the running agent's path is unknown")
	}
	dir := filepath.Dir(u.Target.Exe)
	what := u.Target.Exe
	if u.Target.Bundle != "" {
		dir, what = filepath.Dir(u.Target.Bundle), u.Target.Bundle
	}
	f, err := os.CreateTemp(dir, ".jarvis-agent-write-check-*")
	if err != nil {
		hint := ""
		switch {
		case u.Target.Pi:
			hint = " (run install-pi.sh again: it gives " + dir + " to the node's user, so the agent updates itself)"
		case u.Target.GOOS == "linux":
			hint = " (run `jarvis-agent install`: it sets the agent up in ~/.local/lib/jarvis-agent, which updates itself)"
		}
		return fmt.Errorf("%w: %s isn't writable here%s", ErrNotWritable, what, hint)
	}
	f.Close()
	_ = os.Remove(f.Name())
	return nil
}

// ReadMarker returns the update in progress, nil when there is none.
func ReadMarker(p Paths) (*Marker, error) {
	var m Marker
	if ok, err := readJSON(p.MarkerPath(), &m); err != nil || !ok {
		return nil, err
	}
	return &m, nil
}

// ReadResult returns the last finished update, nil when there is none.
func ReadResult(p Paths) (*Result, error) {
	var r Result
	if ok, err := readJSON(p.ResultPath(), &r); err != nil || !ok {
		return nil, err
	}
	return &r, nil
}

// WriteResult records r (atomically).
func WriteResult(p Paths, r *Result) error { return writeJSON(p.ResultPath(), r) }

func writeMarker(p Paths, m *Marker) error { return writeJSON(p.MarkerPath(), m) }

func readJSON(path string, into any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(b) > 1<<20 {
		return false, fmt.Errorf("%s is too large", path)
	}
	if err := json.Unmarshal(b, into); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return update.RenameRetry(f.Name(), path)
}

// prevPath is where Swap keeps the version it replaced: <exe>.prev (jarvis-agent.prev.exe on
// Windows), or for a bundle a hidden sibling of it.
func (t Target) prevPath() string {
	if t.Bundle != "" {
		return filepath.Join(filepath.Dir(t.Bundle), ".jarvis-agent-previous.app")
	}
	if t.GOOS == "windows" {
		return strings.TrimSuffix(t.Exe, ".exe") + ".prev.exe"
	}
	return t.Exe + ".prev"
}

// newPath is where Swap unpacks the new version, next to what it replaces (same filesystem, so
// the swap is a rename).
func (t Target) newPath() string {
	if t.Bundle != "" {
		return filepath.Join(filepath.Dir(t.Bundle), ".jarvis-agent-update.app")
	}
	if t.GOOS == "windows" {
		return filepath.Join(filepath.Dir(t.Exe), ".jarvis-agent.new.exe")
	}
	return filepath.Join(filepath.Dir(t.Exe), ".jarvis-agent.new")
}

// rolledBackPath keeps a Windows binary a rollback replaced (a running file can't be deleted).
func (t Target) rolledBackPath() string {
	return strings.TrimSuffix(t.Exe, ".exe") + ".rolledback.exe"
}

// programFile is the agent's file name in an archive for t.GOOS.
func (t Target) programFile() string {
	if t.GOOS == "windows" {
		return update.AgentProgram + ".exe"
	}
	return update.AgentProgram
}
