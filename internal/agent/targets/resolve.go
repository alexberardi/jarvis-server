// Package targets resolves what the user said to one element of a snapshot (docs/agent/
// ELEMENT-LAYER-WORK.md §4): an exact name, after a verb the start of a name or a sound-alike,
// links to the same destination as one, ordinals on video results, and the narrow model pick
// (pick.go). It works on computeruse.Snapshot, so the browser and desktop apps share it; it
// never acts, and a ref it returns is only good for the snapshot it came from.
package targets

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

var (
	// Common role words are redundant with the accessible role: "the search box" is "Search".
	reLeadingDeterminer = regexp.MustCompile(`(?i)^(?:the|a|my)\s+`)
	reRoleSuffix        = regexp.MustCompile(`(?i)\s+(?:box|field|bar|button|link|tab)$`)
	reOrdinal           = regexp.MustCompile(`(?i)^\s*(?:the\s+)?(first|1st|second|2nd|third|3rd|fourth|4th|fifth|5th|sixth|6th|seventh|7th|eighth|8th|ninth|9th|tenth|10th)\s+(video|link|result|search\s+result)(?:\s+(?:on\s+the\s+page|in\s+the\s+results))?[\s.!?]*$`)
)

var ordinalNumbers = map[string]int{
	"first": 1, "1st": 1, "second": 2, "2nd": 2, "third": 3, "3rd": 3, "fourth": 4, "4th": 4,
	"fifth": 5, "5th": 5, "sixth": 6, "6th": 6, "seventh": 7, "7th": 7, "eighth": 8, "8th": 8,
	"ninth": 9, "9th": 9, "tenth": 10, "10th": 10,
}

var numberWords = map[string]int{
	"zero": 0, "one": 1, "two": 2, "to": 2, "too": 2, "three": 3, "four": 4, "for": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
	"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	"hundred": 100, "a hundred": 100, "one hundred": 100,
}

const (
	nearScore  = 0.8 // winmatch.Similarity a misheard name needs ("adil" for "Adele")
	nearMargin = 0.1 // ahead of the next different name by this much
)

// SpokenNumber reads "30", "thirty", "twenty-five", "a hundred": spoken durations ("skip ahead
// thirty seconds"). Page elements are never chosen by number.
func SpokenNumber(s string) (int, bool) {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), ".!?,"))
	if n, err := strconv.Atoi(s); err == nil {
		return n, n > 0
	}
	if n, ok := numberWords[s]; ok {
		return n, n > 0
	}
	total, seen := 0, false
	for _, w := range strings.Fields(strings.NewReplacer("-", " ", " and ", " ").Replace(s)) {
		n, ok := numberWords[w]
		if !ok {
			return 0, false
		}
		if n == 100 {
			total = max(total, 1) * 100
		} else {
			total += n
		}
		seen = true
	}
	return total, seen && total > 0
}

// Resolve is the element said names: a unique exact name (a repeated one only when every copy is
// the same link destination), and with loose (a verb was said) also the start of a name and a
// sound-alike clearly ahead of any other name. Descriptions and ambiguous names are the pick's.
func Resolve(s computeruse.Snapshot, said string, loose bool) (string, bool) {
	said = strings.TrimSpace(reLeadingDeterminer.ReplaceAllString(strings.TrimSpace(said), ""))
	keys := []string{labelKey(said), labelKey(reRoleSuffix.ReplaceAllString(said, ""))}
	els := s.Elements
	for _, k := range keys {
		if k == "" {
			continue
		}
		var matches []int
		for i := range els {
			if labelKey(els[i].Name) == k {
				matches = append(matches, i)
			}
		}
		if ref, ok := equivalent(els, matches); ok {
			return ref, true
		}
	}
	if !loose {
		return "", false
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		var matches []int
		for i := range els {
			if key := labelKey(els[i].Name); key == k || strings.HasPrefix(key, k+" ") {
				matches = append(matches, i)
			}
		}
		if ref, ok := equivalent(els, matches); ok {
			return ref, true
		}
	}
	// A misheard name: the best by sound or spelling, clearly ahead of any other name. Links to
	// the same destination count as one name.
	type scored struct {
		score float64
		ref   string
	}
	groups := map[string]scored{}
	var order []string
	for i := range els {
		e := &els[i]
		sc := winmatch.Similarity(keys[0], labelKey(e.Name))
		group := "ref:" + e.Ref
		if e.Role == "link" && e.Target != "" {
			group = "target:" + e.Target
		}
		old, seen := groups[group]
		if !seen {
			order = append(order, group)
		}
		if !seen || sc > old.score {
			groups[group] = scored{sc, e.Ref}
		}
	}
	best, second, bestRef := 0.0, 0.0, ""
	for _, g := range order {
		c := groups[g]
		switch {
		case c.score > best:
			second = best
			best, bestRef = c.score, c.ref
		case c.score > second:
			second = c.score
		}
	}
	if best >= nearScore && best-second >= nearMargin {
		return bestRef, true
	}
	return "", false
}

