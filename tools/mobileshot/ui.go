package main

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Rect is a view's bounds on screen, in pixels: [X1,Y1][X2,Y2] as uiautomator prints them.
type Rect struct{ X1, Y1, X2, Y2 int }

// Center is where a tap lands.
func (r Rect) Center() (int, int) { return (r.X1 + r.X2) / 2, (r.Y1 + r.Y2) / 2 }

// Empty is a zero-area view (collapsed, or not laid out yet).
func (r Rect) Empty() bool { return r.X2 <= r.X1 || r.Y2 <= r.Y1 }

var boundsRE = regexp.MustCompile(`^\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]$`)

func parseBounds(s string) (Rect, error) {
	m := boundsRE.FindStringSubmatch(s)
	if m == nil {
		return Rect{}, fmt.Errorf("bounds %q: want [x1,y1][x2,y2]", s)
	}
	var v [4]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return Rect{v[0], v[1], v[2], v[3]}, nil
}

// Node is one view of a uiautomator dump, flattened (Depth keeps the nesting).
type Node struct {
	Text, Desc, ID, Class, Hint string
	Clickable, Enabled, Focused bool
	Scrollable, Password        bool
	Bounds                      Rect
	Depth                       int
}

// Label is what a reader sees or hears for the view: its text, else its content description.
func (n Node) Label() string {
	if n.Text != "" {
		return n.Text
	}
	return n.Desc
}

type xmlNode struct {
	Text       string    `xml:"text,attr"`
	Desc       string    `xml:"content-desc,attr"`
	ID         string    `xml:"resource-id,attr"`
	Class      string    `xml:"class,attr"`
	Hint       string    `xml:"hint,attr"`
	Clickable  bool      `xml:"clickable,attr"`
	Enabled    bool      `xml:"enabled,attr"`
	Focused    bool      `xml:"focused,attr"`
	Scrollable bool      `xml:"scrollable,attr"`
	Password   bool      `xml:"password,attr"`
	Bounds     string    `xml:"bounds,attr"`
	Children   []xmlNode `xml:"node"`
}

// Screen is a parsed dump: every node in document order.
type Screen struct {
	Nodes []Node
	W, H  int // the root's bounds: the display
}

