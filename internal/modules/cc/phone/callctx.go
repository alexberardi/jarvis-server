package phone

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Per-user call context (services/call_context.py): details the call agent may use, stored as
// one JSON string in the user-scoped, secret phone_calls.call_context setting. CATEGORY says
// whether a field is loaded (today: everything is, select_for_call); TIER says whether the
// agent may volunteer it. This is PII.

// Categories and tiers.
const (
	CatGeneral   = "general"
	CatMedical   = "medical"
	CatAuto      = "auto"
	CatHome      = "home"
	CatFinancial = "financial"
	CatDining    = "dining"

	TierState   = "state"
	TierIfAsked = "if_asked"
)

var (
	categories     = []string{CatGeneral, CatMedical, CatAuto, CatHome, CatFinancial, CatDining}
	categoryLabels = map[string]string{
		CatGeneral: "General", CatMedical: "Medical", CatAuto: "Auto", CatHome: "Home & trades",
		CatFinancial: "Financial", CatDining: "Dining & reservations",
	}
)

type fieldSpec struct{ key, label, category, tier string }

// wellKnownFields are the fixed-label fields (WELL_KNOWN_FIELDS).
var wellKnownFields = []fieldSpec{
	{"full_name", "Full name", CatGeneral, TierState},
	{"address", "Address", CatGeneral, TierIfAsked},
	{"callback_number", "Callback number", CatGeneral, TierIfAsked},
	{"date_of_birth", "Date of birth", CatMedical, TierIfAsked},
	{"insurance_provider", "Insurance provider", CatMedical, TierIfAsked},
	{"insurance_member_id", "Insurance member ID", CatMedical, TierIfAsked},
	{"pharmacy", "Preferred pharmacy", CatMedical, TierState},
	{"vehicle", "Vehicle", CatAuto, TierState},
	{"license_plate", "License plate", CatAuto, TierIfAsked},
}

func wellKnown(key string) (fieldSpec, bool) {
	for _, f := range wellKnownFields {
		if f.key == key {
			return f, true
		}
	}
	return fieldSpec{}, false
}

func isCategory(c string) bool {
	for _, x := range categories {
		if x == c {
			return true
		}
	}
	return false
}

// ContextField is one resolved field.
type ContextField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Category string `json:"category"`
	Tier     string `json:"tier"`
}

// pyStr is Python's str(raw.get(k) or ""): falsy values become "", others their str().
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if !x {
			return ""
		}
		return "True"
	case float64:
		if x == 0 {
			return ""
		}
		b, _ := json.Marshal(x)
		return string(b)
	case []any:
		if len(x) == 0 {
			return ""
		}
	case map[string]any:
		if len(x) == 0 {
			return ""
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// coerceEntry is _coerce_entry: forgiving, so one malformed row never costs the others.
func coerceEntry(raw any) (ContextField, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return ContextField{}, false
	}
	key := strings.TrimSpace(pyStr(m["key"]))
	value := strings.TrimSpace(pyStr(m["value"]))
	if key == "" || value == "" {
		return ContextField{}, false
	}
	known, isKnown := wellKnown(key)
	label := strings.TrimSpace(pyStr(m["label"]))
	if label == "" {
		label = key
		if isKnown {
			label = known.label
		}
	}
	cat := strings.ToLower(strings.TrimSpace(pyStr(m["category"])))
	if !isCategory(cat) {
		cat = CatGeneral
		if isKnown {
			cat = known.category
		}
	}
	tier := strings.ToLower(strings.TrimSpace(pyStr(m["tier"])))
	if tier != TierState && tier != TierIfAsked {
		// Unknown tier falls to the private side: a custom field is more likely an account
		// number than a pleasantry.
		tier = TierIfAsked
		if isKnown {
			tier = known.tier
		}
	}
	return ContextField{Key: key, Label: label, Value: value, Category: cat, Tier: tier}, true
}

// ParseCallContext parses the stored blob (a JSON string, or decoded JSON), dropping
// anything unusable; duplicate keys keep the first.
func ParseCallContext(raw any) []ContextField {
	if s, ok := raw.(string); ok {
		if s == "" {
			return nil
		}
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil
		}
		raw = v
	}
	if raw == nil {
		return nil
	}
	entries := raw
	if m, ok := raw.(map[string]any); ok {
		entries = m["fields"]
	}
	list, ok := entries.([]any)
	if !ok {
		return nil
	}
	var out []ContextField
	seen := map[string]bool{}
	for _, e := range list {
		f, ok := coerceEntry(e)
		if !ok || seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		out = append(out, f)
	}
	return out
}

// SelectForCall is what a call loads today: every field regardless of category (a deliberate
// decision, 2026-07-23; per-call category selection is future work).
func SelectForCall(fields []ContextField) []ContextField {
	return append([]ContextField(nil), fields...)
}

