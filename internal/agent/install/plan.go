// Package install is `jarvis-agent install` and `uninstall` (installers plan item 1.4): the
// per-user setup every agent installer ends in. It registers the login item, the menu entry and
// shortcut, the jarvis-agent:// URL handler and the browsers' native-messaging host, then
// starts the agent; uninstall reverses all of it.
//
// It works in two steps so every OS can be checked from any OS: Plan computes, from an Env
// (home, environment, the running binary) and Options, the list of operations for an OS as pure
// data (golden-tested for linux, darwin and windows on every runner), and Apply carries them out
// against the filesystem and a Sys (commands, the Windows registry, SMAppService, the browser
// hosts), which tests replace with fakes.
//
// Both are idempotent: installing twice gives the same result as once, and uninstall removes
// every file install made (and the agent's own directories), keeping the agent's data unless purging.
package install

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// Kind is what an Op does.
type Kind string

const (
	CopyExe          Kind = "copy-exe"           // copy Target (the running binary) to Path, 0755, atomically
	Write            Kind = "write"              // write Data to Path with Mode, atomically
	Symlink          Kind = "symlink"            // Path -> Target
	Remove           Kind = "remove"             // remove the file or link at Path (missing is fine), then its empty parent dirs below Target (if set)
	RemoveAll        Kind = "remove-all"         // remove the directory Path and everything in it (purge)
	MimeDefault      Kind = "mime-default"       // in mimeapps.list at Path: Name=Value under [Default Applications]
	MimeUnset        Kind = "mime-unset"         // remove Name from Path's [Default Applications] if it is Value
	RegSet           Kind = "reg-set"            // HKCU\Key, value Name ("" = default) = Value
	RegDeleteValue   Kind = "reg-delete-value"   // HKCU\Key, value Name
	RegDeleteTree    Kind = "reg-delete-tree"    // HKCU\Key and its subkeys
	Shortcut         Kind = "shortcut"           // Windows .lnk named Name in special folder Key, to Target with Args
	ShortcutRemove   Kind = "shortcut-remove"    // that .lnk
	BrowserHosts     Kind = "browser-hosts"      // register the native-messaging host for Target (found browsers)
	BrowserHostsGone Kind = "browser-hosts-gone" // unregister it everywhere
	LoginItem        Kind = "login-item"         // macOS: SMAppService.agent(plistName: Name).register()
	LoginItemGone    Kind = "login-item-gone"    // its unregister()
	PortalMigrate    Kind = "portal-migrate"     // forget a GNOME/KDE typing grant made under the old app id
	Exec             Kind = "exec"               // run Args
	StartAgent       Kind = "start"              // start (or restart) the agent: Name says how
	StopAgent        Kind = "stop"               // stop a running agent: Name says how
)

// IfDirExists, as a Write's Key, skips the write when the file's directory doesn't exist.
const IfDirExists = "if-dir-exists"

// Op is one step. Activate marks steps that touch the running session (the service manager,
// LaunchServices, processes); Options.FilesOnly skips them.
type Op struct {
	Kind      Kind
	Path      string
	Target    string
	Data      string
	Mode      fs.FileMode
	Key       string
	Name      string
	Value     string
	Args      []string
	IgnoreErr bool
	Activate  bool
}

// Env is the machine the plan is for. Nothing here is read from the running OS, so a plan for
// any OS can be computed (and golden-tested) anywhere.
type Env struct {
	GOOS   string
	Home   string
	UID    int    // the launchd GUI domain, gui/<UID>
	Exe    string // the running jarvis-agent, absolute, symlinks resolved
	Getenv func(string) string
	// SMAppService: macOS 13 or later (the in-bundle login item).
	SMAppService bool
}

func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

// join builds a path in the target OS's syntax.
func (e Env) join(elem ...string) string {
	if e.GOOS == "windows" {
		var parts []string
		for _, p := range elem {
			if p = strings.Trim(p, `\`); p != "" || len(parts) == 0 {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, `\`)
	}
	return path.Join(elem...)
}

// Options are install's choices.
type Options struct {
	Browser         bool   // register the native-messaging host (the default: DESIGN §13)
	DesktopShortcut bool   // a desktop icon as well as the menu entry (Windows default: yes)
	Start           bool   // start the agent afterwards
	FilesOnly       bool   // skip Activate steps: no service manager, LaunchServices or processes
	LoginItem       string // macOS: "auto" (SMAppService when run from the app, else a LaunchAgent), "smappservice", "launchagent"
}

// UninstallOptions are uninstall's.
type UninstallOptions struct {
	Purge     bool // also delete the agent's data: credentials, settings, caches, logs
	FilesOnly bool
}

// Names shared by the plans.
const (
	urlScheme      = "jarvis-agent"
	mimeURLHandler = "x-scheme-handler/" + urlScheme
	// StartURL is the one URL the handler accepts.
	StartURL = urlScheme + "://start"
)

// Plan is install's operations for e.GOOS.
func Plan(e Env, o Options) ([]Op, error) {
	if e.Exe == "" || e.Home == "" {
		return nil, fmt.Errorf("install: the binary and home directory must be known")
	}
	switch e.GOOS {
	case "linux":
		return linuxPlan(e, o), nil
	case "darwin":
		return darwinPlan(e, o)
	case "windows":
		return windowsPlan(e, o), nil
	}
	return nil, fmt.Errorf("install: %s isn't supported", e.GOOS)
}

// UninstallPlan is uninstall's operations for e.GOOS.
func UninstallPlan(e Env, o UninstallOptions) ([]Op, error) {
	if e.Home == "" {
		return nil, fmt.Errorf("uninstall: the home directory must be known")
	}
	switch e.GOOS {
	case "linux":
		return linuxUninstall(e, o), nil
	case "darwin":
		return darwinUninstall(e, o), nil
	case "windows":
		return windowsUninstall(e, o), nil
	}
	return nil, fmt.Errorf("uninstall: %s isn't supported", e.GOOS)
}

// Dump renders ops as text with the home directory written as ~, for golden files and
// --dry-run.
func Dump(e Env, ops []Op) string {
	home := func(s string) string {
		if e.Home != "" {
			s = strings.ReplaceAll(s, e.Home, "~")
		}
		return s
	}
	var b strings.Builder
	for _, op := range ops {
		fmt.Fprintf(&b, "%s", op.Kind)
		if op.Activate {
			b.WriteString(" [activate]")
		}
		if op.IgnoreErr {
			b.WriteString(" [ignore-errors]")
		}
		for _, kv := range [][2]string{{"path", op.Path}, {"target", op.Target}, {"key", op.Key}, {"name", op.Name}, {"value", op.Value}} {
			if kv[1] != "" {
				fmt.Fprintf(&b, " %s=%q", kv[0], home(kv[1]))
			}
		}
		if op.Mode != 0 {
			fmt.Fprintf(&b, " mode=%#o", op.Mode)
		}
		if len(op.Args) > 0 {
			fmt.Fprintf(&b, " args=%q", home(strings.Join(op.Args, " ")))
		}
		b.WriteString("\n")
		if op.Data != "" {
			for _, l := range strings.Split(strings.TrimRight(home(op.Data), "\n"), "\n") {
				b.WriteString("  | " + l + "\n")
			}
		}
	}
	return b.String()
}

// displayName is the app's name everywhere a person sees it.
const displayName = appid.DisplayName
