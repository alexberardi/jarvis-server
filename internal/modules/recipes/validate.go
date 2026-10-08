package recipes

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Request validation. Legacy replaced FastAPI's 422 with its own body (main.py
// validation_exception_handler; docs/recipes/00-inventory.md §3 "Error shapes"):
//
//	{"error_code": "validation_error", "message": "Invalid request payload.",
//	 "details": [{"field": "body.ingredients", "message": "<pydantic msg>"}], "job_id": "<uuid4>"}
//
// field is pydantic's loc joined with dots; job_id is a fresh uuid4 per response and
// correlates with nothing (§8 item 12). A 422 raised by handler code keeps {"detail": ...}.

// fieldErr is one pydantic error.
type fieldErr struct {
	loc []any
	msg string
}

func (e fieldErr) field() any {
	parts := make([]string, 0, len(e.loc))
	for _, p := range e.loc {
		parts = append(parts, fmt.Sprint(p))
	}
	if len(parts) == 0 {
		return nil
	}
	return strings.Join(parts, ".")
}

// writeValidation writes the custom 422.
func writeValidation(w http.ResponseWriter, errs ...fieldErr) {
	details := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		details = append(details, map[string]any{"field": e.field(), "message": e.msg})
	}
	httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error_code": "validation_error",
		"message":    "Invalid request payload.",
		"details":    details,
		"job_id":     newUUID(),
	})
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// pydantic v2 messages, by error type.
const (
	msgMissing      = "Field required"
	msgString       = "Input should be a valid string"
	msgInt          = "Input should be a valid integer"
	msgIntParsing   = "Input should be a valid integer, unable to parse string as an integer"
	msgIntFraction  = "Input should be a valid integer, got a number with a fractional part"
	msgList         = "Input should be a valid list"
	msgDecimal      = "Input should be a valid decimal"
	msgDecimalType  = "Decimal input should be an integer, float, string or Decimal object"
	msgObject       = "Input should be a valid dictionary or object to extract fields from"
	msgJSON         = "JSON decode error"
	msgSourceType   = "Input should be 'manual', 'image' or 'url'"
	maxRequestBytes = 4 << 20
)

// readBody reads a JSON object body; failures are written as the custom 422.
func readBody(w http.ResponseWriter, r *http.Request) (*obj, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return nil, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		writeValidation(w, fieldErr{loc: []any{"body"}, msg: msgMissing})
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		writeValidation(w, fieldErr{loc: []any{"body", dec.InputOffset()}, msg: msgJSON})
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		writeValidation(w, fieldErr{loc: []any{"body"}, msg: msgObject})
		return nil, false
	}
	return &obj{m: m, loc: []any{"body"}, errs: &[]fieldErr{}}, true
}

// obj validates one JSON object's fields, collecting errors like pydantic. Nested objects
// share the error list.
type obj struct {
	m    map[string]any
	loc  []any
	errs *[]fieldErr
}

func (o *obj) fail(msg string, loc ...any) {
	*o.errs = append(*o.errs, fieldErr{loc: append(append([]any{}, o.loc...), loc...), msg: msg})
}

// done writes the collected errors as a 422 and returns false, or returns true if none.
func (o *obj) done(w http.ResponseWriter) bool {
	if len(*o.errs) > 0 {
		writeValidation(w, *o.errs...)
		return false
	}
	return true
}

func (o *obj) child(v any, msg string, loc ...any) (*obj, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		o.fail(msg, loc...)
		return nil, false
	}
	return &obj{m: m, loc: append(append([]any{}, o.loc...), loc...), errs: o.errs}, true
}

// has reports a present, non-null field.
func (o *obj) has(name string) bool { return o.m[name] != nil }

// str reads a required string.
func (o *obj) str(name string) string {
	v, present := o.m[name]
	if !present {
		o.fail(msgMissing, name)
		return ""
	}
	s, ok := v.(string)
	if !ok {
		o.fail(msgString, name)
	}
	return s
}

// optStr reads an optional, nullable string.
func (o *obj) optStr(name string) *string {
	v := o.m[name]
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		o.fail(msgString, name)
		return nil
	}
	return &s
}

// optInt reads an optional, nullable integer (pydantic lax mode: integral numbers, numeric
// strings and booleans are accepted).
func (o *obj) optInt(name string) *int64 {
	v := o.m[name]
	if v == nil {
		return nil
	}
	n, msg := laxInt(v)
	if msg != "" {
		o.fail(msg, name)
		return nil
	}
	return &n
}

// reqInt reads a required integer.
func (o *obj) reqInt(name string) int64 {
	if _, present := o.m[name]; !present {
		o.fail(msgMissing, name)
		return 0
	}
	if o.m[name] == nil {
		o.fail(msgInt, name)
		return 0
	}
	n := o.optInt(name)
	if n == nil {
		return 0
	}
	return *n
}

// laxInt is pydantic's lax int: it returns the error message on failure.
func laxInt(v any) (int64, string) {
	switch x := v.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return n, ""
		}
		f, err := x.Float64()
		if err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<63 {
			return int64(f), ""
		}
		return 0, msgIntFraction
	case string:
		return parseIntString(x)
	case bool:
		if x {
			return 1, ""
		}
		return 0, ""
	}
	return 0, msgInt
}

// parseIntString is pydantic's int-from-string (surrounding whitespace allowed).
func parseIntString(s string) (int64, string) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, msgIntParsing
	}
	return n, ""
}

// list reads an array field. present is false when absent or null; required reports
// "Field required" when absent; null is a list_type error unless nullable.
func (o *obj) list(name string, required, nullable bool) (l []any, present bool) {
	v, ok := o.m[name]
	switch {
	case !ok:
		if required {
			o.fail(msgMissing, name)
		}
		return nil, false
	case v == nil && nullable:
		return nil, false
	}
	l, isList := v.([]any)
	if !isList {
		o.fail(msgList, name)
		return nil, false
	}
	return l, true
}

// strList reads a list of strings.
func (o *obj) strList(name string, nullable bool) ([]string, bool) {
	l, ok := o.list(name, false, nullable)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for i, e := range l {
		s, isStr := e.(string)
		if !isStr {
			o.fail(msgString, name, i)
			continue
		}
		out = append(out, s)
	}
	return out, true
}

// optDecimal validates an optional Decimal field (the value itself is discarded: the server
// re-parses quantity_value from quantity_display).
func (o *obj) optDecimal(name string) {
	switch x := o.m[name].(type) {
	case nil, json.Number:
	case string:
		if !quantity.IsDecimal(x) {
			o.fail(msgDecimal, name)
		}
	default:
		o.fail(msgDecimalType, name)
	}
}

// pathInt parses an integer path parameter, writing the 422 for path.<name> on failure.
func pathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	n, msg := parseIntString(r.PathValue(name))
	if msg != "" {
		writeValidation(w, fieldErr{loc: []any{"path", name}, msg: msg})
		return 0, false
	}
	return n, true
}

// queryLimit parses an int query parameter with bounds (FastAPI Query(ge=lo, le=hi)).
func queryLimit(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int64) (int64, bool) {
	raw, ok := r.URL.Query()[name]
	if !ok || len(raw) == 0 {
		return def, true
	}
	n, msg := parseIntString(raw[len(raw)-1])
	switch {
	case msg != "":
	case n < lo:
		msg = fmt.Sprintf("Input should be greater than or equal to %d", lo)
	case n > hi:
		msg = fmt.Sprintf("Input should be less than or equal to %d", hi)
	}
	if msg != "" {
		writeValidation(w, fieldErr{loc: []any{"query", name}, msg: msg})
		return 0, false
	}
	return n, true
}
