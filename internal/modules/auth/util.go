package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// --- time ---

// now is the module clock; tests replace it.
var now = func() time.Time { return time.Now().UTC() }

// dbTime renders a timestamp for storage: ISO-8601 UTC with microseconds.
func dbTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

// parseTime reads a stored timestamp (ours, SQLite's default, or an imported one).
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// pyTime renders a stored timestamp the way pydantic renders an aware UTC datetime:
// "2026-10-06T22:12:31.921459Z", with no fraction when it is zero.
func pyTime(s string) any {
	t, ok := parseTime(s)
	if !ok {
		return s
	}
	return pyTimeOf(t)
}

func pyTimeOf(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.000000Z")
}

func pyTimeNull(ns sql.NullString) any {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return pyTime(ns.String)
}

func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

// --- secrets ---

// tokenURLSafe matches Python's secrets.token_urlsafe(n): n random bytes, unpadded base64url.
func tokenURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// randomFrom picks n characters uniformly from alphabet (secrets.choice).
func randomFrom(alphabet string, n int) string {
	var sb strings.Builder
	max := big.NewInt(int64(len(alphabet)))
	for range n {
		i, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		sb.WriteByte(alphabet[i.Int64()])
	}
	return sb.String()
}

// sha256Hex is the refresh-token hash (hashlib.sha256(token).hexdigest()).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// bcryptCost matches passlib's bcrypt default (12 rounds). Tests lower it.
var bcryptCost = 12

// bcryptMax is bcrypt's input limit. passlib (with bcrypt 4.x) silently truncates longer
// secrets, so legacy hashes of long passwords were made from the first 72 bytes; Go's bcrypt
// refuses them instead, so truncate on both sides for parity.
const bcryptMax = 72

func truncate72(s string) []byte {
	b := []byte(s)
	if len(b) > bcryptMax {
		b = b[:bcryptMax]
	}
	return b
}

// hashSecret hashes a password, node key or app key ($2a$ from Go; passlib's $2b$ verify too).
func hashSecret(secret string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(truncate72(secret), bcryptCost)
	return string(h), err
}

// verifySecret checks a secret against a bcrypt hash. Comparison is constant-time.
func verifySecret(secret, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), truncate72(secret)) == nil
}

// dummyHash equalizes login timing for unknown emails (security.DUMMY_PASSWORD_HASH). Lazy,
// so it is made at the configured cost (tests lower bcryptCost first).
var dummyHash = sync.OnceValue(func() string {
	h, _ := hashSecret(tokenURLSafe(32))
	return h
})

// --- errors ---

func detail(w http.ResponseWriter, status int, msg string) { httpx.Error(w, status, msg) }

// --- request bodies: pydantic-shaped validation ---

// body is a decoded JSON object body with accumulated pydantic-style field errors.
type body struct {
	m    map[string]any
	errs []httpx.FieldError
}

// readBody decodes a JSON object body. optional=true accepts an empty body (an omitted
// `payload: Model | None = None`). On failure it writes FastAPI's 422 and returns false.
func readBody(w http.ResponseWriter, r *http.Request, optional bool) (*body, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return nil, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if optional {
			return &body{m: map[string]any{}}, true
		}
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body"}, Msg: "Field required"})
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		var se *json.SyntaxError
		pos := int64(0)
		if errors.As(err, &se) {
			pos = se.Offset
		}
		httpx.ValidationError(w, httpx.FieldError{Type: "json_invalid", Loc: []any{"body", pos},
			Msg: "JSON decode error", Input: map[string]any{}})
		return nil, false
	}
	if v == nil && optional {
		return &body{m: map[string]any{}}, true
	}
	m, ok := v.(map[string]any)
	if !ok {
		httpx.ValidationError(w, httpx.FieldError{Type: "model_attributes_type", Loc: []any{"body"},
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: v})
		return nil, false
	}
	return &body{m: m}, true
}

func (b *body) fail(name, typ, msg string, input any) {
	b.errs = append(b.errs, httpx.FieldError{Type: typ, Loc: []any{"body", name}, Msg: msg, Input: input})
}

// done writes the 422 if any field failed; it returns true when the body is valid.
func (b *body) done(w http.ResponseWriter) bool {
	if len(b.errs) > 0 {
		httpx.ValidationError(w, b.errs...)
		return false
	}
	return true
}

func (b *body) missing(name string) {
	b.fail(name, "missing", "Field required", b.m)
}