// RestrictedFields are the give-if-asked fields: the spoken-output guard's denylist.
func RestrictedFields(fields []ContextField) []ContextField {
	var out []ContextField
	for _, f := range fields {
		if f.Tier == TierIfAsked && f.Value != "" {
			out = append(out, f)
		}
	}
	return out
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(label string) string {
	return strings.Trim(slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(label)), "_"), "_")
}

// PrepareForStorage turns grid rows into canonical fields: a keyless row gets a key slugged
// from its label, then the read path's coercion and first-wins dedup apply.
func PrepareForStorage(rows []any) []ContextField {
	prepared := make([]any, 0, len(rows))
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		c := make(map[string]any, len(m)+1)
		for k, v := range m {
			c[k] = v
		}
		if strings.TrimSpace(pyStr(c["key"])) == "" {
			c["key"] = slugify(pyStr(c["label"]))
		}
		prepared = append(prepared, c)
	}
	return ParseCallContext(map[string]any{"fields": prepared})
}

// SerializeFields is the canonical stored blob, in Python json.dumps form.
func SerializeFields(fields []ContextField) string {
	var b strings.Builder
	b.WriteString(`{"fields": [`)
	for i, f := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`{"key": ` + pyJSONStr(f.Key) + `, "label": ` + pyJSONStr(f.Label) +
			`, "value": ` + pyJSONStr(f.Value) + `, "category": ` + pyJSONStr(f.Category) +
			`, "tier": ` + pyJSONStr(f.Tier) + `}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// pyJSONStr encodes a string like Python's json.dumps (ensure_ascii).
func pyJSONStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20 || (r >= 0x7f && r < 0x10000):
				if r < 0x7f {
					b.WriteString(`\u00`)
					b.WriteString(hex2(byte(r)))
				} else {
					b.WriteString(`\u`)
					b.WriteString(hex4(uint16(r)))
				}
			case r >= 0x10000:
				r -= 0x10000
				b.WriteString(`\u` + hex4(uint16(0xD800+(r>>10))) + `\u` + hex4(uint16(0xDC00+(r&0x3FF))))
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

const hexDigits = "0123456789abcdef"

func hex2(c byte) string { return string([]byte{hexDigits[c>>4], hexDigits[c&0xF]}) }
func hex4(v uint16) string {
	return string([]byte{hexDigits[v>>12], hexDigits[(v>>8)&0xF], hexDigits[(v>>4)&0xF], hexDigits[v&0xF]})
}

// Catalog is the static vocabulary the mobile grid renders.
func Catalog() map[string]any {
	wk := make([]any, 0, len(wellKnownFields))
	for _, f := range wellKnownFields {
		wk = append(wk, orderedMap{{"key", f.key}, {"label", f.label}, {"category", f.category}, {"tier", f.tier}})
	}
	cats := make([]any, 0, len(categories))
	for _, c := range categories {
		cats = append(cats, orderedMap{{"value", c}, {"label", categoryLabels[c]}})
	}
	return map[string]any{
		"well_known": wk,
		"categories": cats,
		"tiers": []any{
			orderedMap{{"value", TierState}, {"label", "May be said freely"}},
			orderedMap{{"value", TierIfAsked}, {"label", "Only if they ask"}},
		},
	}
}

// BuildContextBlock renders the brief's caller-details section, or "" when there is nothing
// to say. full_name is always in the give-when-asked group: stated freely, the model
// introduced itself AS the caller. The wording was tuned live; keep it byte-for-byte.
func BuildContextBlock(fields []ContextField) string {
	var statable, private []ContextField
	for _, f := range fields {
		if f.Tier == TierState && f.Key != "full_name" {
			statable = append(statable, f)
		}
		if f.Tier == TierIfAsked || f.Key == "full_name" {
			private = append(private, f)
		}
	}
	if len(statable) == 0 && len(private) == 0 {
		return ""
	}
	parts := []string{"The details below belong to the person you are calling ON BEHALF OF. " +
		"You are their assistant: never claim to be them, never introduce " +
		"yourself with their name, and never ask to speak to them."}
	lines := func(fs []ContextField) string {
		ls := make([]string, len(fs))
		for i, f := range fs {
			ls[i] = "- " + f.Label + ": " + f.Value
		}
		return strings.Join(ls, "\n")
	}
	if len(statable) > 0 {
		parts = append(parts, "You may state these if it helps:\n"+lines(statable))
	}
	if len(private) > 0 {
		parts = append(parts, "If the business asks for one of these details, give it directly "+
			"and accurately — refusing one listed here fails the call. Never "+
			"volunteer them otherwise:\n"+lines(private))
	}
	return strings.Join(parts, "\n\n")
}

// orderedMap is a JSON object that keeps key order (Python dict order on the wire).
type orderedMap []struct {
	k string
	v any
}

func (o orderedMap) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(kv.k)
		v, err := json.Marshal(kv.v)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}
