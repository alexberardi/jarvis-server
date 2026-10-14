package jarvispage

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
)

func origins(urls ...string) *Origins {
	o := &Origins{}
	for _, u := range urls {
		o.Add(u)
	}
	return o
}

func TestMatchURL(t *testing.T) {
	o := origins("http://localhost:7700", "http://192.168.1.20:7710/", "not a url", "ftp://x:1")
	for u, want := range map[string]bool{
		"http://localhost:7700/chat":        true,
		"http://127.0.0.1:7700/":            true, // loopback names are one host
		"http://[::1]:7700/chat?x=1":        true,
		"http://192.168.1.20:7710/chat":     true,
		"https://192.168.1.20:7710/chat":    true, // scheme doesn't matter, host and port do
		"http://192.168.1.20:3000/":         false,
		"http://localhost:3000/":            false, // a dev page on another port
		"http://192.168.1.21:7710/":         false,
		"https://example.com/":              false,
		"about:blank":                       false,
		"":                                  false,
		"http://localhost.attacker.io:7700": false,
	} {
		if got := o.MatchURL(u); got != want {
			t.Errorf("%q: %v, want %v", u, got, want)
		}
	}
	if (&Origins{}).Known() || (*Origins)(nil).MatchURL("http://localhost:7700") {
		t.Error("an empty set matches")
	}
	if !origins("https://jarvis.example.com").MatchURL("https://jarvis.example.com:443/chat") {
		t.Error("default port")
	}
}

func TestMarksWindow(t *testing.T) {
	o := origins("http://localhost:7700", "http://localhost:7710")
	for _, c := range []struct {
		name  string
		apps  []string
		title string
		want  bool
	}{
		{"firefox with the web app's title", []string{"firefox"}, "Jarvis Automation — Mozilla Firefox", true},
		{"chrome showing the address", []string{"google-chrome"}, "localhost:7710/chat - Google Chrome", true},
		{"chromium app window", []string{"chrome-localhost__chat-Default"}, "Chat", true},
		{"chromium app window, another host", []string{"chrome-example.com__-Default"}, "Example", false},
		{"another page", []string{"firefox"}, "Docs — Mozilla Firefox", false},
		{"a dev page on another port", []string{"firefox"}, "localhost:3000 — Mozilla Firefox", false},
		{"a terminal mentioning the address", []string{"foot"}, "curl localhost:7710", false},
		{"a terminal titled Jarvis Automation", []string{"foot"}, "Jarvis Automation", false},
	} {
		if got := o.MarksWindow(c.apps, c.title); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGuardInFront(t *testing.T) {
	ctx := context.Background()
	o := origins("http://10.0.0.5:7700", "http://10.0.0.5:7710")
	tab := func(url, title string, ok bool) Tab {
		return func(context.Context) (string, string, bool) { return url, title, ok }
	}
	ff := func(title string) *dictate.Window {
		return &dictate.Window{ID: "1", Apps: []string{"firefox"}, Title: title}
	}
	for _, c := range []struct {
		name  string
		g     *Guard
		front *dictate.Window
		want  bool
	}{
		{"marked window, no extension", &Guard{Origins: o}, ff("Jarvis Automation — Mozilla Firefox"), true},
		{"extension: the focused tab is the page, its title in the window's", &Guard{Origins: o, Tab: tab("http://10.0.0.5:7710/chat", "Chat", true)}, ff("Chat — Mozilla Firefox"), true},
		{"extension: the page is current in another browser window", &Guard{Origins: o, Tab: tab("http://10.0.0.5:7710/chat", "Chat", true)}, ff("Docs — Mozilla Firefox"), false},
		{"extension: another site", &Guard{Origins: o, Tab: tab("https://docs.example.com/", "Docs", true)}, ff("Docs — Mozilla Firefox"), false},
		{"extension: no browser has focus", &Guard{Origins: o, Tab: tab("", "", false)}, ff("Docs — Mozilla Firefox"), false},
		{"a terminal in front", &Guard{Origins: o, Tab: tab("http://10.0.0.5:7710/chat", "Chat", true)}, &dictate.Window{Apps: []string{"foot"}, Title: "Chat"}, false},
		{"GNOME: no window, the focused browser shows the page", &Guard{Origins: o, Tab: tab("http://10.0.0.5:7710/chat", "Chat", true)}, nil, true},
		{"GNOME: no window, no extension", &Guard{Origins: o}, nil, false},
		{"no server known", &Guard{Origins: &Origins{}, Tab: tab("http://10.0.0.5:7710/chat", "Chat", true)}, ff("Jarvis Automation"), false},
		{"nil guard", nil, ff("Jarvis Automation"), false},
	} {
		if got := c.g.InFront(ctx, c.front); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
