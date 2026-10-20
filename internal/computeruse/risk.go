package computeruse

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

// Risk levels.
const (
	LevelNone    = "none"
	LevelConfirm = "confirm"
	LevelDeny    = "deny"
)

// Risk categories (COMPUTER-CONTROL §Safety). Each is "confirm" by default; the household's rules
// (cc) can change that. CatDenied is the built-in deny list, which rules cannot lift.
const (
	CatPay              = "pay"
	CatSubmitThirdParty = "submit_third_party"
	CatSend             = "send"
	CatDelete           = "delete"
	CatSharing          = "sharing"
	CatInstall          = "install"
	CatSubscribe        = "subscribe"
	CatUnsubscribe      = "unsubscribe"
	CatSecuritySettings = "security_settings"
	CatCaptcha          = "captcha"
	CatSensitiveInput   = "sensitive_input"
	CatDenied           = "denied"
)

// Risk is Classify's verdict on one action.
type Risk struct {
	Level    string `json:"level"`              // none | confirm | deny
	Category string `json:"category,omitempty"` // one of the Cat* constants
	Reason   string `json:"reason,omitempty"`   // for the confirmation card and the audit log
}

// Element is what the executor knows about the element an action lands on: from the snapshot
// (browser) or the accessibility tree (desktop). Type is the HTML input type or tag ("submit",
// "iframe"); Autocomplete the HTML autocomplete attribute; Secure a password or secure text field.
type Element struct {
	Role         string `json:"role,omitempty"`
	Name         string `json:"name,omitempty"`
	Type         string `json:"type,omitempty"`
	Autocomplete string `json:"autocomplete,omitempty"`
	Secure       bool   `json:"secure,omitempty"`
}

// Classify judges one action on el (nil when acting by coordinates with nothing under the point)
// in target t. The built-in deny list comes first, then the confirm categories; anything else is
// LevelNone. Classify reads names, so an action by coordinates is only judged by what it types:
// the executor should pass the element under the point whenever it can find it.
func Classify(a Action, el *Element, t Target) Risk {
	if deny, why := DenyAction(a, t); deny {
		return Risk{LevelDeny, CatDenied, why}
	}
	if deny, why := BuiltinDeny(t, el); deny {
		return Risk{LevelDeny, CatDenied, why}
	}
	if el != nil && actsOn(a) {
		if w := captchaMatch(el); w != "" {
			return confirm(CatCaptcha, fmt.Sprintf("%s %q looks like a CAPTCHA (%q)", roleOf(el), el.Name, w))
		}
	}

	switch a.Kind {
	case "type", "set_value", "clipboard_write":
		if el != nil && hasCCAutocomplete(el.Autocomplete) {
			return confirm(CatPay, fmt.Sprintf("fills the payment field %q (autocomplete %q)", el.Name, el.Autocomplete))
		}
		if what := sensitiveText(a.Text, el); what != "" {
			return confirm(CatSensitiveInput, "types what looks like "+what)
		}
		return Risk{Level: LevelNone}
	case "keypress":
		if !isEnter(a.Keys) {
			return Risk{Level: LevelNone}
		}
	case "click", "double_click", "triple_click", "press":
	default:
		return Risk{Level: LevelNone}
	}
	if el == nil {
		return Risk{Level: LevelNone}
	}

	ws := words(el.Name)
	for _, l := range riskWords {
		if w := firstPhrase(ws, l.words); w != "" {
			return confirm(l.category, fmt.Sprintf("%s %q matches %q", roleOf(el), el.Name, w))
		}
	}
	name := strings.ToLower(strings.TrimSpace(el.Name))
	for _, s := range installerSuffixes {
		if strings.HasSuffix(name, s) {
			return confirm(CatInstall, fmt.Sprintf("opens the downloaded file %q", el.Name))
		}
	}
	if isSubmit(el, ws) {
		return confirm(CatSubmitThirdParty, fmt.Sprintf("submits a form (%s %q)", roleOf(el), el.Name))
	}
	return Risk{Level: LevelNone}
}

