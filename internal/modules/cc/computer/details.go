package computer

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// Saved details fill forms (docs/agent/COMPUTER-CONTROL.md C13): the model is told which of
// the user's details exist and types a placeholder ("{{email}}"); the value goes in only as
// the action is sent to the computer, so it is in no prompt, history entry or model log.

// Detail is one saved detail a browser task may type by placeholder.
type Detail struct{ Key, Label, Value string }

var detailRe = regexp.MustCompile(`\{\{([a-z0-9_]+)\}\}`)

// autoKeys is the detail an input's autocomplete token asks for.
var autoKeys = map[string]string{
	"name": "full_name", "given-name": "given_name", "family-name": "family_name", "email": "email",
	"tel": "callback_number", "tel-national": "callback_number",
	"street-address": "street", "address-line1": "street", "address-level2": "city", "address-level1": "state",
	"postal-code": "postal_code",
}

// detailKey reports whether key names a saved detail at all, saved or not. autoKeys' values are
// every key cc offers (formDetails in computer_errands.go, and the name split in two).
func detailKey(key string) bool {
	for _, k := range autoKeys {
		if k == key {
			return true
		}
	}
	return false
}

func detail(ds []Detail, key string) (Detail, bool) {
	for _, d := range ds {
		if d.Key == key {
			return d, true
		}
	}
	return Detail{}, false
}

// fillDetails puts the values in for a text's placeholders; an unknown one stays as written.
func fillDetails(text string, ds []Detail) string {
	if len(ds) == 0 || !strings.Contains(text, "{{") {
		return text
	}
	return detailRe.ReplaceAllStringFunc(text, func(p string) string {
		if d, ok := detail(ds, p[2:len(p)-2]); ok {
			return d.Value
		}
		return p
	})
}

// maskMin is the shortest value masked: "PA" would rewrite half the page.
const maskMin = 4

// maskText puts the placeholders back for the values a page shows once they are typed.
func maskText(text string, ds []Detail) string {
	for _, d := range ds {
		if len(d.Value) >= maskMin {
			text = strings.ReplaceAll(text, d.Value, "{{"+d.Key+"}}")
		}
	}
	return text
}

// maskDetails is maskText over everything a model call says.
func maskDetails(msgs []llm.Message, ds []Detail) {
	for _, m := range msgs {
		if m.Content == nil {
			continue
		}
		if m.Content.Text != nil {
			*m.Content.Text = maskText(*m.Content.Text, ds)
		}
		for i, p := range m.Content.Parts {
			if text := maskText(p.Text, ds); text != p.Text {
				m.Content.Parts[i].Text, m.Content.Parts[i].CachePrefix = text, 0
			}
		}
	}
}

