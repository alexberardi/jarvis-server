package cc

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Household settings from mobile (docs/cc/13 §3.7, api/mobile_household_settings.py): a
// household admin flips an allowlisted set of household-scoped settings; any member reads them.
// The allowlist is the security boundary: this route is never a household-admin write to any
// other CC setting. D40 Q6 keeps all 12 legacy keys; D19 adds memory.enabled and
// memory.extraction_enabled so a household can turn learning off.

const settingWebScrapingExternal = "web_scraping.allow_external"

type householdSetting struct {
	key, typ string // typ: "bool" | "int" | "string"
}

// householdControllable is HOUSEHOLD_CONTROLLABLE_SETTINGS, in its legacy order (the GET
// response's key order).
var householdControllable = []householdSetting{
	{settingWebSearch, "bool"},
	{settingWebScrapingExternal, "bool"},
	{settingProposalsEnabled, "bool"},
	{phone.SettingEnabled, "bool"},
	{phone.SettingPlanTTL, "int"},
	{phone.SettingRetentionDays, "int"},
	{phone.SettingMaxCallSeconds, "int"},
	{phone.SettingCallsPerDay, "int"},
	{phone.SettingMonthlyMinutes, "int"},
	{phone.SettingMaxConcurrent, "int"},
	{settingHouseholdLocation, "string"},
	{settingPersona, "string"},
	{settingMemoryEnabled, "bool"},     // D19
	{settingExtractionEnabled, "bool"}, // D19
}

func householdSettingType(key string) (string, bool) {
	for _, s := range householdControllable {
		if s.key == key {
			return s.typ, true
		}
	}
	return "", false
}

// householdSettingDefinitions declares the allowlisted keys nothing else declares yet.
func householdSettingDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingWebScrapingExternal, Category: "web_search", Type: settings.Bool, Default: false,
			Description: "Permit the deep_research scraper to fall back to the third-party r.jina.ai reader proxy when " +
				"a page can't be fetched directly. This leaks which pages the household reads to a third party. " +
				"Default OFF; shares the web_search mobile screen."},
	}
}

func (m *Module) registerHouseholdSettings(mux *http.ServeMux) {
	const base = "/api/v0/mobile/household/{household_id}"
	mux.HandleFunc("GET "+base+"/settings", m.user(m.handleGetHouseholdSettings))
	mux.HandleFunc("PUT "+base+"/settings/{key...}", m.user(m.handlePutHouseholdSetting))
	mux.HandleFunc("GET "+base+"/persona/presets", m.user(m.handlePersonaPresets))
}

func (m *Module) handleGetHouseholdSettings(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	values := pyjson.NewObject()
	for _, s := range householdControllable {
		v, err := m.settings.Get(ctx, s.key, settings.Scope{HouseholdID: hh})
		if err != nil {
			m.internalError(w, err)
			return
		}
		c, ok := coerceHouseholdValue(toPyValue(v.Value), s.typ)
		if !ok {
			// An uncoercible stored value reads as null rather than failing the whole screen.
			m.deps.Log.Warn("cc: household setting has an uncoercible value", "household", hh, "key", s.key)
			c = nil
		}
		values.Set(s.key, c)
	}
	out := pyjson.NewObject()
	out.Set("household_id", hh)
	out.Set("settings", values)
	writePy(w, http.StatusOK, out)
}

func (m *Module) handlePutHouseholdSetting(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, key := r.PathValue("household_id"), r.PathValue("key")
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		detail(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	if !b.has("value") {
		b.fail("value", "Field required")
	}
	if !b.done(w) {
		return
	}
	pv, _ := pyjson.Loads(string(raw))
	obj, _ := pv.(*pyjson.Object)
	var value any
	if obj != nil {
		value, _ = obj.Get("value")
	}

	// The allowlist first (404 leaks nothing to a non-admin), then the role.
	typ, ok := householdSettingType(key)
	if !ok {
		detail(w, http.StatusNotFound, "Setting is not household-controllable: "+key)
		return
	}
	ctx := r.Context()
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleAdmin); err != nil {
		m.writeErr(w, err)
		return
	}
	coerced, cerr := coerceHouseholdWrite(value, typ)
	if cerr != "" {
		detail(w, http.StatusBadRequest, fmt.Sprintf("Invalid value for %s: %s", key, cerr))
		return
	}
	if key == settingPersona {
		if n := utf8.RuneCountInString(pyjson.Str(coerced)); n > prompts.PersonaMaxChars {
			detail(w, http.StatusBadRequest, fmt.Sprintf("Persona is too long (%d chars); max is %d.", n, prompts.PersonaMaxChars))
			return
		}
	}
	if err := m.settings.Set(ctx, key, storeValue(coerced), settings.Scope{HouseholdID: hh}); err != nil {
		m.deps.Log.Error("cc: household setting write failed", "key", key, "err", err)
		detail(w, http.StatusInternalServerError, "Failed to update "+key)
		return
	}
	m.deps.Log.Info("cc: household setting changed", "household", hh, "key", key, "by", u.ID)
	out := pyjson.NewObject()
	out.Set("success", true)
	out.Set("key", key)
	out.Set("value", coerced)
	writePy(w, http.StatusOK, out)
}

