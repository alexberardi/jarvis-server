package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A Case is one computer task, in tools/ccsuite/cases/<name>.json (docs/dev/computer-suite.md):
// what the user says, how the fixtures start, how the phone's cards are answered, and what the
// fixtures' own state must show afterwards. Every case starts pending; the piece that delivers it
// flips it to required.
type Case struct {
	Name   string `json:"name"`
	About  string `json:"about,omitempty"` // for people: what it covers, what flipping it needs
	Phase  string `json:"phase"`
	Status string `json:"status"`
	// Say is the chat message, from the seeded user to the agent node. {{sites}} is the fixture
	// sites' base URL, {{layout}} "?layout=moved" when setup.layout is moved (else empty).
	Say        string   `json:"say"`
	Target     string   `json:"target"` // browser or desktop
	Setup      Setup    `json:"setup"`
	Answers    []Answer `json:"answers"`
	Checks     []Check  `json:"checks"`
	MaxMinutes float64  `json:"max_minutes"`

	File string `json:"-"` // the file it came from, relative to the repo
}

// Setup is how the fixtures start before Say.
type Setup struct {
	// Login lists the ccsites sites (store, bank, card) whose session cookie goes into the
	// browser profile, standing in for a saved login.
	Login       []string `json:"login"`
	Layout      string   `json:"layout,omitempty"`       // ccsites pages: default or moved
	NotesLayout string   `json:"notes_layout,omitempty"` // the notes app: default or moved
}

// Answer says how to answer a card that asks: when is the card's kind (confirm: a card with
// buttons; plan: an errand's plan card), tap the button to press.
type Answer struct {
	When string `json:"when"`
	Tap  string `json:"tap"`
}

// Check is one assertion on the state after the errand (check.go). Which fields apply depends
// on Kind; Validate says which are required.
type Check struct {
	Kind string `json:"kind"`
	// order_placed: an item whose id or name matches Product (a regexp), and the order's total
	// at most MaxTotal when set.
	Product  string  `json:"product,omitempty"`
	MaxTotal float64 `json:"max_total,omitempty"`
	// download: a statement downloaded from Site (bank, card) for Month (YYYY-MM, optional).
	Site  string `json:"site,omitempty"`
	Month string `json:"month,omitempty"`
	// spreadsheet_has: a file in the agent's ~/Documents/Jarvis matching File (a glob, default
	// every file) whose text contains each of Contains (case-insensitive).
	File     string   `json:"file,omitempty"`
	Contains []string `json:"contains,omitempty"`
	// form_submitted: a submission with these fields (case-insensitive, trimmed).
	Fields map[string]string `json:"fields,omitempty"`
	// note_exists: a saved note titled Title whose body contains Body.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// reply_matches, asked, refused: a regexp over what Jarvis said (asked: the asking card's
	// title and body; refused: the replies, default refusalRE).
	Pattern string `json:"pattern,omitempty"`
}

var (
	phases = []string{"tests", "browser", "desktop-mac", "desktop-wlroots", "desktop-x11", "teach"}
	// checkKinds lists every check kind with the field it needs ("" for none).
	checkKinds = map[string]string{
		"order_placed": "", "no_order": "", "download": "site", "spreadsheet_has": "contains",
		"form_submitted": "fields", "note_exists": "title", "reply_matches": "pattern", "asked": "", "refused": "",
	}
	loginSites = []string{"store", "bank", "card"}
	answerWhen = []string{"confirm", "plan"}
	answerTap  = []string{"approve", "cancel"}
	nameRE     = regexp.MustCompile(`^[a-z0-9_]+$`)
	monthRE    = regexp.MustCompile(`^\d{4}-\d{2}$`)
)

func oneOf(s string, list ...string) bool {
	for _, l := range list {
		if s == l {
			return true
		}
	}
	return false
}

