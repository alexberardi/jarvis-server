package winmatch

import "strings"

// Misheard app names: speech-to-text spells an app it doesn't know the way it sounds
// ("Omerite" for omawrite, "slak" for slack). When nothing else names a window, the query is
// compared with every app's name by edit distance, and a near spelling counts only when it is
// close (FuzzyMaxDistance) and exactly one app is that close.

// Fuzzy thresholds: a name and a query are near when their Damerau-Levenshtein distance is at
// most FuzzyMaxDistance and at most FuzzyMaxRatio of the longer one's length, and the longer
// one has at least fuzzyMinLen letters (short names are too easy to hit by accident: "foot"
// and "food").
const (
	FuzzyMaxDistance = 2
	FuzzyMaxRatio    = 0.3
	fuzzyMinLen      = 5
)

// fuzzyHits marks the windows of apps whose name is a near spelling of q, and counts how many
// different apps that is.
func fuzzyHits(cands []Candidate, q string) ([]bool, int) {
	hits := make([]bool, len(cands))
	var forms []string
	if cq := compact(q); cq != "" {
		forms = append(forms, cq)
	}
	if words := strings.Fields(q); len(words) > 1 {
		for _, w := range words {
			if cw := compact(w); cw != "" {
				forms = append(forms, cw)
			}
		}
	}
	if len(forms) == 0 {
		return hits, 0
	}
	apps := map[string]bool{}
	for i, c := range cands {
		if !nearApp(c.Apps, forms) {
			continue
		}
		hits[i] = true
		apps[compact(lastSeg(c.App()))] = true
	}
	return hits, len(apps)
}

// nearApp: one of the app's names (whole, or a reverse-DNS id's last part) is near one of the
// query's forms, by spelling or by sound (sound.go: "oh my right" for omawrite).
func nearApp(apps []string, forms []string) bool {
	for _, a := range apps {
		for _, name := range []string{compact(a), compact(lastSeg(a))} {
			if name == "" {
				continue
			}
			for _, f := range forms {
				if Near(f, name) || SoundsLike(f, name) {
					return true
				}
			}
		}
	}
	return false
}

// Near reports whether two compacted names are within the fuzzy thresholds.
func Near(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	n := max(len(ra), len(rb))
	if n < fuzzyMinLen {
		return false
	}
	limit := min(FuzzyMaxDistance, int(FuzzyMaxRatio*float64(n)))
	if abs(len(ra)-len(rb)) > limit {
		return false
	}
	return distance(ra, rb) <= limit
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// distance is the optimal-string-alignment Damerau-Levenshtein distance: insertions,
// deletions, substitutions and swaps of two neighbours each cost 1.
func distance(a, b []rune) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}
