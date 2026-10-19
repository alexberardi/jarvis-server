package computeruse

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// The built-in deny list (COMPUTER-CONTROL §Safety): what Jarvis never drives, whatever the
// household's rules say. App names are compared after appKey: lower case, without ".app"/".exe".

var terminalApps = set(
	"com.apple.terminal", "terminal",
	"com.googlecode.iterm2", "iterm2", "iterm",
	"dev.warp.warp-stable", "dev.warp.warp", "warp",
	"com.mitchellh.ghostty", "ghostty",
	"net.kovidgoyal.kitty", "kitty",
	"org.alacritty", "alacritty",
	"org.codeberg.dnkl.foot", "foot", "footclient",
	"org.wezfurlong.wezterm", "wezterm", "wezterm-gui",
	"org.gnome.terminal", "gnome-terminal", "gnome-terminal-server",
	"org.kde.konsole", "konsole",
	"xterm", "uxterm",
	"com.gexperts.tilix", "tilix",
	// also GNOME's newer terminals and Windows' shells
	"org.gnome.ptyxis", "ptyxis", "org.gnome.console", "kgx",
	"windowsterminal", "microsoft.windowsterminal", "cmd", "powershell", "pwsh", "conhost",
)

var jarvisApps = set(
	appid.ID, appid.LegacyID, appid.DisplayName, appid.LegacyDisplayName,
	"jarvis-agent", "jarvis-island", "jarvisd", "net.jarvisautomation.jarvisd",
)

// authApps are OS authentication and admin prompts. Any app id containing "polkit" is one too
// (hyprpolkitagent, polkit-gnome-authentication-agent-1, lxpolkit, polkit-kde-…).
var authApps = set(
	"com.apple.securityagent", "securityagent",
	"com.apple.coreauthd", "coreauthd", "com.apple.coreautha", "coreautha",
	"consent", "credentialuibroker",
)

const localAuthPrefix = "com.apple.localauthentication"

var settingsApps = set(
	"com.apple.systempreferences", "system settings", "system preferences",
	"gnome-control-center", "org.gnome.settings", "systemsettings", "org.kde.systemsettings",
)

// settingsPaneWords name the System Settings panes Jarvis never drives (whole words, in a
// settings app only).
var settingsPaneWords = []string{"users", "passwords", "password", "privacy", "login items", "touch id"}

// settingsPaneIDs are the same panes' macOS extension ids, matched as substrings of the app.
var settingsPaneIDs = []string{"privacysecurity", "users-groups", "passwords-settings", "loginitems", "touchid"}

// powerWords are lock, log out, shut down, restart and quit all, on desktop elements (menu items,
// the shell's power menu). Websites' own "Log out" buttons are not on the list.
var powerWords = []string{
	"shut down", "shutdown", "power off", "restart", "reboot", "log out", "logout",
	"lock screen", "quit all",
}

// powerChords are keypresses that lock, log out or force-quit, as sorted key sets.
var powerChords = [][]string{
	{"alt", "ctrl", "delete"},
	{"cmd", "ctrl", "q"}, {"ctrl", "meta", "q"},
	{"cmd", "q", "shift"}, {"meta", "q", "shift"},
	{"l", "super"},
}

var (
	originsMu     sync.RWMutex
	serverOrigins []string // siteKey of each
)

// SetServerOrigins records the Jarvis server's origins ("https://host:port"), which browser
// targets may not act on. Call it once the server URL is known; a later call replaces the list.
func SetServerOrigins(origins ...string) {
	keys := make([]string, 0, len(origins))
	for _, o := range origins {
		if k := siteKey(o); k != "" {
			keys = append(keys, k)
		}
	}
	originsMu.Lock()
	serverOrigins = keys
	originsMu.Unlock()
}

// BuiltinDeny is whether t (and el, the element acted on, or nil) is on the built-in deny list,
// and why. Keypress chords are DenyAction's.
func BuiltinDeny(t Target, el *Element) (bool, string) {
	if t.Kind == "browser" {
		if siteIsServer(t.Site) {
			return true, fmt.Sprintf("%s is the Jarvis server", t.Site)
		}
		return false, ""
	}
	app := appKey(t.App)
	switch {
	case app == "":
	case terminalApps[app]:
		return true, fmt.Sprintf("%s is a terminal", t.App)
	case jarvisApps[app]:
		return true, fmt.Sprintf("%s is Jarvis itself", t.App)
	case authApps[app], strings.HasPrefix(app, localAuthPrefix), strings.Contains(app, "polkit"):
		return true, fmt.Sprintf("%s is an OS authentication prompt", t.App)
	}
	if strings.HasPrefix(app, "com.apple.") {
		for _, id := range settingsPaneIDs {
			if strings.Contains(app, id) {
				return true, fmt.Sprintf("%s is a protected System Settings pane", t.App)
			}
		}
	}
	if el == nil {
		return false, ""
	}
	ws := words(el.Name)
	if settingsApps[app] {
		if w := firstPhrase(ws, settingsPaneWords); w != "" {
			return true, fmt.Sprintf("%q is a protected settings pane", el.Name)
		}
	}
	if w := firstPhrase(ws, powerWords); w != "" {
		return true, fmt.Sprintf("%q would %s", el.Name, w)
	}
	return false, ""
}

// DenyAction is whether the action itself is on the built-in deny list (lock, log out and
// force-quit chords), whatever it lands on.
func DenyAction(a Action, _ Target) (bool, string) {
	if a.Kind != "keypress" || len(a.Keys) == 0 {
		return false, ""
	}
	keys := make([]string, len(a.Keys))
	for i, k := range a.Keys {
		keys[i] = strings.ToLower(k)
	}
	slices.Sort(keys)
	for _, c := range powerChords {
		if slices.Equal(keys, c) {
			return true, fmt.Sprintf("%s locks, logs out or force-quits", strings.Join(a.Keys, "+"))
		}
	}
	return false, ""
}

func appKey(app string) string {
	k := strings.ToLower(strings.TrimSpace(app))
	k = strings.TrimSuffix(k, ".app")
	return strings.TrimSuffix(k, ".exe")
}

// siteKey is the lower-case host[:port] of a site or origin: "https://h:1/x" and "h:1" are "h:1".
func siteKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		return u.Host
	}
	host, _, _ := strings.Cut(s, "/")
	return host
}

func siteIsServer(site string) bool {
	k := siteKey(site)
	if k == "" {
		return false
	}
	originsMu.RLock()
	defer originsMu.RUnlock()
	return slices.Contains(serverOrigins, k)
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[strings.ToLower(it)] = true
	}
	return m
}
