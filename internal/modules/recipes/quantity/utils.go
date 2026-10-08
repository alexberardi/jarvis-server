package quantity

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// CommonUnits is url_parsing/constants.COMMON_UNITS: the unit tokens is_known_unit accepts
// (after NormalizeUnitToken).
var CommonUnits = map[string]bool{
	"tsp": true, "teaspoon": true, "tbsp": true, "tablespoon": true, "c": true, "cup": true,
	"oz": true, "ounce": true, "fl": true, "fl-oz": true, "pint": true, "pt": true, "quart": true,
	"qt": true, "gallon": true, "gal": true, "g": true, "gram": true, "kg": true, "lb": true,
	"lbs": true, "pound": true, "ml": true, "l": true, "liter": true, "litre": true, "stick": true,
	"clove": true, "slice": true, "can": true, "package": true, "pkg": true, "packet": true,
	"bunch": true, "head": true, "ear": true, "piece": true,
}

// fractionMap is url_parsing/constants.FRACTION_MAP, in the legacy (insertion) order.
var fractionMap = [][2]string{
	{"¼", "1/4"}, {"½", "1/2"}, {"¾", "3/4"}, {"⅐", "1/7"}, {"⅑", "1/9"}, {"⅒", "1/10"},
	{"⅓", "1/3"}, {"⅔", "2/3"}, {"⅕", "1/5"}, {"⅖", "2/5"}, {"⅗", "3/5"}, {"⅘", "4/5"},
	{"⅙", "1/6"}, {"⅚", "5/6"}, {"⅛", "1/8"}, {"⅜", "3/8"}, {"⅝", "5/8"}, {"⅞", "7/8"},
}

// FractionChars is FRACTION_CHARS.
const FractionChars = "¼½¾⅐⅑⅒⅓⅔⅕⅖⅗⅘⅙⅚⅛⅜⅝⅞"

var (
	wsRun        = regexp.MustCompile(`[` + PySpace + `]+`)
	digitFrac    = regexp.MustCompile(`(` + PyDigit + `)([` + FractionChars + `])`)
	pureNumber   = regexp.MustCompile(`^-?` + PyDigit + `+(?:\.` + PyDigit + `+)?$`)
	isoDuration  = regexp.MustCompile(`^P(?:(` + PyDigit + `+)D)?T?(?:(` + PyDigit + `+)H)?(?:(` + PyDigit + `+)M)?(?:(` + PyDigit + `+)S)?`)
	minutesText  = regexp.MustCompile(`(?i)(` + PyDigit + `+)[` + PySpace + `]*(min|minute|minutes)`)
	firstNumber  = regexp.MustCompile(PyDigit + `+`)
	servingsText = []*regexp.Regexp{
		regexp.MustCompile(`(?i)serves[` + PySpace + `]+(` + PyDigit + `+)`),
		regexp.MustCompile(`(?i)serves?:[` + PySpace + `]*(` + PyDigit + `+)`),
		regexp.MustCompile(`(?i)yields?:[` + PySpace + `]*(` + PyDigit + `+)`),
	}
	asciiLeadingQty = regexp.MustCompile(`^[0-9/]`)
)

// CleanText is parsing_utils.clean_text: whitespace runs collapse to one space, then strip.
func CleanText(s string) string { return PyStrip(wsRun.ReplaceAllString(s, " ")) }

// NormalizeUnitToken is parsing_utils.normalize_unit_token: lowercase, strip dots, drop one
// trailing "s" (B25 freezes "glass" → "glas").
func NormalizeUnitToken(unit string) string {
	t := strings.Trim(strings.ToLower(unit), ".")
	return strings.TrimSuffix(t, "s")
}

// IsKnownUnit is parsing_utils.is_known_unit.
func IsKnownUnit(unit string) bool { return CommonUnits[NormalizeUnitToken(unit)] }

// NormalizeFractionDisplay is parsing_utils.normalize_fraction_display. A nil or empty input
// is returned as is; an all-whitespace one becomes nil.
func NormalizeFractionDisplay(qty *string) *string {
	if qty == nil || *qty == "" {
		return qty
	}
	s := digitFrac.ReplaceAllString(*qty, "$1 $2")
	for _, kv := range fractionMap {
		s = strings.ReplaceAll(s, kv[0], kv[1])
	}
	s = PyStrip(wsRun.ReplaceAllString(s, " "))
	if pureNumber.MatchString(s) {
		if f, err := strconv.ParseFloat(asciiDigits(s), 64); err == nil {
			if f == math.Trunc(f) {
				s = pyIntOfFloat(f)
			} else {
				s = PyFloatRepr(f)
			}
		}
	}
	if s == "" {
		return nil
	}
	return &s
}

