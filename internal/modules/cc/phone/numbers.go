package phone

import (
	"errors"
	"regexp"
	"strings"

	"github.com/dlclark/regexp2"
)

// Number validation, scraped-number and address extraction, and the location check
// (phone_call_service.normalize_us_number, phone_number_search.py). One validator gates the
// confirm tap, every phonebook write and every scraped number.

// NumberError is a user-presentable validation failure (NumberValidationError).
type NumberError struct{ msg string }

func (e *NumberError) Error() string { return e.msg }

func numErr(msg string) error { return &NumberError{msg} }

// IsNumberError reports whether err is a NumberError.
func IsNumberError(err error) bool {
	var ne *NumberError
	return errors.As(err, &ne)
}

var (
	premiumAreaCodes = map[string]bool{"900": true, "976": true}
	emergencyNumbers = map[string]bool{"911": true, "112": true, "933": true, "988": true}
	nonDialRE        = regexp.MustCompile(`[^\d+]`)
)

// NormalizeUS normalizes a US number to E.164 (+1XXXXXXXXXX) or returns a NumberError with
// the legacy message. US only; emergency, short-code and premium-rate numbers are refused.
func NormalizeUS(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", numErr("No phone number provided.")
	}
	digits := nonDialRE.ReplaceAllString(raw, "")
	if emergencyNumbers[digits] || emergencyNumbers[strings.TrimLeft(digits, "+")] {
		return "", numErr("Emergency and service numbers can't be called.")
	}
	d := strings.TrimLeft(digits, "+")
	if len(d) == 11 && d[0] == '1' {
		d = d[1:]
	}
	if len(d) != 10 {
		if len(d) < 7 {
			return "", numErr("Short codes and service numbers can't be called.")
		}
		return "", numErr("Only US numbers are supported right now (10 digits).")
	}
	area := d[:3]
	if premiumAreaCodes[area] {
		return "", numErr("Premium-rate numbers can't be called.")
	}
	if area[0] == '0' || area[0] == '1' {
		return "", numErr("That doesn't look like a valid US number.")
	}
	// Python's [^\d+] keeps any Unicode digit; a non-ASCII digit would survive to here.
	for i := 0; i < len(d); i++ {
		if d[i] < '0' || d[i] > '9' {
			return "", numErr("That doesn't look like a valid US number.")
		}
	}
	return "+1" + d, nil
}

var nameStripRE = regexp.MustCompile(`[^a-z0-9 ]`)

// NormalizeName is _normalize_name: lowercase, strip [^a-z0-9 ], trim.
func NormalizeName(name string) string {
	return strings.TrimSpace(nameStripRE.ReplaceAllString(strings.ToLower(name), ""))
}

// phoneRE is _PHONE_RE: US listing shapes with a REQUIRED separator, never bare digit runs.
var phoneRE = regexp2.MustCompile(`(?<![\d-])(?:\+?1[\s.\-]*)?(?:\((\d{3})\)|(\d{3}))[\s.\-]+(\d{3})[\s.\-]+(\d{4})(?![\d-])`, regexp2.None)

// addressRE is _ADDRESS_RE: strict, anchored by a state abbreviation or ZIP, with the street
// type ending on a word boundary ("2 yrs and am so pleased" is not an address).
var addressRE = regexp2.MustCompile(`\b\d{1,6}\s+
        (?:[A-Za-z0-9.\-]+\ ){0,5}
        (?:Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Drive|Dr|Lane|Ln|
           Way|Highway|Hwy|Route|Rt|Place|Pl|Court|Ct|Parkway|Pkwy)
        \b\.?
        (?:\ \d{1,4}[A-Za-z]?\b)?
        (?:\s*,?\s*(?:Suite|Ste|Unit|Apt|Bldg|Floor|Fl|\#)\s*[A-Za-z0-9\-]+)?
        (?:\s*,?\s*[A-Za-z.\-]+(?:\ [A-Za-z.\-]+){0,2})?
        \s*,?\s*
        (?:
            (?-i:[A-Z]{2})\b(?:\s+\d{5}(?:-\d{4})?)?
          | \d{5}(?:-\d{4})?
        )`, regexp2.IgnoreCase|regexp2.IgnorePatternWhitespace)

// ExtractNumber returns the first E.164-normalizable US number in text, skipping any number
// in skip (D40 11.Q4: do-not-call numbers never come back from a search).
func ExtractNumber(text string, skip map[string]bool) string {
	m, _ := phoneRE.FindStringMatch(text)
	for m != nil {
		g := m.Groups()
		area := g[1].String()
		if area == "" {
			area = g[2].String()
		}
		if n, err := NormalizeUS(area + g[3].String() + g[4].String()); err == nil && !skip[n] {
			return n
		}
		m, _ = phoneRE.FindNextMatch(m)
	}
	return ""
}

// ExtractAddress returns the first strict street address in text, or "".
func ExtractAddress(text string) string {
	m, _ := addressRE.FindStringMatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.String())
}

var (
	usStates = func() map[string]bool {
		m := map[string]bool{}
		for _, s := range strings.Fields(`AL AK AZ AR CA CO CT DE FL GA HI ID IL IN IA KS KY LA ME MD MA
			MI MN MS MO MT NE NV NH NJ NM NY NC ND OH OK OR PA RI SC SD TN TX UT VT VA WA WV WI WY DC`) {
			m[s] = true
		}
		return m
	}()
	stateRE = regexp.MustCompile(`\b([A-Z]{2})\b`)
)

// stateOf is _state_of: the LAST US state abbreviation in text (case-sensitive on purpose).
func stateOf(text string) string {
	last := ""
	for _, m := range stateRE.FindAllStringSubmatch(text, -1) {
		if usStates[m[1]] {
			last = m[1]
		}
	}
	return last
}

// LocationMismatch warns when address is clearly in a different US state from location.
// Anything less clear-cut (a side without a state, the same state) returns "".
func LocationMismatch(address, location string) string {
	if address == "" || location == "" {
		return ""
	}
	a, h := stateOf(address), stateOf(location)
	if a == "" || h == "" || a == h {
		return ""
	}
	return "⚠️ This result is in " + a + " but your household is in " + h +
		" — check it's the right location before calling."
}
