package cc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// --- time ---

// dbTime is the schema's timestamp format (docs/schema/cc.md): ISO-8601 UTC, ms, trailing Z.
func dbTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseTS(s string) time.Time {
	for _, f := range []string{"2006-01-02T15:04:05.000Z", time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// pyNaive renders a UTC time like Python's naive datetime.isoformat(): microseconds, omitted
// when zero. Every legacy CC timestamp was naive UTC (contract LEGACY-BUG 10, kept because
// mobile parses them).
func pyNaive(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}

func naiveTS(s string) any {
	if s == "" {
		return nil
	}
	return pyNaive(parseTS(s))
}

// --- ids ---

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// uuid4 returns a random RFC 4122 version-4 UUID string.
func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// --- errors ---

// detail writes FastAPI's {"detail": ...}.
func detail(w http.ResponseWriter, status int, d any) { httpx.Error(w, status, d) }

// detailCode writes {"detail": ..., "code": ...}: a FastAPI-shaped error plus a stable,
// machine-readable code the apps branch on (e.g. "pantry_disabled").
func detailCode(w http.ResponseWriter, status int, d, code string) {
	httpx.WriteJSON(w, status, map[string]any{"detail": d, "code": code})
}

// validationError is CC's custom RequestValidationError handler (doc 00 §3.5): 400, not 422,
// with flattened "loc -> loc: msg" details.
func validationError(w http.ResponseWriter, details ...string) {
	httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error":   "validation_error",
		"message": "Request validation failed. Please correct the highlighted fields.",
		"details": details,
	})
}

// --- request bodies (pydantic-shaped validation, flattened like CC's handler) ---

// body validates one JSON object, collecting "body -> field: msg" lines.
type body struct {
	m    map[string]any
	loc  string
	errs *[]string
}

// readBody reads a JSON object body. optional allows an absent/empty body (m is then empty
// and present is false). It writes the 400 and returns ok=false on a malformed body.
func readBody(w http.ResponseWriter, r *http.Request, optional bool) (b *body, present, ok bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		detail(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return nil, false, false
	}
	errs := []string{}
	b = &body{m: map[string]any{}, loc: "body", errs: &errs}
	if len(bytes.TrimSpace(raw)) == 0 {
		if optional {
			return b, false, true
		}
		validationError(w, "body: Field required")
		return nil, false, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		validationError(w, fmt.Sprintf("body -> %d: JSON decode error", dec.InputOffset()))
		return nil, false, false
	}
	if v == nil && optional {
		return b, false, true
	}
	m, isObj := v.(map[string]any)
	if !isObj {
		validationError(w, "body: Input should be a valid dictionary or object to extract fields from")
		return nil, false, false
	}
	b.m = m
	return b, true, true
}

func (b *body) fail(name, msg string) { *b.errs = append(*b.errs, b.loc+" -> "+name+": "+msg) }

// done writes the collected errors as CC's 400 and returns false, or returns true.
func (b *body) done(w http.ResponseWriter) bool {
	if len(*b.errs) > 0 {
		validationError(w, *b.errs...)
		return false
	}
	return true
}

func (b *body) has(name string) bool { _, ok := b.m[name]; return ok }

// str reads a string. Absent: "Field required" when required. null is accepted only when
// optional (ok=false).
func (b *body) str(name string, required bool) (string, bool) {
	v, present := b.m[name]
	if !present || (v == nil && !required) {
		if !present && required {
			b.fail(name, "Field required")
		}
		return "", false
	}
	s, isStr := v.(string)
	if !isStr {
		b.fail(name, "Input should be a valid string")
		return "", false
	}
	return s, true
}

// optStrPtr reads an optional nullable string: (nil, true) for null, (&s, true) for a value.
func (b *body) optStrPtr(name string) (*string, bool) {
	v, present := b.m[name]
	if !present {
		return nil, false
	}
	if v == nil {
		return nil, true
	}
	s, isStr := v.(string)
	if !isStr {
		b.fail(name, "Input should be a valid string")
		return nil, false
	}
	return &s, true
}

// boolean reads a bool in pydantic's lax mode (true/false, 0/1, "true"/"false"/"yes"/...).
func (b *body) boolean(name string) (val, ok bool) {
	v, present := b.m[name]
	if !present || v == nil {
		return false, false
	}
	if x, isBool := laxBool(v); isBool {
		return x, true
	}
	b.fail(name, "Input should be a valid boolean")
	return false, false
}

func laxBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case json.Number:
		switch x.String() {
		case "0", "0.0":
			return false, true
		case "1", "1.0":
			return true, true
		}
	case string:
		switch strings.ToLower(x) {
		case "true", "1", "yes", "on", "t", "y":
			return true, true
		case "false", "0", "no", "off", "f", "n":
			return false, true
		}
	}
	return false, false
}

// number reads an optional float.
func (b *body) number(name string) (float64, bool) {
	v, present := b.m[name]
	if !present || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f, true
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f, true
		}
	}
	b.fail(name, "Input should be a valid number")
	return 0, false
}

// integer reads an int (integral numbers and numeric strings, as pydantic's lax mode).
func (b *body) integer(name string, required bool) (int64, bool) {
	v, present := b.m[name]
	if !present || v == nil {
		if required {
			b.fail(name, "Field required")
		}
		return 0, false
	}
	switch x := v.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil && f == float64(int64(f)) {
			return int64(f), true
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n, true
		}
	}
	b.fail(name, "Input should be a valid integer")
	return 0, false
}

// object reads an optional dict.
func (b *body) object(name string, required bool) (map[string]any, bool) {
	v, present := b.m[name]
	if !present || v == nil {
		if required && !present {
			b.fail(name, "Field required")
		} else if required {
			b.fail(name, "Input should be a valid dictionary")
		}
		return nil, false
	}
	m, isObj := v.(map[string]any)
	if !isObj {
		b.fail(name, "Input should be a valid dictionary")
		return nil, false
	}
	return m, true
}

// strList reads an optional list of strings.
func (b *body) strList(name string) ([]string, bool) {
	v, present := b.m[name]
	if !present || v == nil {
		return nil, false
	}
	l, isList := v.([]any)
	if !isList {
		b.fail(name, "Input should be a valid list")
		return nil, false
	}
	out := make([]string, 0, len(l))
	for i, e := range l {
		s, isStr := e.(string)
		if !isStr {
			*b.errs = append(*b.errs, fmt.Sprintf("%s -> %s -> %d: Input should be a valid string", b.loc, name, i))
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// queryBool parses an optional bool query parameter like FastAPI. ok=false means it wrote a 400.
func queryBool(w http.ResponseWriter, r *http.Request, name string, def bool) (bool, bool) {
	v, present := r.URL.Query()[name]
	if !present || len(v) == 0 {
		return def, true
	}
	if x, isBool := laxBool(v[0]); isBool {
		return x, true
	}
	validationError(w, "query -> "+name+": Input should be a valid boolean, unable to interpret input")
	return false, false
}

// queryInt parses an optional int query parameter; ok=false means it wrote a 400.
func queryInt(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		validationError(w, "query -> "+name+": Input should be a valid integer, unable to parse string as an integer")
		return 0, false
	}
	return n, true
}

func nullStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
