package computeruse

import "testing"

func TestBuiltinDeny(t *testing.T) {
	SetServerOrigins("https://jarvis.example:8443", "http://192.0.2.10:7700", "https://jarvis.home.example")
	defer SetServerOrigins()

	app := func(a string) Target { return Target{Kind: "desktop", App: a} }
	site := func(s string) Target { return Target{Kind: "browser", Site: s} }
	settings := app("com.apple.systempreferences")

	cases := []struct {
		name string
		t    Target
		el   *Element
		deny bool
	}{
		// terminals, by bundle id, app id and name
		{"Terminal", app("com.apple.Terminal"), nil, true},
		{"iTerm2", app("com.googlecode.iterm2"), nil, true},
		{"iTerm2 name", app("iTerm2"), nil, true},
		{"Warp", app("dev.warp.Warp-Stable"), nil, true},
		{"Ghostty", app("com.mitchellh.ghostty"), nil, true},
		{"kitty", app("kitty"), nil, true},
		{"Alacritty", app("Alacritty"), nil, true},
		{"foot", app("foot"), nil, true},
		{"footclient", app("footclient"), nil, true},
		{"WezTerm", app("org.wezfurlong.wezterm"), nil, true},
		{"gnome-terminal", app("org.gnome.Terminal"), nil, true},
		{"gnome-terminal name", app("gnome-terminal-server"), nil, true},
		{"Konsole", app("org.kde.konsole"), nil, true},
		{"xterm", app("XTerm"), nil, true},
		{"Tilix", app("com.gexperts.Tilix"), nil, true},
		{"Terminal.app", app("Terminal.app"), nil, true},
		// Jarvis itself
		{"jarvis-agent", app("jarvis-agent"), nil, true},
		{"agent bundle", app("net.jarvisautomation.agent"), nil, true},
		{"agent legacy", app("io.jarvis.agent"), nil, true},
		{"island", app("jarvis-island"), nil, true},
		{"server origin", site("jarvis.example:8443"), nil, true},
		{"server origin as URL", site("https://jarvis.example:8443/admin"), nil, true},
		{"server by ip", site("192.0.2.10:7700"), nil, true},
		{"server bare host", site("jarvis.example"), nil, true},
		{"server bare ip", site("192.0.2.10"), nil, true},
		{"server default port", site("jarvis.home.example:443"), nil, true},
		{"server default port as URL", site("https://jarvis.home.example/"), nil, true},
		{"server bare host, no port origin", site("JARVIS.home.example"), nil, true},
		{"server host other port", site("jarvis.example:9000"), nil, false},
		{"server host http port", site("jarvis.home.example:80"), nil, false},
		{"other host same port", site("shop.example:8443"), nil, false},
		{"other ip same port", site("192.0.2.11:7700"), nil, false},
		// OS authentication prompts
		{"SecurityAgent", app("com.apple.SecurityAgent"), nil, true},
		{"coreauthd", app("com.apple.coreauthd"), nil, true},
		{"LocalAuthentication UI", app("com.apple.LocalAuthentication.UIAgent"), nil, true},
		{"hyprpolkitagent", app("hyprpolkitagent"), nil, true},
		{"polkit-gnome", app("polkit-gnome-authentication-agent-1"), nil, true},
		{"lxpolkit", app("lxpolkit"), nil, true},
		{"polkit-kde", app("org.kde.polkit-kde-authentication-agent-1"), nil, true},
		// System Settings panes
		{"Users & Groups", settings, &Element{Role: "row", Name: "Users & Groups"}, true},
		{"Passwords", settings, &Element{Role: "row", Name: "Passwords"}, true},
		{"Privacy & Security", app("System Settings"), &Element{Role: "row", Name: "Privacy & Security"}, true},
		{"Login Items", settings, &Element{Role: "row", Name: "Login Items & Extensions"}, true},
		{"pane extension", app("com.apple.settings.PrivacySecurity.extension"), nil, true},
		{"gnome users", app("gnome-control-center"), &Element{Role: "list item", Name: "Users"}, true},
		{"settings wallpaper", settings, &Element{Role: "row", Name: "Wallpaper"}, false},
		{"settings no element", settings, nil, false},
		{"passwords in another app", app("com.apple.Notes"), &Element{Role: "button", Name: "Passwords"}, false},
		// lock, logout, shutdown, restart, quit all
		{"shut down menu item", app("com.apple.finder"), &Element{Role: "menu item", Name: "Shut Down…"}, true},
		{"log out menu item", app("com.apple.finder"), &Element{Role: "menu item", Name: "Log Out Alex…"}, true},
		{"lock screen", app("com.apple.finder"), &Element{Role: "menu item", Name: "Lock Screen"}, true},
		{"restart", app("org.gnome.Shell"), &Element{Role: "button", Name: "Restart"}, true},
		{"sign out, Windows Start", app("explorer.exe"), &Element{Role: "menu item", Name: "Sign out"}, true},
		{"sign out, ChromeOS shelf", app("org.chromium.shell"), &Element{Role: "button", Name: "Sign out"}, true},
		{"lock, Windows Start", app("explorer.exe"), &Element{Role: "menu item", Name: "Lock"}, true},
		{"lock, GNOME power menu", app("org.gnome.Shell"), &Element{Role: "button", Name: "Lock"}, true},
		{"unlock is not lock", app("com.apple.Notes"), &Element{Role: "button", Name: "Unlock"}, false},
		{"log out on a site", site("shop.example"), &Element{Role: "button", Name: "Log out"}, false},
		{"sign out on a site", site("shop.example"), &Element{Role: "button", Name: "Sign out"}, false},
		// allowed
		{"Notes", app("com.apple.Notes"), nil, false},
		{"Finder", app("com.apple.finder"), &Element{Role: "button", Name: "Open"}, false},
		{"shop", site("shop.example"), nil, false},
		{"terminal named site", site("terminal.example"), nil, false},
	}
	for _, c := range cases {
		deny, why := BuiltinDeny(c.t, c.el)
		if deny != c.deny {
			t.Errorf("%s: BuiltinDeny = %v (%q), want %v", c.name, deny, why, c.deny)
		}
		if deny && why == "" {
			t.Errorf("%s: no reason", c.name)
		}
	}
}

func TestDenyAction(t *testing.T) {
	desk := Target{Kind: "desktop", App: "com.apple.Notes"}
	keys := func(k ...string) Action { return Action{Kind: "keypress", Intent: "x", Keys: k} }
	cases := []struct {
		name string
		a    Action
		deny bool
	}{
		{"ctrl+alt+delete", keys("ctrl", "alt", "delete"), true},
		{"order does not matter", keys("Delete", "Alt", "Ctrl"), true},
		{"cmd+ctrl+q", keys("cmd", "ctrl", "q"), true},
		{"meta+ctrl+q", keys("meta", "ctrl", "q"), true},
		{"cmd+shift+q", keys("cmd", "shift", "q"), true},
		{"super+l", keys("super", "l"), true},
		{"cmd+l is the address bar", keys("cmd", "l"), false},
		{"ctrl+s", keys("ctrl", "s"), false},
		{"cmd+q quits one app", keys("cmd", "q"), false},
		{"typing the words", Action{Kind: "type", Intent: "x", Text: "ctrl alt delete"}, false},
	}
	for _, c := range cases {
		deny, why := DenyAction(c.a, desk)
		if deny != c.deny {
			t.Errorf("%s: DenyAction = %v (%q), want %v", c.name, deny, why, c.deny)
		}
	}
}
