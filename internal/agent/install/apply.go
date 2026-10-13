package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Registry is the per-user Windows registry (HKEY_CURRENT_USER); keys are relative to it.
type Registry interface {
	SetString(key, name, value string) error
	// GetString reports ok=false for a missing key or value.
	GetString(key, name string) (value string, ok bool, err error)
	DeleteValue(key, name string) error // a missing key or value is fine
	DeleteTree(key string) error        // a missing key is fine
}

// LoginItems is macOS's SMAppService for the app's own LaunchAgent.
type LoginItems interface {
	Register(plistName string) error
	Unregister(plistName string) error
}

// Browsers registers the native-messaging host (internal/agent/browser).
type Browsers interface {
	Register(exe string) error
	Unregister() error
}

// Sys is what Apply does things with. Tests use fakes; cmd/jarvis-agent wires the real ones.
type Sys struct {
	// Run runs a command to completion.
	Run func(ctx context.Context, args ...string) error
	// Spawn starts the agent detached from this process (it outlives install).
	Spawn func(args ...string) error
	Reg   Registry
	Login LoginItems
	Web   Browsers
	// Portal forgets a GNOME/KDE typing grant made under the old app id (portal.MigrateLegacy).
	Portal func() (bool, error)
	// Out gets a line per notable step (nil: quiet).
	Out io.Writer
}

func (s Sys) say(format string, a ...any) {
	if s.Out != nil {
		fmt.Fprintf(s.Out, format+"\n", a...)
	}
}

// ApplyOptions say how to apply.
type ApplyOptions struct {
	FilesOnly  bool // skip Activate steps
	BestEffort bool // keep going after a failure and return them all (uninstall)
}