// equivalent is the one element matches (indexes into els) names, or, when they are several, the
// first when all are links to the same destination (a result's thumbnail and its title).
func equivalent(els []computeruse.SnapElement, matches []int) (string, bool) {
	if len(matches) == 0 {
		return "", false
	}
	first := &els[matches[0]]
	if len(matches) == 1 {
		return first.Ref, true
	}
	if first.Target == "" || first.Role != "link" {
		return "", false
	}
	for _, i := range matches[1:] {
		if els[i].Role != "link" || els[i].Target != first.Target {
			return "", false
		}
	}
	return first.Ref, true
}

// Ordinal is "the first video", "the second result": only where the meaning is certain, on a
// video results page (or any page, for "video") among links with a recognizable video
// destination, a thumbnail and its title counting once. Never for desktop controls (A2).
func Ordinal(s computeruse.Snapshot, said string) (string, bool) {
	m := reOrdinal.FindStringSubmatch(said)
	if m == nil {
		return "", false
	}
	want, ok := ordinalNumbers[strings.ToLower(m[1])]
	if !ok {
		return "", false
	}
	kind := strings.ToLower(strings.Join(strings.Fields(m[2]), " "))
	if kind != "video" && !(isVideoResultsPage(s.URL) && (kind == "link" || kind == "result" || kind == "search result")) {
		return "", false
	}
	seen := map[string]bool{}
	position := 0
	for i := range s.Elements {
		e := &s.Elements[i]
		if e.Role != "link" {
			continue
		}
		key := VideoKey(e.Target)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		position++
		if position == want {
			return e.Ref, true
		}
	}
	return "", false
}

func isVideoResultsPage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if isDomain(host, "youtube.com") && u.Path == "/results" {
		return true
	}
	if isDomain(host, "google.com") && u.Path == "/search" {
		q := u.Query()
		return q.Get("tbm") == "vid" || q.Get("udm") == "7"
	}
	return isDomain(host, "bing.com") && u.Path == "/videos/search"
}

// VideoKey is a stable identity for a common video URL ("youtube:ID"), unwrapping Google's result
// redirects; "" for anything else. Other links are never guessed to be videos.
func VideoKey(raw string) string { return videoKeyAt(raw, 0) }

func videoKeyAt(raw string, depth int) string {
	if depth > 4 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if isDomain(host, "google.com") && (u.Path == "/url" || u.Path == "/url/") {
		for _, q := range []string{u.Query().Get("q"), u.Query().Get("url")} {
			if key := videoKeyAt(q, depth+1); key != "" {
				return key
			}
		}
		return ""
	}
	parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
	switch {
	case host == "youtu.be":
		if len(parts) > 0 {
			return "youtube:" + parts[0]
		}
	case isDomain(host, "youtube.com"):
		if u.Path == "/watch" && u.Query().Get("v") != "" {
			return "youtube:" + u.Query().Get("v")
		}
		if len(parts) > 1 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") {
			return "youtube:" + parts[1]
		}
	case isDomain(host, "vimeo.com"):
		for i, part := range parts {
			if part == "video" && i+1 < len(parts) {
				return "vimeo:" + parts[i+1]
			}
		}
		if len(parts) > 0 && isDigits(parts[0]) {
			return "vimeo:" + parts[0]
		}
	case host == "dai.ly":
		if len(parts) > 0 {
			return "dailymotion:" + parts[0]
		}
	case isDomain(host, "dailymotion.com"):
		for i, part := range parts {
			if part == "video" && i+1 < len(parts) {
				return "dailymotion:" + strings.SplitN(parts[i+1], "_", 2)[0]
			}
		}
	}
	return ""
}

func isDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// labelKey is a name or what was said for one, compared: lower case, words only, number words
// as digits ("Reply two" is "reply 2").
func labelKey(s string) string {
	w := strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			return unicode.ToLower(r)
		}
		return ' '
	}, s))
	for i, x := range w {
		if n, ok := numberWords[x]; ok && n > 0 && x != "to" && x != "too" && x != "for" {
			w[i] = strconv.Itoa(n)
		}
	}
	return strings.Join(w, " ")
}