// placeDetails checks what a batch types into the user's name, address, phone and email fields.
// A placeholder for a saved detail passes, as does text said holds (the request, what was
// found, the user's answer). Anything else is the model's invention: it becomes the saved
// detail the field's autocomplete token names, or the question for the user when there is none.
// shortcut: only the newest answer counts, so a detail from an earlier answer is asked for again.
func placeDetails(said string, ds []Detail, obs Observation, actions []computeruse.Action) ([]computeruse.Action, string) {
	known := map[string]bool{}
	lowSaid := strings.ToLower(said)
	for _, w := range wordRe.FindAllString(lowSaid, -1) {
		known[w] = true
	}
	for abbr, state := range usStates { // "Burlington, VT" for "Burlington, Vermont" (weather.gov, 2026-11-25)
		if strings.Contains(lowSaid, state) {
			known[abbr] = true
		}
	}
	out := make([]computeruse.Action, 0, len(actions))
	for _, a := range actions {
		if a.Kind != "type" && a.Kind != "set_value" {
			out = append(out, a)
			continue
		}
		e := elementFor(obs, a.Ref)
		// Text that is only a placeholder for no detail at all types nothing: the field is left
		// empty, and a form that needs it says so on the next page. Gemma 4 26B typed a field's
		// ref, "{{e7}}", into an optional Suite/Apt and the task stopped to ask (dominos.com,
		// 2026-11-25). A real detail that isn't offered here (unsaved, or off the task's site) still asks.
		if m := detailRe.FindStringSubmatch(strings.TrimSpace(a.Text)); m != nil && m[0] == strings.TrimSpace(a.Text) && !detailKey(m[1]) {
			continue
		}
		out = append(out, a)
		i := len(out) - 1
		for _, m := range detailRe.FindAllStringSubmatch(a.Text, -1) {
			if _, ok := detail(ds, m[1]); !ok {
				name := m[1]
				if e != nil && e.Name != "" {
					name = e.Name
				}
				return nil, fmt.Sprintf("I can't fill in %q from your saved details here. What should I enter?", name)
			}
		}
		if e == nil {
			continue
		}
		what := personalField(e)
		if what == "" || searchRe.MatchString(e.Name) {
			continue
		}
		for _, w := range wordRe.FindAllString(strings.ToLower(detailRe.ReplaceAllString(a.Text, " ")), -1) {
			if known[w] {
				continue
			}
			key := ""
			for _, tok := range strings.Fields(strings.ToLower(e.Autocomplete)) {
				if _, ok := detail(ds, autoKeys[tok]); ok {
					key = autoKeys[tok]
				}
			}
			if key == "" {
				return nil, fmt.Sprintf("The page asks for your %s (%q) and the request doesn't give it. What should I enter?", what, e.Name)
			}
			out[i].Text = "{{" + key + "}}"
			break
		}
	}
	return out, ""
}

// searchRe names a field that looks a place up ("Local forecast by City, St or ZIP code"): what
// goes in is the task's place, not the user's address.
var searchRe = regexp.MustCompile(`(?i)\b(search|find|forecast|locations?|near)\b`)

// usStates maps each abbreviation to its state's name, lower case.
var usStates = map[string]string{
	"al": "alabama", "ak": "alaska", "az": "arizona", "ar": "arkansas", "ca": "california", "co": "colorado",
	"ct": "connecticut", "de": "delaware", "fl": "florida", "ga": "georgia", "hi": "hawaii", "id": "idaho",
	"il": "illinois", "in": "indiana", "ia": "iowa", "ks": "kansas", "ky": "kentucky", "la": "louisiana",
	"me": "maine", "md": "maryland", "ma": "massachusetts", "mi": "michigan", "mn": "minnesota",
	"ms": "mississippi", "mo": "missouri", "mt": "montana", "ne": "nebraska", "nv": "nevada",
	"nh": "new hampshire", "nj": "new jersey", "nm": "new mexico", "ny": "new york", "nc": "north carolina",
	"nd": "north dakota", "oh": "ohio", "ok": "oklahoma", "or": "oregon", "pa": "pennsylvania",
	"ri": "rhode island", "sc": "south carolina", "sd": "south dakota", "tn": "tennessee", "tx": "texas",
	"ut": "utah", "vt": "vermont", "va": "virginia", "wa": "washington", "wv": "west virginia",
	"wi": "wisconsin", "wy": "wyoming", "dc": "district of columbia",
}

// detailsLine tells the model which saved details it may type, by name only.
func detailsLine(ds []Detail) string {
	if len(ds) == 0 {
		return ""
	}
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = "{{" + d.Key + "}} " + d.Label
	}
	return "The user's saved details: " + strings.Join(names, ", ") + ". To fill a field with one, type its placeholder exactly as written, " +
		`e.g. {"kind": "type", "ref": "e7", "name": "Email", "text": "{{email}}", "intent": "Fill in the email"}: Jarvis puts the value in, and you never see it. ` +
		"A field that shows a placeholder is already filled. Anything else the form needs, ask the user for.\n"
}

// onSite reports whether the page at pageURL is on the task's site (or a subdomain of it).
func onSite(site, pageURL string) bool {
	if i := strings.Index(site, "://"); i >= 0 {
		site = site[i+3:]
	}
	site = strings.TrimPrefix(strings.ToLower(siteHost(site)), "www.")
	u, err := url.Parse(pageURL)
	if err != nil || site == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == site || strings.HasSuffix(host, "."+site)
}
