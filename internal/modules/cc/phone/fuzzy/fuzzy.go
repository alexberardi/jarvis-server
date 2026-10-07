// Package fuzzy is a port of rapidfuzz 3.x fuzz.WRatio (fuzz_py.py, the reference
// implementation of the C++ scorer), used by the phone plan step to match a spoken business
// name against the household phonebook (docs/cc/11-phone.md §3.3 step 2, D40 11.Q5).
//
// Score cutoffs are not ported: in WRatio every cutoff only zeroes a sub-score that could not
// win the final max, so scoring without them gives the same number. ExtractOne differs from
// process.extractOne only in being deterministic on ties (the earliest choice wins).
package fuzzy

import (
	"sort"
	"strings"
)

const unbaseScale = 0.95

// WRatio is rapidfuzz fuzz.WRatio with processor=None: 0..100, 0 when either side is empty.
func WRatio(s1, s2 string) float64 {
	r1, r2 := []rune(s1), []rune(s2)
	if len(r1) == 0 || len(r2) == 0 {
		return 0
	}
	len1, len2 := float64(len(r1)), float64(len(r2))
	lenRatio := len2 / len1
	if len1 > len2 {
		lenRatio = len1 / len2
	}
	end := ratio(r1, r2)
	if lenRatio < 1.5 {
		return max(end, tokenRatio(s1, s2)*unbaseScale)
	}
	partialScale := 0.9
	if lenRatio > 8.0 {
		partialScale = 0.6
	}
	end = max(end, partialRatio(r1, r2)*partialScale)
	return max(end, partialTokenRatio(s1, s2)*unbaseScale*partialScale)
}

// Choice is one candidate for ExtractOne.
type Choice struct {
	Key  string
	Text string
}

// ExtractOne scores query against every choice with WRatio and returns the best one scoring
// at least cutoff. Ties go to the earliest choice in the slice.
func ExtractOne(query string, choices []Choice, cutoff float64) (best Choice, score float64, ok bool) {
	for _, c := range choices {
		s := WRatio(query, c.Text)
		if s < cutoff {
			continue
		}
		if !ok || s > score {
			best, score, ok = c, s, true
		}
	}
	return best, score, ok
}

// lcs is the longest common subsequence length (Indel similarity).
func lcs(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// indelDistance is len1+len2-2*LCS.
func indelDistance(a, b []rune) int { return len(a) + len(b) - 2*lcs(a, b) }

// normSim is Indel normalized_similarity (0..1); two empty sequences are identical.
func normSim(a, b []rune) float64 {
	sum := len(a) + len(b)
	if sum == 0 {
		return 1
	}
	return 1 - float64(indelDistance(a, b))/float64(sum)
}

// ratio is fuzz.ratio.
func ratio(a, b []rune) float64 { return normSim(a, b) * 100 }

// partialRatioImpl is _partial_ratio_impl: the best window of s2 (the longer) against s1.
func partialRatioImpl(s1, s2 []rune) float64 {
	set := map[rune]bool{}
	for _, c := range s1 {
		set[c] = true
	}
	len1, len2 := len(s1), len(s2)
	best := 0.0
	try := func(sub []rune) bool {
		if r := normSim(s1, sub); r > best {
			best = r
			return best == 1
		}
		return false
	}
	for i := 1; i < len1; i++ {
		if set[s2[i-1]] && try(s2[:i]) {
			return 100
		}
	}
	for i := 0; i < len2-len1; i++ {
		if set[s2[i+len1-1]] && try(s2[i:i+len1]) {
			return 100
		}
	}
	for i := max(len2-len1, 0); i < len2; i++ {
		if set[s2[i]] && try(s2[i:]) {
			return 100
		}
	}
	return best * 100
}

// partialRatio is fuzz.partial_ratio.
func partialRatio(s1, s2 []rune) float64 {
	if len(s1) == 0 && len(s2) == 0 {
		return 100
	}
	shorter, longer := s1, s2
	if len(s1) > len(s2) {
		shorter, longer = s2, s1
	}
	res := partialRatioImpl(shorter, longer)
	if res != 100 && len(s1) == len(s2) {
		if r2 := partialRatioImpl(longer, shorter); r2 > res {
			res = r2
		}
	}
	return res
}

func sortedTokens(s string) []string {
	t := strings.Fields(s)
	sort.Strings(t)
	return t
}

func tokenSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, t := range strings.Fields(s) {
		m[t] = true
	}
	return m
}

// diff returns the sorted tokens of a that are not in b.
func diff(a, b map[string]bool) []string {
	var out []string
	for t := range a {
		if !b[t] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func intersection(a, b map[string]bool) []string {
	var out []string
	for t := range a {
		if b[t] {
			out = append(out, t)
		}
	}
	return out
}

func joinRunes(t []string) []rune { return []rune(strings.Join(t, " ")) }

func tokenSortRatio(s1, s2 string) float64 {
	return ratio(joinRunes(sortedTokens(s1)), joinRunes(sortedTokens(s2)))
}

func normDistance(dist, lensum int) float64 {
	if lensum == 0 {
		return 100
	}
	return 100 - 100*float64(dist)/float64(lensum)
}

func tokenSetRatio(s1, s2 string) float64 {
	a, b := tokenSet(s1), tokenSet(s2)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	ab, ba := diff(a, b), diff(b, a)
	sect := intersection(a, b)
	if len(sect) > 0 && (len(ab) == 0 || len(ba) == 0) {
		return 100
	}
	abJ, baJ := joinRunes(ab), joinRunes(ba)
	abLen, baLen := len(abJ), len(baJ)
	sectLen := len(joinRunes(sect))
	sep := 0
	if sectLen != 0 {
		sep = 1
	}
	sectAB := sectLen + sep + abLen
	sectBA := sectLen + sep + baLen
	result := normDistance(indelDistance(abJ, baJ), sectAB+sectBA)
	if sectLen == 0 {
		return result
	}
	r1 := normDistance(sep+abLen, sectLen+sectAB)
	r2 := normDistance(sep+baLen, sectLen+sectBA)
	return max(result, r1, r2)
}

func tokenRatio(s1, s2 string) float64 {
	return max(tokenSetRatio(s1, s2), tokenSortRatio(s1, s2))
}

func partialTokenRatio(s1, s2 string) float64 {
	splitA, splitB := strings.Fields(s1), strings.Fields(s2)
	a, b := tokenSet(s1), tokenSet(s2)
	if len(intersection(a, b)) > 0 {
		return 100
	}
	ab, ba := diff(a, b), diff(b, a)
	result := partialRatio(joinRunes(sortedTokens(s1)), joinRunes(sortedTokens(s2)))
	if len(splitA) == len(ab) && len(splitB) == len(ba) {
		return result
	}
	return max(result, partialRatio(joinRunes(ab), joinRunes(ba)))
}
