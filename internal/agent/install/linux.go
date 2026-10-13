package install

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/deploy"
)

// linuxPaths are where the per-user install lives (installers plan §2.2, Q5).
type linuxPaths struct {
	bin, unit, wants, oldWants, menu, autostart, icon, mimeapps, apps, config, cache string
	units                                                                            string
}

func (e Env) linux() linuxPaths {
	cfg := e.getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = e.join(e.Home, ".config")
	}
	data := e.getenv("XDG_DATA_HOME")
	if data == "" {
		data = e.join(e.Home, ".local", "share")
	}
	cache := e.getenv("XDG_CACHE_HOME")
	if cache == "" {
		cache = e.join(e.Home, ".cache")
	}
	desktop := e.getenv("XDG_DESKTOP_DIR")
	if desktop == "" {
		desktop = e.join(e.Home, "Desktop")
	}
	units := e.join(cfg, "systemd", "user")
	apps := e.join(data, "applications")
	return linuxPaths{
		// The user's own, self-updatable copy (Q5): the .deb's /usr/lib copy is never run as the agent.
		bin:       e.join(e.Home, ".local", "lib", "jarvis-agent", "jarvis-agent"),
		unit:      e.join(units, deploy.SystemdUnitName),
		wants:     e.join(units, "graphical-session.target.wants", deploy.SystemdUnitName),
		oldWants:  e.join(units, "default.target.wants", deploy.SystemdUnitName),
		menu:      e.join(apps, appid.DesktopFile),
		autostart: e.join(cfg, "autostart", appid.DesktopFile),
		icon:      e.join(desktop, appid.DesktopFile),
		mimeapps:  e.join(cfg, "mimeapps.list"),
		apps:      apps,
		units:     units,
		config:    e.join(cfg, "jarvis-agent"),
		cache:     e.join(cache, "jarvis-agent"),
	}
}

// LinuxBin is where install puts the per-user copy of the agent.
func LinuxBin(e Env) string { return e.linux().bin }

func linuxPlan(e Env, o Options) []Op {
	p := e.linux()
	ops := []Op{
		{Kind: PortalMigrate},
		// scripts/agent-dev-run.sh --install enabled the unit under default.target.
		{Kind: Remove, Path: p.oldWants, Target: p.units},
	}
	if e.Exe != p.bin {
		ops = append(ops, Op{Kind: CopyExe, Target: e.Exe, Path: p.bin, Mode: 0o755})
	}
	ops = append(ops,
		Op{Kind: Write, Path: p.unit, Mode: 0o644, Data: strings.ReplaceAll(deploy.SystemdUnit, deploy.BinPlaceholder, systemdExecArg(p.bin))},
		Op{Kind: Symlink, Path: p.wants, Target: p.unit},
		Op{Kind: Write, Path: p.menu, Mode: 0o644, Data: MenuEntry(p.bin)},
		Op{Kind: Write, Path: p.autostart, Mode: 0o644, Data: AutostartEntry(p.bin)},
	)
	if o.DesktopShortcut {
		// Desktops only run a launcher on the desktop that is executable. Only into a Desktop
		// folder that exists: creating one (and later removing it) isn't ours to do.
		ops = append(ops, Op{Kind: Write, Path: p.icon, Mode: 0o755, Data: MenuEntry(p.bin), Key: IfDirExists})
	}
	ops = append(ops, Op{Kind: MimeDefault, Path: p.mimeapps, Name: mimeURLHandler, Value: appid.DesktopFile})
	if o.Browser {
		ops = append(ops, Op{Kind: BrowserHosts, Target: p.bin})
	}
	ops = append(ops,
		// Refreshes mimeinfo.cache, for tools that read only that (missing on minimal systems: fine).
		Op{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"update-desktop-database", p.apps}},
		// No systemd user manager (or none reachable) is fine: the autostart entry covers it.
		Op{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"systemctl", "--user", "daemon-reload"}},
	)
	if o.Start {
		ops = append(ops, Op{Kind: StartAgent, Activate: true, Name: "systemd-or-spawn", Key: deploy.SystemdUnitName, Args: []string{p.bin, "run"}})
	}
	return ops
}

