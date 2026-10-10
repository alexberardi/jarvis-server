package apps

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
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
	if len(e.Suggestions) == 0 {
		return fmt.Sprintf("%v %q", ErrNotFound, e.Query)
	}
	return fmt.Sprintf("%v %q (closest: %s)", ErrNotFound, e.Query, strings.Join(e.Suggestions, ", "))
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

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

// Resolve picks exactly one app for a spoken name, in tiers, stopping at the first tier with
// any match: exact (name or desktop id, ignoring case and punctuation), then the name starts
// with the query, then every query word appears in the name, then a small edit distance (typos,
// mishearings). A tier with several matches is ambiguous rather than a guess.
func Resolve(list []App, query string) (App, error) {
	q := cleanQuery(query)
	nq := norm(q)
	if nq == "" {
		return App{}, &NotFoundError{Query: query}
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
			return hits[0], nil
		}
		if len(hits) > 1 {
			return App{}, &AmbiguousError{Query: query, Candidates: names(hits, 8)}
		}
	}
	// Fuzzy: allow 1 edit for short names, 2 for longer; the best distance must be unique.
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
			return best[0], nil
		}
		return App{}, &AmbiguousError{Query: query, Candidates: names(best, 8)}
	}
	return App{}, &NotFoundError{Query: query, Suggestions: suggest(list, nq, 5)}
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

// suggest returns the n names closest to the query by edit distance.
func suggest(list []App, nq string, n int) []string {
	type sc struct {
		name string
		d    int
	}
	var all []sc
	for _, a := range list {
		all = append(all, sc{a.Name, levenshtein(nq, norm(a.Name))})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].d < all[j].d })
	var out []string
	for i := 0; i < len(all) && i < n; i++ {
		if all[i].d <= max(2, len(nq)/2) { // only names that are actually close
			out = append(out, all[i].name)
		}
	}
	return out
}
