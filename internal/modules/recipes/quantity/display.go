package quantity

import (
	"math/big"
	"strconv"
	"strings"
)

// dec is a Python Decimal as far as parse_quantity_display needs one: a finite exact value, or
// an infinity, or NaN.
type dec struct {
	r   *big.Rat
	inf int // +1 / -1 for an infinity, 0 when finite
	nan bool
}

func (d dec) finite() bool { return !d.nan && d.inf == 0 }

// parseDecimal is Python's Decimal(str): surrounding whitespace is ignored, underscores between
// digits are allowed, any Unicode decimal digit counts, and "Infinity"/"Inf"/"NaN"/"sNaN" (any
// case, with an optional sign; NaN with an optional payload) are special values.
func parseDecimal(s string) (dec, bool) {
	s = PyStrip(s)
	if s == "" {
		return dec{}, false
	}
	s = asciiDigits(s)
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	low := strings.ToLower(s)
	switch {
	case low == "inf" || low == "infinity":
		if neg {
			return dec{inf: -1}, true
		}
		return dec{inf: 1}, true
	case strings.HasPrefix(low, "nan") && allDigits(low[3:], true):
		return dec{nan: true}, true
	case strings.HasPrefix(low, "snan") && allDigits(low[4:], true):
		return dec{nan: true}, true
	}
	mant, exp, hasExp := s, "", false
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, exp, hasExp = s[:i], s[i+1:], true
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	if intPart == "" && frac == "" {
		return dec{}, false
	}
	if !digitRun(intPart) || !digitRun(frac) {
		return dec{}, false
	}
	num := strings.ReplaceAll(intPart+frac, "_", "")
	if num == "" {
		num = "0"
	}
	r, ok := new(big.Rat).SetString(num)
	if !ok {
		return dec{}, false
	}
	scale := -len(strings.ReplaceAll(frac, "_", ""))
	if hasExp {
		if exp == "" {
			return dec{}, false
		}
		esign := 1
		switch exp[0] {
		case '+':
			exp = exp[1:]
		case '-':
			esign = -1
			exp = exp[1:]
		}
		if exp == "" || !digitRun(exp) {
			return dec{}, false
		}
		e, err := strconv.Atoi(strings.ReplaceAll(exp, "_", ""))
		if err != nil || e > 100000 {
			return dec{}, false
		}
		scale += esign * e
	}
	if scale != 0 {
		p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(scale))), nil)
		if scale > 0 {
			r.Mul(r, new(big.Rat).SetInt(p))
		} else {
			r.Quo(r, new(big.Rat).SetInt(p))
		}
	}
	if neg {
		r.Neg(r)
	}
	return dec{r: r}, true
}

// digitRun accepts "" or ASCII digits with single underscores strictly between digits (PEP 515).
func digitRun(s string) bool {
	if s == "" {
		return true
	}
	if s[0] == '_' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
		return false
	}
	return allDigits(strings.ReplaceAll(s, "_", ""), false)
}

func allDigits(s string, emptyOK bool) bool {
	if s == "" {
		return emptyOK
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// quo is Decimal division for parse_quantity_display: ok is false where Python raises
// (Infinity/Infinity, NaN operands are passed through as NaN).
func quo(n, d dec) (dec, bool) {
	switch {
	case n.nan || d.nan:
		return dec{nan: true}, true
	case n.inf != 0 && d.inf != 0:
		return dec{}, false
	case n.inf != 0:
		return dec{inf: n.inf * d.r.Sign()}, true
	case d.inf != 0:
		return dec{r: new(big.Rat)}, true
	}
	return dec{r: new(big.Rat).Quo(n.r, d.r)}, true
}

func add(a, b dec) (dec, bool) {
	switch {
	case a.nan || b.nan:
		return dec{nan: true}, true
	case a.inf != 0 && b.inf != 0 && a.inf != b.inf:
		return dec{}, false
	case a.inf != 0:
		return a, true
	case b.inf != 0:
		return b, true
	}
	return dec{r: new(big.Rat).Add(a.r, b.r)}, true
}

func isZero(d dec) bool { return d.finite() && d.r.Sign() == 0 }

// parseQuantityDisplay is legacy quantity_parser.parse_quantity_display.
func parseQuantityDisplay(raw string) (dec, bool) {
	value := PyStrip(raw)
	if value == "" {
		return dec{}, false
	}
	if !strings.Contains(value, "/") && !strings.Contains(value, " ") {
		if d, ok := parseDecimal(value); ok {
			return d, true
		}
	}
	fraction := func(s string) (dec, bool) {
		numS, denS, ok := strings.Cut(s, "/")
		if !ok {
			return dec{}, false
		}
		num, ok1 := parseDecimal(numS)
		den, ok2 := parseDecimal(denS)
		if !ok1 || !ok2 || isZero(den) {
			return dec{}, false
		}
		return quo(num, den)
	}
	if strings.Contains(value, " ") {
		wholeS, fracS, _ := strings.Cut(value, " ")
		whole, ok := parseDecimal(wholeS)
		if !ok {
			return dec{}, false
		}
		f, ok := fraction(fracS)
		if !ok {
			return dec{}, false
		}
		return add(whole, f)
	}
	if strings.Contains(value, "/") {
		return fraction(value)
	}
	return dec{}, false
}

// ParseDisplay is the stored quantity_value for a quantity_display: legacy
// parse_quantity_display, rounded to the Numeric(10,4) column's four decimals (half away from
// zero, as Postgres rounds). ok is false for no quantity (empty, garbage, a zero denominator)
// and, fixing B2, for NaN and the infinities, which the legacy response model then refused.
func ParseDisplay(raw string) (float64, bool) {
	d, ok := parseQuantityDisplay(raw)
	if !ok || !d.finite() {
		return 0, false
	}
	return round4(d.r), true
}

// round4 rounds to four decimals, half away from zero.
func round4(r *big.Rat) float64 {
	scaled := new(big.Rat).Mul(r, big.NewRat(10000, 1))
	num, den := scaled.Num(), scaled.Denom()
	q, m := new(big.Int).QuoRem(new(big.Int).Abs(num), den, new(big.Int))
	if new(big.Int).Mul(m, big.NewInt(2)).Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if num.Sign() < 0 {
		q.Neg(q)
	}
	f, _ := new(big.Rat).SetFrac(q, big.NewInt(10000)).Float64()
	if f == 0 {
		return 0 // no negative zero in a numeric column
	}
	return f
}

// Wire renders a stored quantity_value as the legacy API did: the Numeric(10,4) value through
// pydantic's Decimal serialiser, i.e. always four decimals ("1.5000").
func Wire(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

// IsDecimal reports whether s is a valid Python Decimal literal (pydantic's Decimal from str).
func IsDecimal(s string) bool {
	_, ok := parseDecimal(s)
	return ok
}
