// Package winmatch picks the window a user named ("firefox", "the terminal", "slack", words from
// a title) from a platform's window list. read-the-screen (screen) and dictation (dictate) both
// use it, so "the firefox window" means the same window to every tool.
package winmatch

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Naming a window: a tool's `window`/`app` argument is what the user called it ("firefox",
// "the terminal", "slack", words from a title). Each platform lists its windows as candidates
// and Pick (or PickUnique) chooses one, the same way everywhere.

// Error codes for a named window that can't be used.
const (
	CodeNotFound   = "window_not_found"
	CodeNotVisible = "window_not_visible"
	CodeAmbiguous  = "ambiguous_window" // PickUnique: several windows match equally well
)

// Error is a named window that can't be used, with a message the model can relay.
type Error struct {
	Code string
	Msg  string
	// Candidates (CodeAmbiguous): the matching windows as "App — title", most recent first,
	// so the model can ask the user which one they meant.
	Candidates []string
	// Query is what the user called the window, as given ("Omerite").
	Query string
	// Apps (CodeNotFound): the open apps' display names, most recent first, so the model can
	// ask which one the user meant.
	Apps []string
}

// Match is the window a query picked and how.
type Match struct {
	Index int
	// Fuzzy: no app name matched as said and the window was taken from the one app whose
	// name is a near spelling of the query ("omerite" for omawrite: a misheard name).
	Fuzzy bool
}

func (e *Error) Error() string { return e.Msg }

// Candidate is one window as Pick sees it.
type Candidate struct {
	Apps    []string // app class / app id / owner name (and an initial class), most specific first
	Title   string
	Recency int  // lower = focused more recently (focus history, or front-to-back order)
	Visible bool // its pixels are on screen now (what a region capture would show)
}

