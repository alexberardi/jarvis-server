package install

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// Registry keys (relative to HKEY_CURRENT_USER: nothing here needs admin rights).
const (
	// RunKey starts programs at logon (installers plan §2.2: HKCU Run, the logon task is the
	// fallback if Run proves too early for audio).
	RunKey = `Software\Microsoft\Windows\CurrentVersion\Run`
	// URLKey is the jarvis-agent: URL scheme.
	URLKey = `Software\Classes\` + urlScheme
)

// Special folders for shortcuts ([Environment]::GetFolderPath names).
const (
	FolderPrograms = "Programs" // Start menu
	FolderDesktop  = "Desktop"
)

type windowsPaths struct{ config, cache string }

func (e Env) windows() windowsPaths {
	roaming := e.getenv("APPDATA")
	if roaming == "" {
		roaming = e.join(e.Home, "AppData", "Roaming")
	}
	local := e.getenv("LOCALAPPDATA")
	if local == "" {
		local = e.join(e.Home, "AppData", "Local")
	}
	return windowsPaths{config: e.join(roaming, "jarvis-agent"), cache: e.join(local, "jarvis-agent")}
}

// winQuote quotes a command-line argument for the registry's command strings (paths can't
// contain a double quote on Windows).
func winQuote(s string) string { return `"` + s + `"` }

// LauncherName is the windowless launcher (cmd/jarvis-agentw) the Windows installer puts beside
// jarvis-agent.exe. Where it is, the login item, shortcuts and URL handler start the agent
// through it, so no console window opens.
const LauncherName = "jarvis-agentw.exe"

func windowsPlan(e Env, o Options) []Op {
	exe := e.Exe
	// What the login item, the shortcuts and the URL handler run.
	launch := exe
	if e.Launcher != "" {
		launch = e.Launcher
	}
	ops := []Op{
		// The logon task an earlier dev setup may have registered by hand.
		{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"schtasks", "/Delete", "/TN", appid.LegacyWindowsTaskName, "/F"}},
		{Kind: RegSet, Key: RunKey, Name: displayName, Value: winQuote(launch) + " run"},
		{Kind: RegSet, Key: URLKey, Value: "URL:" + displayName},
		{Kind: RegSet, Key: URLKey, Name: "URL Protocol", Value: ""},
		{Kind: RegSet, Key: URLKey + `\DefaultIcon`, Value: winQuote(exe) + ",0"},
		// The handler is start: it checks the URL is jarvis-agent://start and reads nothing from it.
		{Kind: RegSet, Key: URLKey + `\shell\open\command`, Value: winQuote(launch) + ` start "%1"`},
		{Kind: Shortcut, Key: FolderPrograms, Name: displayName, Target: launch, Args: []string{"start"}},
	}
	if o.DesktopShortcut {
		ops = append(ops, Op{Kind: Shortcut, Key: FolderDesktop, Name: displayName, Target: launch, Args: []string{"start"}})
	}
	if o.Browser {
		ops = append(ops, Op{Kind: BrowserHosts, Target: exe})
	}
	if o.Start {
		ops = append(ops, Op{Kind: StartAgent, Activate: true, Name: "spawn", Args: []string{exe, "run"}})
	}
	return ops
}

func windowsUninstall(e Env, o UninstallOptions) []Op {
	p := e.windows()
	ops := []Op{
		{Kind: StopAgent, Activate: true, IgnoreErr: true, Name: "control"},
		{Kind: RegDeleteValue, Key: RunKey, Name: displayName},
		{Kind: RegDeleteTree, Key: URLKey},
		{Kind: ShortcutRemove, Key: FolderPrograms, Name: displayName},
		{Kind: ShortcutRemove, Key: FolderDesktop, Name: displayName},
		{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"schtasks", "/Delete", "/TN", appid.LegacyWindowsTaskName, "/F"}},
		{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"schtasks", "/Delete", "/TN", appid.WindowsTaskName, "/F"}},
		{Kind: BrowserHostsGone},
	}
	if o.Purge {
		ops = append(ops, Op{Kind: RemoveAll, Path: p.config}, Op{Kind: RemoveAll, Path: p.cache})
	}
	return ops
}

// UninstallKey is the Windows installer's entry in Apps & features for this user (Inno Setup
// names it after its AppId, appid.ID, plus "_is1"; scripts/packaging/windows/agent.iss).
const UninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + appid.ID + `_is1`

// RecordVersion sets the uninstall entry's DisplayVersion to version (without its "v") after
// the agent updated itself, so Apps & features shows the version that runs. Nothing is written
// when the installer made no entry (a dev build, the archive): ok reports whether one was there.
func RecordVersion(r Registry, version string) (ok bool, err error) {
	if r == nil {
		return false, nil
	}
	if _, ok, err := r.GetString(UninstallKey, "DisplayName"); err != nil || !ok {
		return false, err
	}
	return true, r.SetString(UninstallKey, "DisplayVersion", strings.TrimPrefix(version, "v"))
}