func linuxUninstall(e Env, o UninstallOptions) []Op {
	p := e.linux()
	ops := []Op{
		{Kind: StopAgent, Activate: true, IgnoreErr: true, Name: "systemd", Key: deploy.SystemdUnitName},
		{Kind: StopAgent, Activate: true, IgnoreErr: true, Name: "control"}, // one started outside systemd
		// Only the agent's own directories go with their last file (the .wants link dir, as
		// systemctl disable does, and ~/.local/lib/jarvis-agent); shared ones like
		// ~/.config/autostart stay, empty or not.
		{Kind: Remove, Path: p.wants, Target: p.units},
		{Kind: Remove, Path: p.oldWants, Target: p.units},
		{Kind: Remove, Path: p.unit},
		{Kind: Remove, Path: p.autostart},
		{Kind: Remove, Path: p.menu},
		{Kind: Remove, Path: p.icon}, // never the Desktop folder itself
		{Kind: MimeUnset, Path: p.mimeapps, Name: mimeURLHandler, Value: appid.DesktopFile},
		{Kind: BrowserHostsGone},
		{Kind: Remove, Path: p.bin, Target: e.join(e.Home, ".local", "lib")},
		{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"systemctl", "--user", "daemon-reload"}},
		{Kind: Exec, Activate: true, IgnoreErr: true, Path: p.apps, Args: []string{"update-desktop-database", p.apps}},
		// Then the cache install's update-desktop-database made, when it is all that is left there.
		{Kind: Remove, Path: e.join(p.apps, "mimeinfo.cache"), Key: IfAlone},
	}
	if o.Purge {
		ops = append(ops, Op{Kind: RemoveAll, Path: p.config}, Op{Kind: RemoveAll, Path: p.cache})
	}
	return ops
}

// MenuEntry is the agent's freedesktop application entry: the app-menu item, the
// jarvis-agent:// URL handler, and the file the RemoteDesktop portal knows the agent's app id by
// (it must be named <app id>.desktop and its Exec program must exist). Opening it, or the URL,
// runs `start`, which never reads anything from the URL but checks it is jarvis-agent://start.
func MenuEntry(bin string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Version=1.5\n" +
		"Name=" + displayName + "\n" +
		"GenericName=Desktop agent\n" +
		"Comment=Use Jarvis on this computer: voice, browser control, dictation\n" +
		"Exec=" + desktopExecArg(bin) + " start %u\n" +
		"TryExec=" + desktopString(bin) + "\n" +
		"Terminal=false\n" +
		"StartupNotify=false\n" +
		"Categories=Utility;\n" +
		"MimeType=" + mimeURLHandler + ";\n"
}

// AutostartEntry starts the agent at login on desktops that don't start
// graphical-session.target (the systemd unit's trigger). X-systemd-skip keeps
// systemd-xdg-autostart-generator (GNOME, KDE, uwsm) from running it as well; where it runs
// anyway, `start --login` leaves an agent systemd started alone.
func AutostartEntry(bin string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Version=1.5\n" +
		"Name=" + displayName + "\n" +
		"Comment=Starts the Jarvis desktop agent at login\n" +
		"Exec=" + desktopExecArg(bin) + " start --login\n" +
		"TryExec=" + desktopString(bin) + "\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"X-systemd-skip=true\n" +
		"X-GNOME-Autostart-enabled=true\n"
}

// desktopExecArg quotes one Exec argument (Desktop Entry spec, "The Exec key"): quoted when it
// has a reserved character, with ", `, $ and \ escaped inside; % doubled; then the string
// value's own escaping (\ → \\).
func desktopExecArg(s string) string {
	q := s
	if strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`") {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
		q = `"` + r.Replace(s) + `"`
	}
	q = strings.ReplaceAll(q, "%", "%%")
	return desktopString(q)
}

// desktopString escapes a string value (Desktop Entry spec, "Possible value types").
func desktopString(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`).Replace(s)
}

// systemdExecArg quotes a path for ExecStart= (systemd.service, "Command lines").
func systemdExecArg(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\%$") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}