// app is the Candidate's first non-empty app name.
func (c Candidate) App() string {
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

// Normalize turns "the Firefox window" into "firefox".
func Normalize(q string) string {
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

// IsBrowser: one of a window's app names is a web browser's ("firefox", "google-chrome",
// a Chromium app window's "chrome-host__path-Default").
func IsBrowser(apps []string) bool { return aliasMatches(apps, "browser") }

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

// DisplayName is how a message names a window's app: "org.mozilla.firefox" → "Firefox".
func DisplayName(c Candidate) string {
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

// Pick chooses the window the query names: the first tier with any hit decides (app name,
// then a generic kind like "terminal", then title words, then a near spelling of one app's
// name), and within it the most recently focused visible window wins. A hit that isn't
// visible is window_not_visible; no hit at all is window_not_found naming a few visible apps.
// It never falls back to another window.
func Pick(cands []Candidate, query string) (int, error) {
	m, err := PickMatch(cands, query)
	return m.Index, err
}

// PickMatch is Pick, saying whether the match was a fuzzy one.
func PickMatch(cands []Candidate, query string) (Match, error) {
	q := Normalize(query)
	order := byRecency(cands)
	if q != "" {
		for t, match := range tiers(cands, q) {
			if match == nil {
				return Match{Index: -1}, fuzzyAmbiguous(cands, order, query)
			}
			first := -1
			for _, i := range order {
				if !match(i) {
					continue
				}
				if cands[i].Visible {
					return Match{Index: i, Fuzzy: t == fuzzyTier}, nil
				}
				if first < 0 {
					first = i
				}
			}
			if first >= 0 {
				return Match{Index: -1}, &Error{Code: CodeNotVisible, Query: query,
					Msg: DisplayName(cands[first]) + " is open but not visible — bring it to the front and ask again"}
			}
		}
	}
	return Match{Index: -1}, notFound(cands, order, query, true)
}

// byRecency is the candidates' indexes, most recently focused first.
func byRecency(cands []Candidate) []int {
	order := make([]int, len(cands))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return cands[order[a]].Recency < cands[order[b]].Recency })
	return order
}

// fuzzyTier is the index of the near-spelling tier in tiers.
const fuzzyTier = 3

// tiers are the ways a query can name a window, strongest first. The fuzzy tier is nil when
// near spellings of several different apps are within reach: that is ambiguous, never a
// guess.
func tiers(cands []Candidate, q string) []func(i int) bool {
	ts := []func(i int) bool{
		func(i int) bool { return appMatches(cands[i].Apps, q) },
		func(i int) bool { return aliasMatches(cands[i].Apps, q) },
		func(i int) bool { return titleMatches(cands[i].Title, q) },
	}
	hits, apps := fuzzyHits(cands, q)
	if apps > 1 {
		return append(ts, nil)
	}
	return append(ts, func(i int) bool { return hits[i] })
}

// notFound is window_not_found naming up to 5 apps: the visible ones (onlyVisible: what a
// capture could show), or every open one (acting on a window brings it forward).
func notFound(cands []Candidate, order []int, query string, onlyVisible bool) error {
	var names []string
	seen := map[string]bool{}
	for _, i := range order {
		if n := DisplayName(cands[i]); (cands[i].Visible || !onlyVisible) && !seen[n] && len(names) < 5 {
			seen[n] = true
			names = append(names, n)
		}
	}
	msg := fmt.Sprintf("no open window matches %q", query)
	if len(names) > 0 {
		if onlyVisible {
			msg += "; visible windows: " + strings.Join(names, ", ")
		} else {
			msg += "; open apps: " + strings.Join(names, ", ")
		}
	}
	return &Error{Code: CodeNotFound, Msg: msg, Query: query, Apps: names}
}

// fuzzyAmbiguous: near spellings of several apps, so asking beats guessing.
func fuzzyAmbiguous(cands []Candidate, order []int, query string) error {
	hits, _ := fuzzyHits(cands, Normalize(query))
	var names []string
	seen := map[string]bool{}
	for _, i := range order {
		if n := DisplayName(cands[i]); hits[i] && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	return &Error{Code: CodeAmbiguous, Query: query, Candidates: names,
		Msg: fmt.Sprintf("no app is called %q, and it sounds like several (%s); ask the user which one they mean",
			query, strings.Join(names, ", "))}
}

// PickUnique is Pick for acting on a window (typing into it), where a wrong guess is costly: the
// first tier with any hit decides, and within it the window must be the only match. Visibility
// doesn't disqualify (focusing a window brings it forward). When several match, the one that
// has focus now wins (focused is its index, -1 for none), else the only visible one; otherwise
// it is CodeAmbiguous with the candidates, never a guess.
func PickUnique(cands []Candidate, query string, focused int) (int, error) {
	m, err := PickUniqueMatch(cands, query, focused)
	return m.Index, err
}

// PickUniqueMatch is PickUnique, saying whether the match was a fuzzy one.
func PickUniqueMatch(cands []Candidate, query string, focused int) (Match, error) {
	q := Normalize(query)
	order := byRecency(cands)
	if q != "" {
		for t, match := range tiers(cands, q) {
			if match == nil {
				return Match{Index: -1}, fuzzyAmbiguous(cands, order, query)
			}
			var hits []int
			for _, i := range order {
				if match(i) {
					hits = append(hits, i)
				}
			}
			if len(hits) == 0 {
				continue
			}
			fuzzy := t == fuzzyTier
			if len(hits) == 1 {
				return Match{Index: hits[0], Fuzzy: fuzzy}, nil
			}
			visible := -1
			nVisible := 0
			for _, i := range hits {
				if i == focused {
					return Match{Index: i, Fuzzy: fuzzy}, nil
				}
				if cands[i].Visible {
					visible, nVisible = i, nVisible+1
				}
			}
			if nVisible == 1 {
				return Match{Index: visible, Fuzzy: fuzzy}, nil
			}
			var names []string
			for _, i := range hits {
				if len(names) == 8 {
					break
				}
				n := DisplayName(cands[i])
				if t := strings.TrimSpace(cands[i].Title); t != "" {
					if r := []rune(t); len(r) > 60 {
						t = string(r[:60]) + "…"
					}
					n += " — " + t
				}
				names = append(names, n)
			}
			return Match{Index: -1}, &Error{Code: CodeAmbiguous, Candidates: names, Query: query,
				Msg: fmt.Sprintf("%d windows match %q; ask the user which one they mean (use words from its title)", len(hits), query)}
		}
	}
	return Match{Index: -1}, notFound(cands, order, query, false)
}