// Validate reports the first thing wrong with c.
func (c *Case) Validate() error {
	switch {
	case !nameRE.MatchString(c.Name):
		return fmt.Errorf("name %q: lower case letters, digits and _", c.Name)
	case !oneOf(c.Phase, phases...):
		return fmt.Errorf("phase %q: one of %s", c.Phase, strings.Join(phases, ", "))
	case !oneOf(c.Status, "pending", "required"):
		return fmt.Errorf("status %q: pending or required", c.Status)
	case strings.TrimSpace(c.Say) == "":
		return errors.New("say is empty")
	case !oneOf(c.Target, "browser", "desktop"):
		return fmt.Errorf("target %q: browser or desktop", c.Target)
	case c.MaxMinutes <= 0 || c.MaxMinutes > 60:
		return fmt.Errorf("max_minutes %v: more than 0, at most 60", c.MaxMinutes)
	case len(c.Checks) == 0:
		return errors.New("no checks")
	case !oneOf(c.Setup.Layout, "", "default", "moved"):
		return fmt.Errorf("setup.layout %q: default or moved", c.Setup.Layout)
	case !oneOf(c.Setup.NotesLayout, "", "default", "moved"):
		return fmt.Errorf("setup.notes_layout %q: default or moved", c.Setup.NotesLayout)
	}
	for _, s := range c.Setup.Login {
		if !oneOf(s, loginSites...) {
			return fmt.Errorf("setup.login %q: one of %s", s, strings.Join(loginSites, ", "))
		}
	}
	for i, a := range c.Answers {
		if !oneOf(a.When, answerWhen...) || !oneOf(a.Tap, answerTap...) {
			return fmt.Errorf("answers[%d]: when confirm|plan, tap approve|cancel", i)
		}
	}
	for i, ch := range c.Checks {
		if err := ch.Validate(); err != nil {
			return fmt.Errorf("checks[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate reports a check of an unknown kind, without the field its kind needs, or with a bad
// regexp or month.
func (ch Check) Validate() error {
	need, ok := checkKinds[ch.Kind]
	if !ok {
		kinds := make([]string, 0, len(checkKinds))
		for k := range checkKinds {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		return fmt.Errorf("kind %q: one of %s", ch.Kind, strings.Join(kinds, ", "))
	}
	missing := false
	switch need {
	case "site":
		missing = ch.Site == ""
	case "contains":
		missing = len(ch.Contains) == 0
	case "fields":
		missing = len(ch.Fields) == 0
	case "title":
		missing = ch.Title == ""
	case "pattern":
		missing = ch.Pattern == ""
	}
	if missing {
		return fmt.Errorf("%s needs %s", ch.Kind, need)
	}
	if ch.Site != "" && !oneOf(ch.Site, "bank", "card") {
		return fmt.Errorf("site %q: bank or card", ch.Site)
	}
	if ch.Month != "" && !monthRE.MatchString(ch.Month) {
		return fmt.Errorf("month %q: YYYY-MM", ch.Month)
	}
	if ch.File != "" {
		if _, err := filepath.Match(ch.File, ""); err != nil {
			return fmt.Errorf("file %q: %w", ch.File, err)
		}
	}
	for _, re := range []string{ch.Product, ch.Pattern} {
		if _, err := regexp.Compile(re); err != nil {
			return err
		}
	}
	return nil
}

// LoadCases reads every *.json in dir, sorted by file name; rel is how the files are named in
// the report (relative to the repo).
func LoadCases(dir, rel string) ([]Case, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no cases in %s", dir)
	}
	sort.Strings(paths)
	var cases []Case
	seen := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var c Case
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if want := strings.TrimSuffix(filepath.Base(p), ".json"); c.Name != want {
			return nil, fmt.Errorf("%s: name %q is not the file's name %q", p, c.Name, want)
		}
		if other, dup := seen[c.Name]; dup {
			return nil, fmt.Errorf("%s: name %q is also %s", p, c.Name, other)
		}
		seen[c.Name] = p
		c.File = filepath.ToSlash(filepath.Join(rel, filepath.Base(p)))
		cases = append(cases, c)
	}
	return cases, nil
}

// Message is Say with the run's values filled in.
func (c *Case) Message(sites string) string {
	layout := ""
	if c.Setup.Layout == "moved" {
		layout = "?layout=moved"
	}
	return strings.NewReplacer("{{sites}}", strings.TrimRight(sites, "/"), "{{layout}}", layout).Replace(c.Say)
}

// needs says what a case needs brought up: a desktop session of which kind (wlroots, x11, mac,
// or "" for none) and whether a browser runs in it.
func (c *Case) needs() (desktop string, browser bool) {
	switch c.Phase {
	case "browser", "teach":
		return "wlroots", true
	case "desktop-wlroots":
		return "wlroots", false
	case "desktop-x11":
		return "x11", false
	case "desktop-mac":
		return "mac", false
	}
	return "", false
}