// ParseISODuration is parsing_utils.parse_iso8601_duration: minutes from a "PT1H30M"-style
// prefix, seconds rounding to a minute at 30; zero is nil. Fixing B25, a day part counts
// ("P1DT2H" = 1560; legacy ignored the whole duration).
func ParseISODuration(s string) *int {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	n := func(g string) int {
		v, _ := PyInt(g)
		return v
	}
	total := n(m[1])*1440 + n(m[2])*60 + n(m[3])
	if n(m[4]) >= 30 {
		total++
	}
	if total == 0 {
		return nil
	}
	return &total
}

// ParseMinutes is parsing_utils.parse_minutes over a JSON value: a number truncates, a string
// is an ISO duration or "<n> min…".
func ParseMinutes(v any) *int {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if d := ParseISODuration(x); d != nil {
			return d
		}
		if m := minutesText.FindStringSubmatch(x); m != nil {
			if n, ok := PyInt(m[1]); ok {
				return &n
			}
		}
		return nil
	}
	return numberToInt(v)
}

// ParseServings is parsing_utils.parse_servings: a number truncates, a string's first integer.
func ParseServings(v any) *int {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if m := firstNumber.FindString(x); m != "" {
			if n, ok := PyInt(m); ok {
				return &n
			}
		}
		return nil
	}
	return numberToInt(v)
}

// numberToInt is Python's int() of an int, float or bool (truncating toward zero).
func numberToInt(v any) *int {
	var f float64
	switch x := v.(type) {
	case bool:
		if x {
			f = 1
		}
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case float64:
		f = x
	case interface{ Float64() (float64, error) }: // json.Number
		var err error
		if f, err = x.Float64(); err != nil {
			return nil
		}
	default:
		return nil
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	n := int(math.Trunc(f))
	return &n
}

// ParseServingsFromText is parsing_utils.parse_servings_from_text ("Serves 4", "Yield: 12").
func ParseServingsFromText(text string) *int {
	if text == "" {
		return nil
	}
	lowered := strings.ToLower(text)
	for _, re := range servingsText {
		if m := re.FindStringSubmatch(lowered); m != nil {
			if n, ok := PyInt(m[1]); ok {
				return &n
			}
		}
	}
	return nil
}

// splitUnits is the unit set of parse_job_service._split_qty_unit.
var splitUnits = map[string]bool{
	"cup": true, "cups": true, "teaspoon": true, "teaspoons": true, "tsp": true, "tbsp": true,
	"tablespoon": true, "tablespoons": true, "ounce": true, "ounces": true, "oz": true,
	"pound": true, "pounds": true, "lb": true, "lbs": true, "gram": true, "grams": true, "g": true,
	"kg": true, "milliliter": true, "milliliters": true, "ml": true, "liter": true, "liters": true,
	"l": true, "pinch": true, "pinches": true, "clove": true, "cloves": true, "can": true,
	"cans": true, "package": true, "packages": true, "stick": true, "sticks": true, "slice": true,
	"slices": true, "piece": true, "pieces": true,
}

// SplitQtyUnit is parse_job_service._split_qty_unit: split "3/4 teaspoon" into ("3/4",
// "teaspoon"), preferring a unit among the first three tokens, then the last token.
func SplitQtyUnit(qty string) (q, unit *string) {
	if qty == "" {
		return nil, nil
	}
	r := strings.NewReplacer("(", " ", ")", " ", ",", " ")
	tokens := PySplit(r.Replace(qty))
	if len(tokens) < 2 {
		return &qty, nil
	}
	shouldSplit := func(p string) bool { return p == "" || asciiLeadingQty.MatchString(p) }
	result := func(p, u string) (*string, *string) {
		u = strings.TrimRight(u, ".,")
		if p == "" {
			return nil, &u
		}
		return &p, &u
	}
	for idx, tok := range tokens[:min(3, len(tokens))] {
		if splitUnits[strings.Trim(strings.ToLower(tok), ".,")] {
			p := PyStrip(strings.Join(tokens[:idx], " "))
			if !shouldSplit(p) {
				continue
			}
			return result(p, tok)
		}
	}
	last := tokens[len(tokens)-1]
	if splitUnits[strings.Trim(strings.ToLower(last), ".,")] {
		p := PyStrip(strings.Join(tokens[:len(tokens)-1], " "))
		if shouldSplit(p) {
			return result(p, last)
		}
	}
	return &qty, nil
}
