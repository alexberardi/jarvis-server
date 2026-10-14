// Package jarvispage recognises the Jarvis web page of this agent's own server on this
// computer (DESIGN.md §14 "The Jarvis page in front"; web app plan §3.6). Someone chatting with
// Jarvis in that page asks it to type or fill something: the window in front, and the browser's
// current tab, is the chat itself, so dictation and the browser's fill and click must not act
// on it.
//
// The agent knows its server: the config URL it enrolled with and the web UI's address from
// discovery. A page is "the Jarvis page" when its address has one of those hosts and ports (a
// loopback name matches any loopback name). Without the browser extension a window can only be
// judged by what the compositor shows of it: a browser window whose title carries the web
// app's own title ("Jarvis Automation") or one of those addresses, or a Chromium app window
// whose app id carries the server's host.
package jarvispage

import (
	"context"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// WebTitle is the web app's document title (web/admin/index.html), which browsers put in the
// window title.
const WebTitle = "Jarvis Automation"

// Origins is the set of this agent's server addresses (host and port). Safe for concurrent use:
// discovery adds the web UI's address while tools run.
type Origins struct {
	mu   sync.RWMutex
	list []hostPort
}

type hostPort struct{ host, port string }

const loopback = "\x00loopback"

func canonHost(h string) string {
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return loopback
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return loopback
	}
	return h
}

func parse(raw string) (hostPort, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return hostPort{}, false
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return hostPort{canonHost(u.Hostname()), port}, true
}

// Add records a server address (an http(s) URL); anything else is ignored.
func (o *Origins) Add(raw string) {
	hp, ok := parse(raw)
	if !ok {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, x := range o.list {
		if x == hp {
			return
		}
	}
	o.list = append(o.list, hp)
}

func (o *Origins) all() []hostPort {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return append([]hostPort(nil), o.list...)
}

// Known: at least one address is recorded.
func (o *Origins) Known() bool { return len(o.all()) > 0 }

// MatchURL: the page at raw is on this agent's server.
func (o *Origins) MatchURL(raw string) bool {
	hp, ok := parse(raw)
	if !ok {
		return false
	}
	for _, x := range o.all() {
		if x == hp {
			return true
		}
	}
	return false
}

// spellings are how an address can appear in a window title: "host:port", and for a loopback
// address each loopback name.
func (hp hostPort) spellings() []string {
	hosts := []string{hp.host}
	if hp.host == loopback {
		hosts = []string{"localhost", "127.0.0.1", "[::1]"}
	}
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h + ":" + hp.port
	}
	return out
}

// chromiumAppPrefixes start a Chromium app window's id ("chrome-<host>__<path>-<profile>").
var chromiumAppPrefixes = []string{"chrome-", "chromium-", "brave-", "msedge-", "vivaldi-"}

// MarksWindow: from its app names and title alone, the window shows the Jarvis page.
func (o *Origins) MarksWindow(apps []string, title string) bool {
	if !winmatch.IsBrowser(apps) {
		return false // a terminal titled "curl localhost:7710" is not the page
	}
	t := strings.ToLower(title)
	if strings.Contains(t, strings.ToLower(WebTitle)) {
		return true
	}
	for _, hp := range o.all() {
		for _, s := range hp.spellings() {
			if strings.Contains(t, s) {
				return true
			}
		}
		for _, a := range apps {
			a = strings.ToLower(a)
			for _, p := range chromiumAppPrefixes {
				if !strings.HasPrefix(a, p) {
					continue
				}
				host := strings.TrimPrefix(a, p)
				if i := strings.Index(host, "__"); i >= 0 {
					host = host[:i]
				}
				// Chromium writes the port, if any, as "_port" or drops it.
				if h, _, ok := strings.Cut(host, "_"); ok {
					host = h
				}
				if canonHost(host) == hp.host {
					return true
				}
			}
		}
	}
	return false
}

// Tab is the current tab of a browser that has the keyboard focus now (from the Jarvis
// browser extension); ok false when no connected browser has focus or none is connected.
type Tab func(ctx context.Context) (url, title string, ok bool)

// Guard is dictation's JarvisPage check.
type Guard struct {
	Origins *Origins
	// Tab, when set, is the extension's view of the focused browser's current tab.
	Tab Tab
}

// InFront says whether front (nil: the window backend can't tell which window is in front)
// shows the Jarvis page. With the extension, a browser window in front whose title shows the
// focused browser's current tab, and that tab is on the server, is the page; without a window
// to look at (GNOME, KDE), the focused browser's current tab alone decides.
func (g *Guard) InFront(ctx context.Context, front *dictate.Window) bool {
	if g == nil || !g.Origins.Known() {
		return false
	}
	if front != nil && g.Origins.MarksWindow(front.Apps, front.Title) {
		return true
	}
	if front != nil && !winmatch.IsBrowser(front.Apps) {
		return false
	}
	if g.Tab == nil {
		return false
	}
	u, title, ok := g.Tab(ctx)
	if !ok || !g.Origins.MatchURL(u) {
		return false
	}
	if front == nil {
		return true
	}
	title = strings.TrimSpace(title)
	return title == "" || strings.Contains(strings.ToLower(front.Title), strings.ToLower(title))
}