// ParseDump reads `uiautomator dump` XML. Trailing text after the document (the "UI hierchary
// dumped to" line some versions print on stdout) is ignored.
func ParseDump(b []byte) (*Screen, error) {
	var doc struct {
		Nodes []xmlNode `xml:"node"`
	}
	start := strings.Index(string(b), "<hierarchy")
	if start < 0 {
		return nil, fmt.Errorf("not a uiautomator dump: %.80q", b)
	}
	end := strings.LastIndex(string(b), "</hierarchy>")
	if end < 0 {
		return nil, fmt.Errorf("truncated uiautomator dump")
	}
	if err := xml.Unmarshal(b[start:end+len("</hierarchy>")], &doc); err != nil {
		return nil, fmt.Errorf("uiautomator dump: %w", err)
	}
	s := &Screen{}
	var walk func(ns []xmlNode, depth int) error
	walk = func(ns []xmlNode, depth int) error {
		for _, x := range ns {
			r, err := parseBounds(x.Bounds)
			if err != nil {
				return err
			}
			s.Nodes = append(s.Nodes, Node{Text: x.Text, Desc: x.Desc, ID: x.ID, Class: x.Class, Hint: x.Hint,
				Clickable: x.Clickable, Enabled: x.Enabled, Focused: x.Focused, Scrollable: x.Scrollable,
				Password: x.Password, Bounds: r, Depth: depth})
			if depth == 0 && r.X2 > s.W {
				s.W, s.H = r.X2, r.Y2
			}
			if err := walk(x.Children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(doc.Nodes, 0); err != nil {
		return nil, err
	}
	return s, nil
}

// Selector picks views: "text=Log In" (exact), "text~^Log" (regexp), "desc=…", "desc~…",
// "id=email-input" (a React Native testID: resource-id, with or without a package prefix),
// "hint=…"/"hint~…" (a TextInput's placeholder). Text and desc are matched trimmed.
type Selector struct {
	Field string // text, desc, id, hint
	Exact string
	Re    *regexp.Regexp
	raw   string
}

func (s Selector) String() string { return s.raw }

// ParseSelector parses "field=value" or "field~regexp".
func ParseSelector(s string) (Selector, error) {
	i := strings.IndexAny(s, "=~")
	if i <= 0 {
		return Selector{}, fmt.Errorf("selector %q: want field=value or field~regexp", s)
	}
	sel := Selector{Field: s[:i], raw: s}
	switch sel.Field {
	case "text", "desc", "id", "hint":
	default:
		return Selector{}, fmt.Errorf("selector %q: field is text, desc, id or hint", s)
	}
	if s[i] == '~' {
		re, err := regexp.Compile(s[i+1:])
		if err != nil {
			return Selector{}, fmt.Errorf("selector %q: %w", s, err)
		}
		sel.Re = re
	} else {
		sel.Exact = s[i+1:]
	}
	return sel, nil
}

// MustSelector is ParseSelector for the flow's own literals.
func MustSelector(s string) Selector {
	sel, err := ParseSelector(s)
	if err != nil {
		panic(err)
	}
	return sel
}

func (s Selector) value(n Node) string {
	switch s.Field {
	case "text":
		return strings.TrimSpace(n.Text)
	case "desc":
		return strings.TrimSpace(n.Desc)
	case "hint":
		return strings.TrimSpace(n.Hint)
	}
	id := n.ID
	if j := strings.Index(id, ":id/"); j >= 0 {
		id = id[j+len(":id/"):]
	}
	return id
}

// Match reports whether the view matches.
func (s Selector) Match(n Node) bool {
	v := s.value(n)
	if s.Re != nil {
		return v != "" && s.Re.MatchString(v)
	}
	return v == s.Exact
}

// FindAll returns the visible (non-empty) views that match, in document order.
func (sc *Screen) FindAll(sel Selector) []Node {
	var out []Node
	for _, n := range sc.Nodes {
		if !n.Bounds.Empty() && sel.Match(n) {
			out = append(out, n)
		}
	}
	return out
}

// Find returns the first visible match.
func (sc *Screen) Find(sel Selector) (Node, bool) {
	all := sc.FindAll(sel)
	if len(all) == 0 {
		return Node{}, false
	}
	return all[0], true
}

// Tab finds a bottom-tab button by its label. React Navigation gives each tab the content
// description "<label>, tab, i of n"; failing that, the lowest view showing the label.
func (sc *Screen) Tab(label string) (Node, bool) {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(label) + `, tab\b`)
	if n, ok := sc.Find(Selector{Field: "desc", Re: re}); ok {
		return n, true
	}
	all := sc.FindAll(Selector{Field: "text", Exact: label})
	if len(all) == 0 {
		return Node{}, false
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Bounds.Y1 > all[j].Bounds.Y1 })
	return all[0], true
}

// Texts is every visible label on screen, in order, without repeats (a view and its wrapper
// often carry the same text).
func (sc *Screen) Texts() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range sc.Nodes {
		l := strings.TrimSpace(n.Label())
		if l == "" || n.Bounds.Empty() || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// Overflow lists the labelled views that stick out past the display's left or right edge:
// clipped text a user can't read. Views that are wholly off-screen (a pager's other pages)
// don't count.
func (sc *Screen) Overflow() []string {
	var out []string
	for _, n := range sc.Nodes {
		l := strings.TrimSpace(n.Label())
		if l == "" || n.Bounds.Empty() || sc.W == 0 {
			continue
		}
		if n.Bounds.X2 <= 0 || n.Bounds.X1 >= sc.W {
			continue
		}
		if n.Bounds.X1 < 0 || n.Bounds.X2 > sc.W {
			out = append(out, fmt.Sprintf("%q [%d,%d][%d,%d]", l, n.Bounds.X1, n.Bounds.Y1, n.Bounds.X2, n.Bounds.Y2))
		}
	}
	return out
}

// loadingRE is a placeholder that says the screen is still filling in ("Loading tools…").
var loadingRE = regexp.MustCompile(`(?i)^(loading\b|connecting\b)`)

// Busy reports a visible spinner (React Native's ActivityIndicator is an Android ProgressBar)
// or a "Loading…" placeholder.
func (sc *Screen) Busy() bool {
	for _, n := range sc.Nodes {
		if n.Bounds.Empty() {
			continue
		}
		if n.Class == "android.widget.ProgressBar" || loadingRE.MatchString(strings.TrimSpace(n.Label())) ||
			loadingRE.MatchString(strings.TrimSpace(n.Hint)) {
			return true
		}
	}
	return false
}

// Signature is a cheap fingerprint of what is on screen (labels and where they are), used to
// tell when a screen has stopped changing and whether a scroll moved anything.
func (sc *Screen) Signature() string {
	var b strings.Builder
	for _, n := range sc.Nodes {
		if l := n.Label(); l != "" {
			fmt.Fprintf(&b, "%s@%d,%d;", l, n.Bounds.X1, n.Bounds.Y1)
		}
	}
	return b.String()
}

// shellQuote quotes s for the device's /bin/sh: adb shell joins its arguments into one command
// line, so every argument with user text in it must survive that shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// inputText is the argument for `input text`: spaces become %s, which `input` turns back into
// spaces (a literal "%s" in the text can't be typed this way).
func inputText(s string) string {
	return shellQuote(strings.ReplaceAll(s, " ", "%s"))
}
