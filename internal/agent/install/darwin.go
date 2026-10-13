package install

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/deploy"
)

// lsregister registers an app's URL schemes with LaunchServices (it does this by itself when
// the app is first opened or copied into /Applications; install doesn't wait for that).
const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// Login item kinds on macOS.
const (
	LoginAuto         = "auto"
	LoginSMAppService = "smappservice"
	LoginLaunchAgent  = "launchagent"
)

// BundlePlistName is the LaunchAgent inside the app (Contents/Library/LaunchAgents) that
// SMAppService registers.
const BundlePlistName = appid.ID + ".plist"

type darwinPaths struct {
	bundle, plist, oldPlist, logs, config, cache, domain string
}

func (e Env) darwin() darwinPaths {
	la := e.join(e.Home, "Library", "LaunchAgents")
	return darwinPaths{
		bundle:   BundleOf(e.Exe),
		plist:    e.join(la, appid.ID+".plist"),
		oldPlist: e.join(la, appid.LegacyID+".plist"),
		logs:     e.join(e.Home, "Library", "Logs"),
		config:   e.join(e.Home, "Library", "Application Support", "jarvis-agent"),
		cache:    e.join(e.Home, "Library", "Caches", "jarvis-agent"),
		domain:   "gui/" + strconv.Itoa(e.UID),
	}
}

// BundleOf is the .app exe runs from ("…/X.app/Contents/MacOS/jarvis-agent" → "…/X.app"), or "".
func BundleOf(exe string) string {
	i := strings.LastIndex(exe, ".app/Contents/MacOS/")
	if i < 0 || strings.Contains(exe[i+len(".app/Contents/MacOS/"):], "/") {
		return ""
	}
	return exe[:i+len(".app")]
}

func darwinPlan(e Env, o Options) ([]Op, error) {
	p := e.darwin()
	mode := o.LoginItem
	if mode == "" || mode == LoginAuto {
		mode = LoginLaunchAgent
		if p.bundle != "" && e.SMAppService {
			mode = LoginSMAppService
		}
	}
	ops := []Op{
		// What scripts/agent-dev-run.sh --install made under the old app id.
		{Kind: Exec, Activate: true, IgnoreErr: true, Path: p.oldPlist, Args: []string{"launchctl", "bootout", p.domain + "/" + appid.LegacyID}},
		{Kind: Remove, Path: p.oldPlist},
	}
	switch mode {
	case LoginSMAppService:
		if p.bundle == "" {
			return nil, fmt.Errorf("install: a login item through SMAppService needs the agent run from %s.app (this is %s)", displayName, e.Exe)
		}
		if !e.SMAppService {
			return nil, fmt.Errorf("install: SMAppService needs macOS 13 or later; use --login-item %s", LoginLaunchAgent)
		}
		ops = append(ops,
			// A plain LaunchAgent from an earlier install would run a second copy.
			Op{Kind: Exec, Activate: true, IgnoreErr: true, Path: p.plist, Args: []string{"launchctl", "bootout", p.domain + "/" + appid.ID}},
			Op{Kind: Remove, Path: p.plist},
			Op{Kind: LoginItem, Activate: true, Path: p.bundle, Name: BundlePlistName},
		)
	case LoginLaunchAgent:
		data := strings.ReplaceAll(deploy.LaunchAgent, deploy.BinPlaceholder, xmlText(e.Exe))
		data = strings.ReplaceAll(data, deploy.LogDirPlaceholder, xmlText(p.logs))
		if p.bundle != "" {
			// Switching from SMAppService: one login item, not two.
			ops = append(ops, Op{Kind: LoginItemGone, Activate: true, IgnoreErr: true, Path: p.bundle, Name: BundlePlistName})
		}
		ops = append(ops, Op{Kind: Write, Path: p.plist, Mode: 0o644, Data: data})
	default:
		return nil, fmt.Errorf("install: --login-item is %s, %s or %s, not %q", LoginAuto, LoginSMAppService, LoginLaunchAgent, mode)
	}
	if p.bundle != "" {
		// The URL scheme is the bundle's (CFBundleURLTypes); this makes LaunchServices know it now.
		ops = append(ops, Op{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{lsregister, "-f", p.bundle}})
	}
	if o.Browser {
		ops = append(ops, Op{Kind: BrowserHosts, Target: e.Exe})
	}
	if o.Start {
		if mode == LoginSMAppService {
			// Registering started it; -k restarts an agent an earlier install left running.
			ops = append(ops, Op{Kind: StartAgent, Activate: true, Name: "launchd", Args: []string{"launchctl", "kickstart", "-k", p.domain + "/" + appid.ID}})
		} else {
			ops = append(ops,
				Op{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"launchctl", "bootout", p.domain + "/" + appid.ID}},
				Op{Kind: StartAgent, Activate: true, Name: "launchd", Args: []string{"launchctl", "bootstrap", p.domain, p.plist}})
		}
	}
	return ops, nil
}

func darwinUninstall(e Env, o UninstallOptions) []Op {
	p := e.darwin()
	var ops []Op
	if p.bundle != "" {
		// SMAppService can only be asked from inside the app; from elsewhere the app's own
		// removal (Trash) takes its login item with it.
		ops = append(ops, Op{Kind: LoginItemGone, Activate: true, IgnoreErr: true, Path: p.bundle, Name: BundlePlistName})
	}
	ops = append(ops, []Op{
		{Kind: StopAgent, Activate: true, IgnoreErr: true, Name: "control"},
		{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{"launchctl", "bootout", p.domain + "/" + appid.ID}},
		{Kind: Remove, Path: p.plist},
		{Kind: Exec, Activate: true, IgnoreErr: true, Path: p.oldPlist, Args: []string{"launchctl", "bootout", p.domain + "/" + appid.LegacyID}},
		{Kind: Remove, Path: p.oldPlist},
		{Kind: BrowserHostsGone},
	}...)
	if p.bundle != "" {
		ops = append(ops, Op{Kind: Exec, Activate: true, IgnoreErr: true, Args: []string{lsregister, "-u", p.bundle}})
	}
	if o.Purge {
		ops = append(ops,
			Op{Kind: RemoveAll, Path: p.config},
			Op{Kind: RemoveAll, Path: p.cache},
			Op{Kind: Remove, Path: e.join(p.logs, "jarvis-agent.log")},
			Op{Kind: Remove, Path: e.join(p.logs, "jarvis-agent.err.log")},
		)
	}
	return ops
}

// xmlText escapes s for a plist <string>.
func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
