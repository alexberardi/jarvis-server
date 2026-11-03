package apps

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// ErrNotFound: no installed app matches the name.
var ErrNotFound = errors.New("no installed app matches")

// AmbiguousError: several apps match equally well; the caller should ask which one.
type AmbiguousError struct {
	Query      string
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches several apps: %s", e.Query, strings.Join(e.Candidates, ", "))
}

// NotFoundError carries the closest names as suggestions.
type NotFoundError struct {
	Query       string
	Suggestions []string
}

func (e *NotFoundError) Error() string {
	// Said aloud when every tool failed ("Sorry, that didn't work: …"), so it reads as a sentence.
	if len(e.Suggestions) == 0 {
		return fmt.Sprintf("there's no app called %q on this computer", e.Query)
	}
	return fmt.Sprintf("there's no app called %q on this computer; did you mean %s?", e.Query, orList(e.Suggestions))
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// orList is "A", "A or B", "A, B or C".
func orList(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}

// norm lowercases and keeps letters and digits only: "Visual Studio Code" -> "visualstudiocode".
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// cleanQuery drops filler a model or a transcript adds: "the Steam app" -> "steam".
func cleanQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimSuffix(strings.TrimSuffix(q, ".app"), ".desktop")
	ws := words(q)
	for len(ws) > 1 && (ws[0] == "the" || ws[0] == "my") {
		ws = ws[1:]
	}
	for len(ws) > 1 && (ws[len(ws)-1] == "app" || ws[len(ws)-1] == "application") {
		ws = ws[:len(ws)-1]
	}
	return strings.Join(ws, " ")
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Pick is the app a spoken name resolved to, and how.
type Pick struct {
	App App
	// Fuzzy: nothing is called that, and the app was taken from a near spelling or a
	// sound-alike of what was heard ("Omerite", "oh my right" → Omawrite).
	Fuzzy bool
	// HeardAs is the name as given, when Fuzzy.
	HeardAs string
}

// Resolve picks exactly one app for a spoken name (Find without the how).
func Resolve(list []App, query string) (App, error) {
	p, err := Find(list, query)
	return p.App, err
}

// Find picks exactly one app for a spoken name, in tiers, stopping at the first tier with
// any match: exact (name or desktop id, ignoring case and punctuation), then the name starts
// with the query, then every query word appears in the name, then the app's GenericName or a
// Keyword (desktop files), then a small edit distance (typos,
// mishearings), then the same sound (winmatch.SoundsLike: "oh my right" for Omawrite). A tier
// with several matches is ambiguous rather than a guess. Names that only sound close
// (winmatch.SoundsNear: "the oven" for Neovim) are never picked; they come back as suggestions.
func Find(list []App, query string) (Pick, error) {
	q := cleanQuery(query)
	nq := norm(q)
	if nq == "" {
		return Pick{}, &NotFoundError{Query: query}
	}
	tiers := []func(a App) bool{
		func(a App) bool { return norm(a.Name) == nq || (a.ID != "" && norm(a.ID) == nq) },
		func(a App) bool { return strings.HasPrefix(norm(a.Name), nq) },
		func(a App) bool {
			name := norm(a.Name)
			for _, w := range words(q) {
				if !strings.Contains(name, w) {
					return false
				}
			}
			return true
		},
	}
	for _, match := range tiers {
		var hits []App
		for _, a := range list {
			if match(a) {
				hits = append(hits, a)
			}
		}
		if len(hits) == 1 {
			return Pick{App: hits[0]}, nil
		}
		if len(hits) > 1 {
			return Pick{}, &AmbiguousError{Query: query, Candidates: names(hits, 8)}
		}
	}
	// What the app is (GenericName, Keywords): "calculator" for Omacalc. Before any fuzzy
	// tier: an exact word the app describes itself with beats a near spelling of another name.
	var also []App
	for _, a := range list {
		for _, w := range a.Also {
			if norm(w) == nq {
				also = append(also, a)
				break
			}
		}
	}
	if len(also) == 1 {
		return Pick{App: also[0]}, nil
	}
	if len(also) > 1 {
		return Pick{}, &AmbiguousError{Query: query, Candidates: names(also, 8)}
	}
	fuzzy := func(a App) Pick { return Pick{App: a, Fuzzy: true, HeardAs: strings.TrimSpace(query)} }
	// Spelling: allow 1 edit for short names, 2 for longer; the best distance must be unique.
	limit := 1
	if len(nq) >= 6 {
		limit = 2
	}
	best, bestD := []App{}, limit+1
	for _, a := range list {
		d := levenshtein(nq, norm(a.Name))
		if d < bestD {
			best, bestD = []App{a}, d
		} else if d == bestD {
			best = append(best, a)
		}
	}
	if bestD <= limit {
		if len(best) == 1 {
			return fuzzy(best[0]), nil
		}
		return Pick{}, &AmbiguousError{Query: query, Candidates: names(best, 8)}
	}
	// Sound: the same sound-alike key as exactly one app.
	var alike []App
	for _, a := range list {
		if winmatch.SoundsLike(q, a.Name) || (a.ID != "" && winmatch.SoundsLike(q, a.ID)) {
			alike = append(alike, a)
		}
	}
	if len(alike) == 1 {
		return fuzzy(alike[0]), nil
	}
	if len(alike) > 1 {
		return Pick{}, &AmbiguousError{Query: query, Candidates: names(alike, 8)}
	}
	return Pick{}, &NotFoundError{Query: query, Suggestions: suggest(list, q, 5)}
}

// Scored is one app and how alike its name is to what was heard (0..1).
type Scored struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// minScore: names less alike than this are not worth offering.
const minScore = 0.5

// Closest is the n apps whose names are most alike what was heard, by spelling or sound
// (winmatch.Similarity), best first; an exact, prefix or all-words match scores 1.
func Closest(list []App, query string, n int) []Scored {
	q := cleanQuery(query)
	nq := norm(q)
	if nq == "" {
		return nil
	}
	var all []Scored
	for _, a := range list {
		s := winmatch.Similarity(q, a.Name)
		if na := norm(a.Name); na == nq || strings.HasPrefix(na, nq) || (a.ID != "" && norm(a.ID) == nq) {
			s = 1
		}
		if s >= minScore {
			all = append(all, Scored{Name: a.Name, Score: math.Round(s*100) / 100})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > n {
		all = all[:n]
	}
	return all
}

func names(as []App, n int) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Name)
	}
	sort.Strings(out)
	if len(out) > n {
		out = append(out[:n], fmt.Sprintf("and %d more", len(as)-n))
	}
	return out
}

// suggest returns up to n names close to the query, by spelling or sound, best first.
func suggest(list []App, q string, n int) []string {
	var out []string
	for _, c := range Closest(list, q, n) {
		if c.Score >= suggestScore {
			out = append(out, c.Name)
		}
	}
	return out
}

// suggestScore: only names that are actually close are suggested ("calculator" is 0.63 from
// "Google Photos" by sound).
const suggestScore = 0.65