func confirm(cat, reason string) Risk { return Risk{LevelConfirm, cat, reason} }

// actsOn is whether the action does something to the element, rather than look or scroll past it.
func actsOn(a Action) bool {
	switch a.Kind {
	case "click", "double_click", "triple_click", "press", "type", "set_value", "select", "keypress", "drag":
		return true
	}
	return false
}

func isEnter(keys []string) bool {
	if len(keys) != 1 {
		return false
	}
	k := strings.ToLower(keys[0])
	return k == "enter" || k == "return"
}

func roleOf(el *Element) string {
	if el.Role != "" {
		return el.Role
	}
	return "element"
}

func captchaMatch(el *Element) string {
	for _, s := range []string{el.Role, el.Name} {
		if w := firstPhrase(words(s), captchaWords); w != "" {
			return w
		}
	}
	return ""
}

func hasCCAutocomplete(ac string) bool {
	for _, tok := range strings.Fields(strings.ToLower(ac)) {
		if strings.HasPrefix(tok, "cc-") {
			return true
		}
	}
	return false
}

// isSubmit is a button that submits a form to the site, other than the pre-approved login and
// search forms. A submit-typed element is only one when its name says so ("Submit", "Sign up"):
// shops make "Add to cart" a submit input too, and that is not a risk.
func isSubmit(el *Element, ws []string) bool {
	if firstPhrase(ws, preApprovedSubmitWords) != "" {
		return false
	}
	return firstPhrase(ws, submitWords) != ""
}

// words splits s into lower-case runs of letters and digits: "Privacy & Security" is
// ["privacy", "security"], "I'm" is ["i", "m"].
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// hasPhrase is whether phrase's words appear in ws, contiguous and in order.
func hasPhrase(ws []string, phrase string) bool {
	p := words(phrase)
	if len(p) == 0 {
		return false
	}
	for i := 0; i+len(p) <= len(ws); i++ {
		match := true
		for j := range p {
			if ws[i+j] != p[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func firstPhrase(ws []string, phrases []string) string {
	for _, p := range phrases {
		if hasPhrase(ws, p) {
			return p
		}
	}
	return ""
}

var (
	digitRun = regexp.MustCompile(`\d(?:[ -]?\d)*`)
	ssnRe    = regexp.MustCompile(`(?:^|\D)\d{3}[- ]\d{2}[- ]\d{4}(?:\D|$)`)
	ssnBare  = regexp.MustCompile(`(?:^|\D)\d{9}(?:\D|$)`)
	ibanRe   = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]){11,30}\b`)
)

// sensitiveText says what sensitive value text holds ("a card number", "a social security
// number", "an IBAN"), or "". "Typing sensitive data into a form counts as sending it."
func sensitiveText(text string, el *Element) string {
	for _, run := range digitRun.FindAllString(text, -1) {
		d := strings.NewReplacer(" ", "", "-", "").Replace(run)
		if len(d) >= 13 && len(d) <= 19 && luhn(d) {
			return "a card number"
		}
	}
	if ssnRe.MatchString(text) {
		return "a social security number"
	}
	if el != nil && ssnBare.MatchString(text) && firstPhrase(words(el.Name+" "+el.Autocomplete), ssnFieldWords) != "" {
		return "a social security number"
	}
	// The match may run on into the next words ("… 7654 32 THANKS"), so try it at each group's end.
	for _, m := range ibanRe.FindAllString(strings.ToUpper(text), -1) {
		acc := ""
		for _, g := range strings.Fields(m) {
			acc += g
			if validIBAN(acc) {
				return "an IBAN"
			}
		}
	}
	return ""
}

func luhn(digits string) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

// validIBAN is the ISO 13616 check: 15 to 34 characters, mod 97 of the rearranged number is 1.
func validIBAN(s string) bool {
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	var b strings.Builder
	for _, r := range s[4:] + s[:4] {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			fmt.Fprintf(&b, "%d", r-'A'+10)
		default:
			return false
		}
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}