// str reads a string field. required=false returns ("", false) when absent; null is an
// error either way (the field is `str`, not `str | None`). min/max are character counts
// (0 = unbounded).
func (b *body) str(name string, required bool, min, max int) (string, bool) {
	v, ok := b.m[name]
	if !ok {
		if required {
			b.missing(name)
		}
		return "", false
	}
	s, isStr := v.(string)
	if !isStr {
		b.fail(name, "string_type", "Input should be a valid string", v)
		return "", false
	}
	n := len([]rune(s))
	if min > 0 && n < min {
		b.fail(name, "string_too_short", fmt.Sprintf("String should have at least %d characters", min), s)
		return "", false
	}
	if max > 0 && n > max {
		b.fail(name, "string_too_long", fmt.Sprintf("String should have at most %d characters", max), s)
		return "", false
	}
	return s, true
}

// optStr reads `str | None = None`: absent or null is (nil).
func (b *body) optStr(name string, min, max int) *string {
	if v, ok := b.m[name]; !ok || v == nil {
		return nil
	}
	s, ok := b.str(name, false, min, max)
	if !ok {
		return nil
	}
	return &s
}

// email reads an EmailStr field and normalizes it as email-validator does (domain lowercased).
func (b *body) email(name string) (string, bool) {
	s, ok := b.str(name, true, 0, 0)
	if !ok {
		return "", false
	}
	norm, reason := normalizeEmail(s)
	if reason != "" {
		b.fail(name, "value_error", "value is not a valid email address: "+reason, s)
		return "", false
	}
	return norm, true
}

// specialUseDomains are rejected by email-validator (RFC 6761 et al.).
var specialUseDomains = []string{"arpa", "invalid", "local", "localhost", "onion", "test"}

func normalizeEmail(s string) (string, string) {
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return "", "An email address must have an @-sign."
	}
	local, domain := s[:at], s[at+1:]
	if local == "" {
		return "", "There must be something before the @-sign."
	}
	if domain == "" {
		return "", "There must be something after the @-sign."
	}
	if strings.ContainsAny(s, " \t\r\n") || strings.Contains(local, "@") {
		return "", "The email address contains invalid characters."
	}
	domain = strings.ToLower(domain)
	if !strings.Contains(domain, ".") {
		return "", "The part after the @-sign is not valid. It should have a period."
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return "", "An email address cannot have a period immediately after the @-sign."
	}
	for _, d := range specialUseDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return "", "The part after the @-sign is a special-use or reserved name that cannot be used with email."
		}
	}
	return local + "@" + domain, ""
}

// integer reads an int field (pydantic lax mode: integral numbers and numeric strings).
// present=false when absent; required adds the missing error.
func (b *body) integer(name string, required bool) (int64, bool) {
	v, ok := b.m[name]
	if !ok {
		if required {
			b.missing(name)
		}
		return 0, false
	}
	n, ok := laxInt(v)
	if !ok {
		b.fail(name, "int_type", "Input should be a valid integer", v)
		return 0, false
	}
	return n, true
}

// optInt reads `int | None`: absent or null is nil.
func (b *body) optInt(name string) (*int64, bool) {
	if v, ok := b.m[name]; !ok || v == nil {
		return nil, true
	}
	n, ok := b.integer(name, false)
	if !ok {
		return nil, false
	}
	return &n, true
}

func laxInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil && f == float64(int64(f)) {
			return int64(f), true
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n, true
		}
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// boolean reads a bool field with a default (pydantic lax: "true"/"1"/"yes"/... accepted).
func (b *body) boolean(name string, def bool, required bool) bool {
	v, ok := b.m[name]
	if !ok {
		if required {
			b.missing(name)
		}
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case json.Number:
		switch x.String() {
		case "0":
			return false
		case "1":
			return true
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "on", "t", "y":
			return true
		case "false", "0", "no", "off", "f", "n":
			return false
		}
	}
	b.fail(name, "bool_type", "Input should be a valid boolean", v)
	return def
}

// role reads a HouseholdRole enum field.
func (b *body) role(name string, def role, required bool) role {
	v, ok := b.m[name]
	if !ok {
		if required {
			b.missing(name)
		}
		return def
	}
	s, _ := v.(string)
	if r := role(s); r.valid() {
		return r
	}
	b.fail(name, "enum", "Input should be 'member', 'power_user' or 'admin'", v)
	return def
}

// strList reads `list[str] | None = None`.
func (b *body) strList(name string) []string {
	v, ok := b.m[name]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		b.fail(name, "list_type", "Input should be a valid list", v)
		return nil
	}
	out := make([]string, 0, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			b.errs = append(b.errs, httpx.FieldError{Type: "string_type", Loc: []any{"body", name, i},
				Msg: "Input should be a valid string", Input: e})
			continue
		}
		out = append(out, s)
	}
	return out
}

// pathInt parses an int path parameter, writing FastAPI's 422 on failure.
func pathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	v := r.PathValue(name)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		httpx.ValidationError(w, httpx.FieldError{Type: "int_parsing", Loc: []any{"path", name},
			Msg: "Input should be a valid integer, unable to parse string as an integer", Input: v})
		return 0, false
	}
	return n, true
}
