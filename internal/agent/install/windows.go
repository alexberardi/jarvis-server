package install

import (
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

func windowsPlan(e Env, o Options) []Op {
	exe := e.Exe
	ops := []Op{
		// The logon task an earlier dev setup may have registered by hand.
		{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"schtasks", "/Delete", "/TN", appid.LegacyWindowsTaskName, "/F"}},
		{Kind: RegSet, Key: RunKey, Name: displayName, Value: winQuote(exe) + " run"},
		{Kind: RegSet, Key: URLKey, Value: "URL:" + displayName},
		{Kind: RegSet, Key: URLKey, Name: "URL Protocol", Value: ""},
		{Kind: RegSet, Key: URLKey + `\DefaultIcon`, Value: winQuote(exe) + ",0"},
		// The handler is start: it checks the URL is jarvis-agent://start and reads nothing from it.
		{Kind: RegSet, Key: URLKey + `\shell\open\command`, Value: winQuote(exe) + ` start "%1"`},
		{Kind: Shortcut, Key: FolderPrograms, Name: displayName, Target: exe, Args: []string{"start"}},
	}
	if o.DesktopShortcut {
		ops = append(ops, Op{Kind: Shortcut, Key: FolderDesktop, Name: displayName, Target: exe, Args: []string{"start"}})
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
