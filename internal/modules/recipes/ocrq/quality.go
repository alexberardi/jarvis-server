package ocrq

import (
	"regexp"
	"sort"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// Quality is ocr_quality.score_quality's result (stored in pipeline_json).
type Quality struct {
	CharCount      int      `json:"char_count"`
	LineCount      int      `json:"line_count"`
	HardFail       bool     `json:"hard_fail"`
	Gibberish      bool     `json:"gibberish"`
	AlphaRatio     float64  `json:"alpha_ratio"`
	VowelRatio     float64  `json:"vowel_ratio"`
	TokenCount     int      `json:"token_count"`
	VowelfulTokens int      `json:"vowelful_tokens"`
	Score          int      `json:"score"`
	PassGate       bool     `json:"pass_gate"`
	Warnings       []string `json:"warnings"`
}

var (
	alphaRe          = regexp.MustCompile(`[A-Za-z]`)
	tokenRe          = regexp.MustCompile(`[A-Za-z]{2,}`)
	vowelRe          = regexp.MustCompile(`[AEIOUaeiou]`)
	ingredientLikeRe = regexp.MustCompile(`^[` + quantity.PySpace + `]*[` + quantity.PyDigit + `\-/.` + quantity.PySpace + `]+[a-zA-Z]?`)
	stepLikeRe       = regexp.MustCompile(`^[` + quantity.PySpace + `]*` + quantity.PyDigit + `+[).` + quantity.PySpace + `]`)
	qualityKeywords  = []string{"ingredients", "directions", "instructions", "method", "serves", "yield"}
)

// ScoreQuality is score_quality: a hard fail under 250 characters or 10 lines or when the text
// looks like gibberish; otherwise a pass at 2 points (confidence ≥ 50, two keyword lines, an
// ingredient-like line, a numbered step). confidence is on 0–100 (B4 fixed upstream).
func ScoreQuality(text string, confidence *float64) Quality {
	var lines []string
	for _, ln := range splitLines(text) {
		if s := quantity.PyStrip(ln); s != "" {
			lines = append(lines, s)
		}
	}
	runes := []rune(text)
	q := Quality{CharCount: len(runes), LineCount: len(lines), Warnings: []string{}}
	hard := q.CharCount < 250 || q.LineCount < 10

	alpha := len(alphaRe.FindAllStringIndex(text, -1))
	if q.CharCount > 0 {
		q.AlphaRatio = float64(alpha) / float64(q.CharCount)
	}
	tokens := tokenRe.FindAllString(text, -1)
	chars, vowels := 0, 0
	for _, t := range tokens {
		chars += len(t)
		n := len(vowelRe.FindAllStringIndex(t, -1))
		vowels += n
		if n > 0 {
			q.VowelfulTokens++
		}
	}
	if chars == 0 {
		chars = 1
	}
	q.VowelRatio = float64(vowels) / float64(chars)
	q.TokenCount = len(tokens)
	q.Gibberish = q.AlphaRatio < 0.65 || q.VowelRatio < 0.30 || q.VowelfulTokens < 20 || q.TokenCount < 50

	if confidence != nil && *confidence >= 50 {
		q.Score++
	}
	kw := 0
	for _, ln := range lines {
		l := strings.ToLower(ln)
		for _, k := range qualityKeywords {
			if strings.Contains(l, k) {
				kw++
				break
			}
		}
	}
	if kw >= 2 {
		q.Score++
	}
	for _, ln := range lines {
		if ingredientLikeRe.MatchString(ln) {
			q.Score++
			break
		}
	}
	for _, ln := range lines {
		if stepLikeRe.MatchString(ln) {
			q.Score++
			break
		}
	}
	q.HardFail = hard || q.Gibberish
	q.PassGate = !hard && q.Score >= 2 && !q.Gibberish
	return q
}

// splitLines is str.splitlines.
func splitLines(s string) []string {
	var out []string
	rs := []rune(s)
	start := 0
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			if rs[i] == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// Result is one image's reading in a stored reading (ocr_readings).
type Result struct {
	Index      int      `json:"index"`
	Text       string   `json:"ocr_text"`
	Confidence *float64 `json:"confidence,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// Reading is one engine's reading of the photos.
type Reading struct {
	Provider   string   `json:"provider"`
	ReceivedAt string   `json:"received_at,omitempty"`
	Results    []Result `json:"results"`
}

// FromOCR converts the OCR module's readings.
func FromOCR(rs []ocr.Reading) []Reading {
	out := make([]Reading, 0, len(rs))
	for _, r := range rs {
		rd := Reading{Provider: r.Engine, Results: []Result{}}
		for _, ir := range r.Results {
			rd.Results = append(rd.Results, Result{Index: ir.Index, Text: ir.Text, Confidence: ir.Confidence, Error: ir.Error})
		}
		out = append(out, rd)
	}
	return out
}

// Sort is ocr_join.readings_for_llm: best engine first (ENGINE_RANK; unknown engines last),
// then by arrival.
func Sort(rs []Reading) []Reading {
	out := append([]Reading(nil), rs...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := ocr.RankOf(out[i].Provider), ocr.RankOf(out[j].Provider)
		if ri != rj {
			return ri < rj
		}
		return out[i].ReceivedAt < out[j].ReceivedAt
	})
	return out
}

// Combine is ocr_join.combine: one reading's image texts in image order, blank ones dropped,
// joined by a blank line.
func Combine(results []Result) string {
	ordered := append([]Result(nil), results...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	var texts []string
	for _, r := range ordered {
		if r.Text != "" {
			texts = append(texts, r.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// Text is one engine's text for the prompt.
type Text struct {
	Provider, Text string
}

// Texts is the (provider, text) pairs of the sorted readings, empty readings dropped.
func Texts(sorted []Reading) []Text {
	var out []Text
	for _, r := range sorted {
		t := Combine(r.Results)
		if quantity.PyStrip(t) != "" {
			out = append(out, Text{Provider: r.Provider, Text: t})
		}
	}
	return out
}

// GateText is the reading the quality gate judges: the one with the most whitespace-separated
// words (the first on a tie).
func GateText(texts []Text) string {
	best, n := "", -1
	for _, t := range texts {
		if w := len(quantity.PySplit(t.Text)); w > n {
			best, n = t.Text, w
		}
	}
	return best
}

// MeanConfidence is the mean of every image result's confidence (nil when none has one).
func MeanConfidence(rs []Reading) *float64 {
	var sum float64
	n := 0
	for _, r := range rs {
		for _, ir := range r.Results {
			if ir.Confidence != nil {
				sum += *ir.Confidence
				n++
			}
		}
	}
	if n == 0 {
		return nil
	}
	m := sum / float64(n)
	return &m
}