func (m *Module) handlePersonaPresets(w http.ResponseWriter, r *http.Request, u authn.User) {
	if err := m.requireRole(r.Context(), u.ID, r.PathValue("household_id"), authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	presets := make([]any, len(prompts.PersonaPresets))
	for i, p := range prompts.PersonaPresets {
		o := pyjson.NewObject()
		o.Set("id", p.ID)
		o.Set("label", p.Label)
		o.Set("text", p.Text)
		presets[i] = o
	}
	out := pyjson.NewObject()
	out.Set("presets", presets)
	out.Set("default_preset_id", prompts.DefaultPersonaPresetID)
	out.Set("default_text", prompts.DefaultPersona)
	out.Set("max_chars", int64(prompts.PersonaMaxChars))
	writePy(w, http.StatusOK, out)
}

// writePy writes a pyjson value (dict key order kept, as FastAPI's JSONResponse).
func writePy(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, pyjson.Compact(v))
}

// toPyValue maps a settings value (int64, float64, bool, string, decoded JSON) to pyjson's types.
func toPyValue(v any) any {
	switch x := v.(type) {
	case int64:
		return big.NewInt(x)
	case int:
		return big.NewInt(int64(x))
	case nil, bool, string, float64, *big.Int, *pyjson.Object, []any:
		return x
	}
	return toPy(v)
}

// coerceHouseholdValue is _coerce for a read: ok=false where the write path would raise.
func coerceHouseholdValue(v any, typ string) (any, bool) {
	c, err := coerceHouseholdWrite(v, typ)
	return c, err == ""
}

// coerceHouseholdWrite is _coerce: bool never fails; int rejects bools and non-integral floats
// and parses strings; the error text is Python's exception message.
func coerceHouseholdWrite(v any, typ string) (any, string) {
	switch typ {
	case "bool":
		switch x := v.(type) {
		case bool:
			return x, ""
		case string:
			l := strings.ToLower(x)
			return l == "true" || l == "1" || l == "yes", ""
		}
		return pyTruthyValue(v), ""
	case "int":
		switch x := v.(type) {
		case bool:
			return nil, "expected an integer, got boolean " + pyjson.Repr(x)
		case *big.Int:
			return x, ""
		case float64:
			if !math.IsInf(x, 0) && !math.IsNaN(x) && x == math.Trunc(x) {
				n, _ := new(big.Float).SetFloat64(x).Int(nil)
				return n, ""
			}
			return nil, "expected an integer, got " + pyjson.Repr(x)
		case string:
			s := strings.TrimSpace(x)
			n, ok := pyInt(s)
			if !ok {
				return nil, "invalid literal for int() with base 10: " + pyjson.Repr(s)
			}
			return n, ""
		}
		return nil, "expected an integer, got " + pyjson.TypeName(v)
	}
	return v, ""
}

// pyInt is int(str) for base 10: an optional sign, digits with single underscores between them.
func pyInt(s string) (*big.Int, bool) {
	t := s
	if strings.HasPrefix(t, "+") || strings.HasPrefix(t, "-") {
		t = t[1:]
	}
	if t == "" || strings.HasPrefix(t, "_") || strings.HasSuffix(t, "_") || strings.Contains(t, "__") {
		return nil, false
	}
	for _, c := range t {
		if c != '_' && (c < '0' || c > '9') {
			return nil, false
		}
	}
	n, ok := new(big.Int).SetString(strings.ReplaceAll(s, "_", ""), 10)
	return n, ok
}

// storeValue hands a coerced value to the settings service the way the Python client stored it.
func storeValue(v any) any {
	switch x := v.(type) {
	case nil, bool, string:
		return x
	case *big.Int:
		if x.IsInt64() {
			return x.Int64()
		}
		return x.String()
	}
	return pyjson.Str(v) // str(value) for a non-string written to a string setting
}
