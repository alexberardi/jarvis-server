package live

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Module-level helpers of the gateway's turn pipeline (services/turn_pipeline.py) and the
// wrap-up assessment parser (services/dial_worker.py).

const (
	// LoopRepeatLimit: the same far-end line this many times ends the call gracefully.
	LoopRepeatLimit = 3
	// LoopSimilarity is the difflib ratio at which two lines count as the same.
	LoopSimilarity = 0.85
)

var (
	timeHintRE  = regexp.MustCompile(`(?i)\b(mon|tue|wed|thu|fri|sat|sun|noon|midnight|\d{1,2}\s*(?:o'?clock|am|pm|a\.m|p\.m)|at\s+\d{1,2})`)
	nonSpeechRE = regexp.MustCompile(`^(?:[\s\W]*(?:\[[^\]]*\]|\([^)]*\))[\s\W]*)+$`)
	// AffirmRE is a bare affirmation; confirming a value the caller read back is a leak.
	AffirmRE      = regexp.MustCompile(`(?i)^\s*(yes|yeah|yep|yup|correct|right|that'?s (right|correct)|that is (right|correct)|confirmed|affirmative|exactly)\b`)
	nonAlnumSpace = regexp.MustCompile(`[^a-z0-9 ]`)
	assessJSONRE  = regexp.MustCompile(`(?s)\{.*\}`)
)

// MightProposeTime is the loose gate that keeps a non-scheduling turn from running the
// availability check: a weekday, a clock-ish number, or noon/midnight.
func MightProposeTime(text string) bool { return timeHintRE.MatchString(text) }

// IsNonSpeech reports a transcript made only of whisper non-speech markers ([BLANK_AUDIO],
// (silence), ...): silence, not an utterance.
func IsNonSpeech(heard string) bool {
	s := pyStrip(heard)
	return s != "" && nonSpeechRE.MatchString(s)
}

// IsAffirmation reports a sentence that opens with a bare affirmation.
func IsAffirmation(s string) bool { return AffirmRE.MatchString(s) }

// NormalizeLine lowercases, replaces non-alphanumerics with spaces and collapses whitespace.
func NormalizeLine(s string) string {
	return strings.Join(strings.Fields(nonAlnumSpace.ReplaceAllString(strings.ToLower(s), " ")), " ")
}

// SimilarLine reports two lines as essentially the same (equal after normalisation, or a
// difflib ratio of at least LoopSimilarity).
func SimilarLine(a, b string) bool {
	na, nb := NormalizeLine(a), NormalizeLine(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb || SequenceRatio(na, nb) >= LoopSimilarity
}

// SequenceRatio is Python's difflib.SequenceMatcher(None, a, b).ratio() over code points,
// autojunk included.
func SequenceRatio(a, b string) float64 {
	ar, br := []rune(a), []rune(b)
	la, lb := len(ar), len(br)
	if la+lb == 0 {
		return 1.0
	}
	// b2j with the autojunk "popular" heuristic.
	b2j := map[rune][]int{}
	for i, r := range br {
		b2j[r] = append(b2j[r], i)
	}
	if lb >= 200 {
		ntest := lb/100 + 1
		for r, idx := range b2j {
			if len(idx) > ntest {
				delete(b2j, r)
			}
		}
	}
	longest := func(alo, ahi, blo, bhi int) (int, int, int) {
		besti, bestj, bestsize := alo, blo, 0
		j2len := map[int]int{}
		for i := alo; i < ahi; i++ {
			newj2len := map[int]int{}
			for _, j := range b2j[ar[i]] {
				if j < blo {
					continue
				}
				if j >= bhi {
					break
				}
				k := j2len[j-1] + 1
				newj2len[j] = k
				if k > bestsize {
					besti, bestj, bestsize = i-k+1, j-k+1, k
				}
			}
			j2len = newj2len
		}
		// No isjunk function, so bjunk is empty: extend over (popular) equal elements.
		for besti > alo && bestj > blo && ar[besti-1] == br[bestj-1] {
			besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
		}
		for besti+bestsize < ahi && bestj+bestsize < bhi && ar[besti+bestsize] == br[bestj+bestsize] {
			bestsize++
		}
		return besti, bestj, bestsize
	}
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, la, 0, lb}}
	matches := 0
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		i, j, k := longest(q.alo, q.ahi, q.blo, q.bhi)
		if k == 0 {
			continue
		}
		matches += k
		if q.alo < i && q.blo < j {
			queue = append(queue, span{q.alo, i, q.blo, j})
		}
		if i+k < q.ahi && j+k < q.bhi {
			queue = append(queue, span{i + k, q.ahi, j + k, q.bhi})
		}
	}
	return 2.0 * float64(matches) / float64(la+lb)
}

// ParseAssessment extracts (summary, goal_achieved) from the wrap-up model's JSON reply,
// tolerating fences and prose. No usable JSON (or a non-boolean verdict) gives the raw text
// as the summary and a nil verdict.
func ParseAssessment(content string) (string, *bool) {
	text := pyStrip(content)
	if m := assessJSONRE.FindString(text); m != "" {
		var data any
		if err := json.Unmarshal([]byte(m), &data); err == nil {
			if obj, ok := data.(map[string]any); ok {
				summary := pyStrip(pyStrOr(obj["summary"]))
				if summary == "" {
					summary = text
				}
				if ga, ok := obj["goal_achieved"].(bool); ok {
					return summary, &ga
				}
				return summary, nil
			}
		}
	}
	return text, nil
}

// sortedKeys returns a map's keys in order (deterministic signatures).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
