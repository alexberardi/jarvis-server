// Package quantity holds the recipes module's pure ingredient-text helpers, ported from
// jarvis-recipes-server (quantity_parser, url_parsing/parsing_utils, url_parsing/ingredient_parser,
// parse_job_service._split_qty_unit). The golden fixtures in fixtures/golden/recipes/quantity.json and
// ingredients.json are the contract (docs/recipes/00-inventory.md §12).
//
// The legacy code is Python, whose str methods and `re` classes are Unicode-aware where Go's are
// ASCII-only (`\d`, `\s`, `\b`, str.strip/split). The helpers here reproduce Python's behaviour.
package quantity

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
)

// PySpace is the character class body matching Python's str.isspace() (and so `\s` in a str
// pattern): ASCII whitespace, the information separators, NEL, NBSP and the Unicode spaces.
const PySpace = `\t\n\x0b\f\r\x1c-\x1f \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// PyDigit is `\d` in a Python str pattern: any Unicode decimal digit.
const PyDigit = `\p{Nd}`

// IsPySpace reports whether r is whitespace to Python.
func IsPySpace(r rune) bool {
	switch {
	case r == ' ', r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x1f, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// PyStrip is Python's str.strip().
func PyStrip(s string) string { return strings.TrimFunc(s, IsPySpace) }

// PySplit is Python's str.split() with no separator.
func PySplit(s string) []string { return strings.FieldsFunc(s, IsPySpace) }

// DigitValue returns the value of a Unicode decimal digit (Nd), or -1.
func DigitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	if !unicode.Is(unicode.Nd, r) {
		return -1
	}
	// Every Nd range in the Unicode tables starts at a zero digit and runs in blocks of ten.
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	return -1
}

// asciiDigits maps every Unicode decimal digit in s to its ASCII digit.
func asciiDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r > '9' {
			if v := DigitValue(r); v >= 0 {
				return '0' + rune(v)
			}
		}
		return r
	}, s)
}

// PyInt is Python's int() over a run of Unicode digits; ok is false if it isn't one or overflows.
func PyInt(s string) (int, bool) {
	n, err := strconv.Atoi(asciiDigits(s))
	return n, err == nil
}

// PyFloatRepr is Python's repr() of a finite float: the shortest round-trip digits, fixed
// notation for exponents -4..15, scientific ("1e+16", "1.5e-05") otherwise.
func PyFloatRepr(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // e.g. "-1.5e-05"
	mant, expS, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expS)
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	sign := ""
	if neg {
		sign = "-"
	}
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := strconv.Itoa(abs(exp))
		if len(es) < 2 {
			es = "0" + es
		}
		if exp < 0 {
			return sign + m + "e-" + es
		}
		return sign + m + "e+" + es
	}
	var out string
	switch {
	case exp < 0:
		out = "0." + strings.Repeat("0", -exp-1) + digits
	case exp+1 >= len(digits):
		out = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	default:
		out = digits[:exp+1] + "." + digits[exp+1:]
	}
	return sign + out
}

// pyIntOfFloat is str(int(f)) for an integral float: exact, however large.
func pyIntOfFloat(f float64) string {
	if f == 0 {
		return "0"
	}
	i, _ := big.NewFloat(f).Int(nil)
	return i.String()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