// Apply carries out ops. Install stops at the first failure; uninstall (BestEffort) does
// everything it can.
func Apply(ctx context.Context, ops []Op, s Sys, o ApplyOptions) error {
	var errs []error
	for _, op := range ops {
		if op.Activate && o.FilesOnly {
			continue
		}
		err := s.apply(ctx, op)
		if err == nil || op.IgnoreErr {
			continue
		}
		err = fmt.Errorf("%s %s: %w", op.Kind, firstNonEmpty(op.Path, op.Key, strings.Join(op.Args, " "), op.Name), err)
		if !o.BestEffort {
			return err
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func (s Sys) apply(ctx context.Context, op Op) error {
	switch op.Kind {
	case CopyExe:
		return copyExe(op.Target, op.Path, op.Mode)
	case Write:
		if op.Key == IfDirExists {
			if st, err := os.Stat(filepath.Dir(op.Path)); err != nil || !st.IsDir() {
				s.say("No %s folder: skipped the shortcut there.", filepath.Dir(op.Path))
				return nil
			}
		}
		return writeFile(op.Path, []byte(op.Data), op.Mode)
	case Symlink:
		return symlink(op.Path, op.Target)
	case Remove:
		return remove(op.Path, op.Target)
	case RemoveAll:
		return os.RemoveAll(op.Path)
	case MimeDefault:
		return setMimeDefault(op.Path, op.Name, op.Value)
	case MimeUnset:
		if err := unsetMimeDefault(op.Path, op.Name, op.Value); err != nil {
			return err
		}
		if _, err := os.Stat(op.Path); errors.Is(err, os.ErrNotExist) {
			return remove(op.Path, op.Target) // the empty dirs it may have left
		}
		return nil
	case RegSet, RegDeleteValue, RegDeleteTree:
		if s.Reg == nil {
			return errors.New("no registry on this platform")
		}
		switch op.Kind {
		case RegSet:
			return s.Reg.SetString(op.Key, op.Name, op.Value)
		case RegDeleteValue:
			return s.Reg.DeleteValue(op.Key, op.Name)
		default:
			return s.Reg.DeleteTree(op.Key)
		}
	case Shortcut:
		return s.Run(ctx, powershell(shortcutScript(op, false))...)
	case ShortcutRemove:
		return s.Run(ctx, powershell(shortcutScript(op, true))...)
	case BrowserHosts:
		if s.Web == nil {
			return nil
		}
		return s.Web.Register(op.Target)
	case BrowserHostsGone:
		if s.Web == nil {
			return nil
		}
		return s.Web.Unregister()
	case LoginItem, LoginItemGone:
		if op.Path == "" {
			return errors.New("not running from the app bundle")
		}
		if s.Login == nil {
			return errors.New("SMAppService isn't available")
		}
		if op.Kind == LoginItem {
			if _, err := os.Stat(filepath.Join(op.Path, "Contents", "Library", "LaunchAgents", op.Name)); err != nil {
				return fmt.Errorf("the app has no LaunchAgent to register: %w", err)
			}
			return s.Login.Register(op.Name)
		}
		return s.Login.Unregister(op.Name)
	case PortalMigrate:
		if s.Portal == nil {
			return nil
		}
		did, err := s.Portal()
		if did {
			s.say("The agent's app id changed: allow typing again with `jarvis-agent dictation allow`.")
		}
		return err
	case Exec:
		if op.Path != "" {
			// Only when that file exists (e.g. a LaunchAgent an earlier install wrote).
			if _, err := os.Lstat(op.Path); err != nil {
				return nil
			}
		}
		return s.Run(ctx, op.Args...)
	case StartAgent:
		return s.start(ctx, op)
	case StopAgent:
		return s.stop(ctx, op)
	}
	return fmt.Errorf("unknown step %q", op.Kind)
}

// start starts the agent the way op.Name says.
func (s Sys) start(ctx context.Context, op Op) error {
	switch op.Name {
	case "systemd-or-spawn":
		// The unit is only right when the graphical session's target is up: then systemd has the
		// display variables and supervises the agent. Otherwise (no systemd, or a compositor
		// that never starts the target) this session's own environment does.
		if s.Run(ctx, "systemctl", "--user", "is-active", "--quiet", "graphical-session.target") == nil {
			return s.Run(ctx, "systemctl", "--user", "restart", op.Key)
		}
		return s.Spawn(op.Args...)
	case "launchd":
		return s.Run(ctx, op.Args...)
	case "spawn":
		return s.Spawn(op.Args...)
	}
	return fmt.Errorf("unknown start %q", op.Name)
}

func (s Sys) stop(ctx context.Context, op Op) error {
	switch op.Name {
	case "systemd":
		return s.Run(ctx, "systemctl", "--user", "stop", op.Key)
	}
	return fmt.Errorf("unknown stop %q", op.Name)
}

// --- files ---

func writeFile(path string, data []byte, mode fs.FileMode) error {
	if b, err := os.ReadFile(path); err == nil && string(b) == string(data) {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm() == mode.Perm() {
			return nil
		}
	}
	return writeAtomic(path, mode, func(w io.Writer) error { _, err := w.Write(data); return err })
}

func copyExe(from, to string, mode fs.FileMode) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	// Rename over the old copy: a running agent keeps its (now unlinked) file.
	return writeAtomic(to, mode, func(w io.Writer) error { _, err := io.Copy(w, src); return err })
}

func writeAtomic(path string, mode fs.FileMode, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".jarvis-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := fill(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil && !isWindows() {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func symlink(path, target string) error {
	if cur, err := os.Readlink(path); err == nil && cur == target {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(target, path)
}

// remove deletes path (a missing one is fine), then each now-empty parent directory strictly
// below stop.
func remove(path, stop string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if stop == "" {
		return nil
	}
	stop = filepath.Clean(stop)
	for dir := filepath.Dir(path); dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil { // not empty, or gone
			break
		}
	}
	return nil
}

// --- Windows shortcuts ---

// powershell runs a script without profiles or prompts.
func powershell(script string) []string {
	return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
}

// psQuote is a PowerShell single-quoted string.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// shortcutScript creates (or removes) the .lnk op.Name in the special folder op.Key, resolved
// by Windows (a Desktop redirected to OneDrive included).
func shortcutScript(op Op, remove bool) string {
	path := "(Join-Path ([Environment]::GetFolderPath(" + psQuote(op.Key) + ")) " + psQuote(op.Name+".lnk") + ")"
	if remove {
		return "$p = " + path + "; if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Force }"
	}
	return "$p = " + path + "; $l = (New-Object -ComObject WScript.Shell).CreateShortcut($p); " +
		"$l.TargetPath = " + psQuote(op.Target) + "; $l.Arguments = " + psQuote(strings.Join(op.Args, " ")) + "; " +
		"$l.WorkingDirectory = " + psQuote(filepath.Dir(op.Target)) + "; $l.Description = " + psQuote(displayName) + "; " +
		"$l.WindowStyle = 7; $l.Save()"
}
