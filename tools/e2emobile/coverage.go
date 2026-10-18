package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The coverage gate's mobile half (docs/decisions/2026-10-18-e2e-coverage-gate.md): every
// screen registered in the mobile checkout's src/navigation has an entry in e2e/coverage.json,
// naming the flows that cover it or why it has none, and every entry is a registered screen.

// Entry is one route or screen in e2e/coverage.json: exactly one of Flows (Tests on the web
// side), Manual (why it has no automated test) or Pending (the item that will add one).
type Entry struct {
	Tests   []string `json:"tests,omitempty"`
	Flows   []string `json:"flows,omitempty"`
	Manual  string   `json:"manual,omitempty"`
	Pending string   `json:"pending,omitempty"`
}

// Kind is the entry's kind: "covered", "manual", "pending", or "" when it isn't exactly one.
func (e Entry) Kind() string {
	var kinds []string
	if len(e.Tests) > 0 || len(e.Flows) > 0 {
		kinds = append(kinds, "covered")
	}
	if e.Manual != "" {
		kinds = append(kinds, "manual")
	}
	if e.Pending != "" {
		kinds = append(kinds, "pending")
	}
	if len(kinds) != 1 || (len(e.Tests) > 0 && len(e.Flows) > 0) {
		return ""
	}
	return kinds[0]
}

// Coverage is e2e/coverage.json.
type Coverage struct {
	Web    map[string]Entry `json:"web"`
	Mobile map[string]Entry `json:"mobile"`
}

func LoadCoverage(path string) (*Coverage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Coverage
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

var (
	// screenTag is the start of a <Stack.Screen …> / <Tab.Screen …> registration.
	screenTag     = regexp.MustCompile(`<\w+\.Screen\b`)
	screenName    = regexp.MustCompile(`\bname="([^"]+)"`)
	screenElement = regexp.MustCompile(`\bcomponent=\{(\w+)\}`)
)

// Screens lists the screens registered in navDir (a checkout's src/navigation) as
// "<Navigator file>/<screen name>", sorted: screen names repeat across navigators. A
// registration whose component is itself a navigator of navDir (RootNavigator's Inbox is
// InboxStackNavigator) is a container, not a screen: its own screens are listed instead.
func Screens(navDir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(navDir, "*.tsx"))
	if err != nil {
		return nil, err
	}
	navigators := map[string]bool{}
	for _, f := range files {
		navigators[strings.TrimSuffix(filepath.Base(f), ".tsx")] = true
	}
	var out []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		src, nav := string(raw), strings.TrimSuffix(filepath.Base(f), ".tsx")
		starts := screenTag.FindAllStringIndex(src, -1)
		for i, s := range starts {
			end := len(src)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			// The first name= after the tag is the screen's (an icon's name= in its options comes later).
			tag := src[s[1]:end]
			m := screenName.FindStringSubmatch(tag)
			if m == nil {
				return nil, fmt.Errorf("%s: a screen registration without a name", filepath.Base(f))
			}
			if c := screenElement.FindStringSubmatch(tag); c != nil && navigators[c[1]] {
				continue
			}
			out = append(out, nav+"/"+m[1])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no screen registrations in %s", navDir)
	}
	sort.Strings(out)
	return out, nil
}

// CoverageResult is what the check found, written to <out>/coverage.json for the run's report.
type CoverageResult struct {
	Screens int      `json:"screens"`
	Covered []string `json:"covered"`
	Manual  []string `json:"manual"`  // "<screen>: <reason>"
	Pending []string `json:"pending"` // "<screen>: <item>"
	Missing []string `json:"missing"` // registered, no entry
	Stale   []string `json:"stale"`   // an entry, not registered
	Invalid []string `json:"invalid"` // an entry that isn't exactly one kind, or names a flow that doesn't exist
}

func (r CoverageResult) OK() bool { return len(r.Missing)+len(r.Stale)+len(r.Invalid) == 0 }

// Problems is the result as text: one line per problem, empty when OK.
func (r CoverageResult) Problems() string {
	var b strings.Builder
	for _, s := range r.Missing {
		fmt.Fprintf(&b, "no entry in e2e/coverage.json for screen %s (add the flows covering it, or a \"manual\" reason)\n", s)
	}
	for _, s := range r.Stale {
		fmt.Fprintf(&b, "e2e/coverage.json has an entry for %s, which src/navigation does not register\n", s)
	}
	for _, s := range r.Invalid {
		fmt.Fprintf(&b, "e2e/coverage.json: %s\n", s)
	}
	return b.String()
}

// CheckCoverage compares the mobile entries with the registered screens. flowsDir is the
// checkout's .maestro (rel, as in the entries' paths: ".maestro/home-chat.yaml").
func CheckCoverage(entries map[string]Entry, screens []string, flowsDir, rel string) CoverageResult {
	r := CoverageResult{Screens: len(screens), Covered: []string{}, Manual: []string{}, Pending: []string{},
		Missing: []string{}, Stale: []string{}, Invalid: []string{}}
	registered := map[string]bool{}
	for _, s := range screens {
		registered[s] = true
		if _, ok := entries[s]; !ok {
			r.Missing = append(r.Missing, s)
		}
	}
	names := make([]string, 0, len(entries))
	for k := range entries {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, s := range names {
		e := entries[s]
		if !registered[s] {
			r.Stale = append(r.Stale, s)
			continue
		}
		switch e.Kind() {
		case "covered":
			if len(e.Tests) > 0 {
				r.Invalid = append(r.Invalid, s+": a screen's entry names flows, not tests")
				continue
			}
			bad := false
			for _, f := range e.Flows {
				p := filepath.Join(flowsDir, strings.TrimPrefix(f, rel+"/"))
				if !strings.HasPrefix(f, rel+"/") || !isFile(p) {
					r.Invalid = append(r.Invalid, fmt.Sprintf("%s: no flow %s in the checkout", s, f))
					bad = true
				}
			}
			if !bad {
				r.Covered = append(r.Covered, s)
			}
		case "manual":
			r.Manual = append(r.Manual, s+": "+e.Manual)
		case "pending":
			r.Pending = append(r.Pending, s+": "+e.Pending)
		default:
			r.Invalid = append(r.Invalid, s+`: want exactly one of "flows", "manual" or "pending"`)
		}
	}
	return r
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// coverage runs the check and writes <out>/coverage.json; the record is the suite's first test.
func coverage(covPath, navDir, flowsDir, rel, out string) (CoverageResult, Record, error) {
	rec := Record{Suite: "mobile", ID: "coverage", Title: "coverage: every screen has a flow or a reason (e2e/coverage.json)",
		File: "e2e/coverage.json", Line: 1, Status: "passed", Screenshots: []string{}, ConsoleErrors: []string{}}
	cov, err := LoadCoverage(covPath)
	if err != nil {
		return CoverageResult{}, rec, err
	}
	screens, err := Screens(navDir)
	if err != nil {
		return CoverageResult{}, rec, err
	}
	res := CheckCoverage(cov.Mobile, screens, flowsDir, rel)
	if !res.OK() {
		msg := res.Problems()
		rec.Status, rec.Error = "failed", &msg
	}
	if out != "" {
		raw, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return res, rec, err
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return res, rec, err
		}
		if err := os.WriteFile(filepath.Join(out, "coverage.json"), append(raw, '\n'), 0o644); err != nil {
			return res, rec, err
		}
	}
	return res, rec, nil
}
