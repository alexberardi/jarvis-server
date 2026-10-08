package recipes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// More request validation (dates, bounded strings, literals, int lists, query parameters),
// with pydantic v2's messages. See validate.go for the 422 shape.

const (
	msgDate     = "Input should be a valid date or datetime, input is too short"
	msgBool     = "Input should be a valid boolean"
	msgBoolStr  = "Input should be a valid boolean, unable to interpret input"
	msgStrShort = "String should have at least %d character"
	msgStrLong  = "String should have at most %d characters"
)

// parseDate is pydantic's lax date from a string: YYYY-MM-DD (a datetime with a zero time is
// accepted too). It returns the canonical YYYY-MM-DD.
func parseDate(s string) (string, bool) {
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 {
				return "", false
			}
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// date reads a required date field.
func (o *obj) date(name string) string {
	v, present := o.m[name]
	if !present {
		o.fail(msgMissing, name)
		return ""
	}
	s, ok := v.(string)
	if !ok {
		o.fail(msgDate, name)
		return ""
	}
	d, ok := parseDate(s)
	if !ok {
		o.fail(msgDate, name)
	}
	return d
}

// boundedStr reads a required string of lo..hi characters (pydantic min/max_length).
func (o *obj) boundedStr(name string, lo, hi int) string {
	s := o.str(name)
	if _, isStr := o.m[name].(string); isStr {
		o.checkLen(s, lo, hi, name)
	}
	return s
}

// optBoundedStr reads an optional, nullable string of at most hi characters.
func (o *obj) optBoundedStr(name string, hi int) *string {
	s := o.optStr(name)
	if s != nil {
		o.checkLen(*s, 0, hi, name)
	}
	return s
}

func (o *obj) checkLen(s string, lo, hi int, loc ...any) {
	n := utf8.RuneCountInString(s)
	switch {
	case n < lo:
		msg := fmt.Sprintf(msgStrShort, lo)
		if lo != 1 {
			msg += "s"
		}
		o.fail(msg, loc...)
	case n > hi:
		o.fail(fmt.Sprintf(msgStrLong, hi), loc...)
	}
}

// optBool reads an optional bool (pydantic lax: true/false, 0/1 and the usual strings).
func (o *obj) optBool(name string, def bool) bool {
	switch x := o.m[name].(type) {
	case nil:
		if _, present := o.m[name]; present {
			o.fail(msgBool, name)
		}
		return def
	case bool:
		return x
	case json.Number:
		switch x.String() {
		case "0":
			return false
		case "1":
			return true
		}
		o.fail(msgBool, name)
	case string:
		switch strings.ToLower(x) {
		case "0", "off", "f", "false", "n", "no":
			return false
		case "1", "on", "t", "true", "y", "yes":
			return true
		}
		o.fail(msgBoolStr, name)
	default:
		o.fail(msgBool, name)
	}
	return def
}

// literal reads an optional Literal field with a default; null is an error, as in pydantic.
func (o *obj) literal(name, def string, allowed ...string) string {
	v, present := o.m[name]
	if !present {
		return def
	}
	if s, ok := v.(string); ok && slices.Contains(allowed, s) {
		return s
	}
	o.fail(literalMsg(allowed), name)
	return def
}

// literalMsg is pydantic's literal_error text: "Input should be 'a', 'b' or 'c'".
func literalMsg(allowed []string) string {
	q := make([]string, len(allowed))
	for i, a := range allowed {
		q[i] = "'" + a + "'"
	}
	if len(q) == 1 {
		return "Input should be " + q[0]
	}
	return "Input should be " + strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}

// intList reads an optional list of lax ints (absent: empty).
func (o *obj) intList(name string) []int64 {
	l, ok := o.list(name, false, false)
	if !ok {
		return []int64{}
	}
	out := make([]int64, 0, len(l))
	for i, e := range l {
		if e == nil {
			o.fail(msgInt, name, i)
			continue
		}
		n, msg := laxInt(e)
		if msg != "" {
			o.fail(msg, name, i)
			continue
		}
		out = append(out, n)
	}
	return out
}

// queryVals validates query parameters like FastAPI, collecting every error.
type queryVals struct {
	q    url.Values
	errs []fieldErr
}

func newQueryVals(r *http.Request) *queryVals { return &queryVals{q: r.URL.Query()} }

func (v *queryVals) fail(name, msg string) {
	v.errs = append(v.errs, fieldErr{loc: []any{"query", name}, msg: msg})
}

// date reads a required date parameter (the last value wins, as in Starlette).
func (v *queryVals) date(name string) string {
	vals, ok := v.q[name]
	if !ok || len(vals) == 0 {
		v.fail(name, msgMissing)
		return ""
	}
	d, ok := parseDate(vals[len(vals)-1])
	if !ok {
		v.fail(name, msgDate)
	}
	return d
}

// literal reads an optional Literal parameter with a default.
func (v *queryVals) literal(name, def string, allowed ...string) string {
	vals, ok := v.q[name]
	if !ok || len(vals) == 0 {
		return def
	}
	s := vals[len(vals)-1]
	if !slices.Contains(allowed, s) {
		v.fail(name, literalMsg(allowed))
		return def
	}
	return s
}

func (v *queryVals) done(w http.ResponseWriter) bool {
	if len(v.errs) > 0 {
		writeValidation(w, v.errs...)
		return false
	}
	return true
}
