package screen

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Naming a window: desktop_read_screen's `window` argument is what the user called it ("firefox",
// "the terminal", "slack", words from a title). Each platform lists its windows as candidates
// and pickWindow chooses one, the same way everywhere.

// Error codes for a named window that can't be captured.
const (
	CodeWindowNotFound   = "window_not_found"
	CodeWindowNotVisible = "window_not_visible"
)

// WindowError is a named window that can't be captured, with a message the model can relay.
type WindowError struct {
	Code string
	Msg  string
}

func (e *WindowError) Error() string { return e.Msg }

// candidate is one window as pickWindow sees it.
type candidate struct {
	Apps    []string // app class / app id / owner name (and an initial class), most specific first
	Title   string
	Recency int  // lower = focused more recently (focus history, or front-to-back order)
	Visible bool // its pixels are on screen now (what a region capture would show)
}

// app is the candidate's first non-empty app name.
func (c candidate) app() string {
	for _, a := range c.Apps {
		if a != "" {
			return a
		}
	}
	return ""
}

// aliases are generic words for kinds of app: "the terminal", "the browser".
var aliases = map[string][]string{
	"terminal": {"foot", "kitty", "alacritty", "ghostty", "wezterm", "konsole", "gnometerminal", "xterm", "terminal",
		"iterm", "warp", "tilix", "terminator", "urxvt", "ptyxis", "console"},
	"browser": {"firefox", "chrome", "chromium", "brave", "safari", "zen", "librewolf", "vivaldi", "msedge", "opera", "epiphany"},
	"editor":  {"code", "codium", "zed", "sublime", "gedit", "texteditor", "kate", "neovide", "emacs", "nvim", "vim"},
	"email":   {"thunderbird", "mail", "geary", "evolution", "outlook"},
}

func init() {
	aliases["console"] = aliases["terminal"]
	aliases["shell"] = aliases["terminal"]
	aliases["web browser"] = aliases["browser"]
	aliases["mail"] = aliases["email"]
	aliases["code editor"] = aliases["editor"]
}

// normalizeQuery turns "the Firefox window" into "firefox".
func normalizeQuery(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	q = strings.Trim(q, "\"'`“”‘’ .")
	q = strings.ReplaceAll(q, "'s ", " ")
	q = strings.TrimSuffix(q, "'s")
	for changed := true; changed; {
		changed = false
		for _, p := range []string{"the ", "my ", "this ", "that "} {
			if strings.HasPrefix(q, p) {
				q, changed = strings.TrimSpace(q[len(p):]), true
			}
		}
		for _, s := range []string{" window", " app", " application", " program"} {
			if strings.HasSuffix(q, s) && len(q) > len(s) {
				q, changed = strings.TrimSpace(q[:len(q)-len(s)]), true
			}
		}
	}
	if fillers[q] {
		return ""
	}
	return q
}

// fillers name no particular window: "this window", "the current one".
var fillers = map[string]bool{"window": true, "app": true, "focused": true, "current": true, "active": true,
	"front": true, "it": true, "one": true, "screen": true, "focused window": true, "current window": true,
	"active window": true, "front window": true, "current one": true}

// compact keeps letters and digits: "Google-Chrome" and "google chrome" both become "googlechrome".
func compact(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// lastSeg is a reverse-DNS app id's last part: "org.mozilla.firefox" → "firefox".
func lastSeg(app string) string {
	if i := strings.LastIndex(app, "."); i >= 0 && i < len(app)-1 {
		return app[i+1:]
	}
	return app
}

// appMatches: the query names this app (exact, the id's last part, or a part of the name).
func appMatches(apps []string, q string) bool {
	cq := compact(q)
	if cq == "" {
		return false
	}
	words := strings.Fields(q)
	for _, a := range apps {
		ca, cs := compact(a), compact(lastSeg(a))
		if ca == "" {
			continue
		}
		if ca == cq || cs == cq || (len(cq) >= 3 && strings.Contains(ca, cq)) {
			return true
		}
		for _, w := range words { // "firefox browser", "slack app"
			if cw := compact(w); len(cw) >= 3 && (cw == ca || cw == cs) {
				return true
			}
		}
	}
	return false
}

func aliasMatches(apps []string, q string) bool {
	kinds := aliases[q]
	for _, a := range apps {
		ca := compact(a)
		for _, k := range kinds {
			if ca != "" && strings.Contains(ca, k) {
				return true
			}
		}
	}
	return false
}

func titleMatches(title, q string) bool {
	t := strings.ToLower(title)
	if t == "" {
		return false
	}
	for _, w := range strings.Fields(q) {
		if !strings.Contains(t, w) {
			return false
		}
	}
	return true
}

// displayName is how a message names a window's app: "org.mozilla.firefox" → "Firefox".
func displayName(c candidate) string {
	for _, a := range c.Apps {
		if s := strings.TrimSpace(lastSeg(a)); s != "" {
			if strings.ToLower(s) == s {
				r := []rune(s)
				r[0] = unicode.ToUpper(r[0])
				s = string(r)
			}
			return s
		}
	}
	return "That window"
}

// pickWindow chooses the window the query names: the first tier with any hit decides (app name,
// then a generic kind like "terminal", then title words), and within it the most recently
// focused visible window wins. A hit that isn't visible is window_not_visible; no hit at all is
// window_not_found naming a few visible apps. It never falls back to another window.
func pickWindow(cands []candidate, query string) (int, error) {
	q := normalizeQuery(query)
	order := make([]int, len(cands))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return cands[order[a]].Recency < cands[order[b]].Recency })
	tiers := []func(c candidate) bool{
		func(c candidate) bool { return appMatches(c.Apps, q) },
		func(c candidate) bool { return aliasMatches(c.Apps, q) },
		func(c candidate) bool { return titleMatches(c.Title, q) },
	}
	if q != "" {
		for _, match := range tiers {
			first := -1
			for _, i := range order {
				if !match(cands[i]) {
					continue
				}
				if cands[i].Visible {
					return i, nil
				}
				if first < 0 {
					first = i
				}
			}
			if first >= 0 {
				return -1, &WindowError{Code: CodeWindowNotVisible,
					Msg: displayName(cands[first]) + " is open but not visible — bring it to the front and ask again"}
			}
		}
	}
	var names []string
	seen := map[string]bool{}
	for _, i := range order {
		if n := displayName(cands[i]); cands[i].Visible && !seen[n] && len(names) < 5 {
			seen[n] = true
			names = append(names, n)
		}
	}
	msg := fmt.Sprintf("no open window matches %q", query)
	if len(names) > 0 {
		msg += "; visible windows: " + strings.Join(names, ", ")
	}
	return -1, &WindowError{Code: CodeWindowNotFound, Msg: msg}
}
