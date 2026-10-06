//go:build contract

package contract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Matcher checks a decoded JSON value (from Resp.JSON: objects are map[string]any, arrays are
// []any, numbers are json.Number) and returns one message per mismatch, prefixed with its path.
//
// The point is to freeze *shapes*, not data: key sets, types, and the few literal values clients
// actually branch on. Volatile values (ids, timestamps, tokens) are matched by type only.
type Matcher interface {
	Match(path string, v any) []string
}

// MatchFunc adapts a function to Matcher.
type MatchFunc func(path string, v any) []string

func (f MatchFunc) Match(path string, v any) []string { return f(path, v) }

func mismatch(path, want string, v any) []string {
	return []string{fmt.Sprintf("%s: want %s, got %s", path, want, show(v))}
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Obj matches a JSON object with exactly these keys (keys wrapped in Optional may be absent).
// Use it for pydantic response models, whose key sets are fixed.
type Obj map[string]Matcher

func (o Obj) Match(path string, v any) []string { return matchObject(path, v, o, true) }

// Open matches a JSON object that has at least these keys; extra keys are allowed. Use it where
// the payload is intentionally open-ended (health details, provider-specific blobs).
type Open map[string]Matcher

func (o Open) Match(path string, v any) []string { return matchObject(path, v, o, false) }

func matchObject(path string, v any, fields map[string]Matcher, exact bool) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return mismatch(path, "object", v)
	}
	var errs []string
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fm := fields[k]
		val, present := m[k]
		if opt, isOpt := fm.(optional); isOpt {
			if !present {
				continue
			}
			fm = opt.m
		} else if !present {
			errs = append(errs, fmt.Sprintf("%s: missing key %q", path, k))
			continue
		}
		errs = append(errs, fm.Match(path+"."+k, val)...)
	}
	if exact {
		var extra []string
		for k := range m {
			if _, known := fields[k]; !known {
				extra = append(extra, k)
			}
		}
		if len(extra) > 0 {
			sort.Strings(extra)
			errs = append(errs, fmt.Sprintf("%s: unexpected keys %v", path, extra))
		}
	}
	return errs
}

type optional struct{ m Matcher }

func (o optional) Match(path string, v any) []string { return o.m.Match(path, v) }

// Optional marks an object key that may be absent. When present, it must match m.
func Optional(m Matcher) Matcher { return optional{m} }

// Any matches anything, including null.
var Any Matcher = MatchFunc(func(string, any) []string { return nil })

// Null matches JSON null.
var Null Matcher = MatchFunc(func(path string, v any) []string {
	if v != nil {
		return mismatch(path, "null", v)
	}
	return nil
})

// String matches any JSON string.
var String Matcher = MatchFunc(func(path string, v any) []string {
	if _, ok := v.(string); !ok {
		return mismatch(path, "string", v)
	}
	return nil
})

// NonEmptyString matches a non-empty JSON string.
var NonEmptyString Matcher = MatchFunc(func(path string, v any) []string {
	if s, ok := v.(string); !ok || s == "" {
		return mismatch(path, "non-empty string", v)
	}
	return nil
})

// Bool matches true or false.
var Bool Matcher = MatchFunc(func(path string, v any) []string {
	if _, ok := v.(bool); !ok {
		return mismatch(path, "bool", v)
	}
	return nil
})

// Num matches any JSON number.
var Num Matcher = MatchFunc(func(path string, v any) []string {
	if _, ok := v.(json.Number); !ok {
		return mismatch(path, "number", v)
	}
	return nil
})

// Int matches a JSON number written without a fraction or exponent.
var Int Matcher = MatchFunc(func(path string, v any) []string {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(n.String(), ".eE") {
		return mismatch(path, "integer", v)
	}
	return nil
})

// Array matches any JSON array.
var Array Matcher = ArrayOf(Any)

// Object matches any JSON object.
var Object Matcher = Open{}

// Eq matches a JSON value equal to want (a Go string, bool, int, float64 or nil).
func Eq(want any) Matcher {
	return MatchFunc(func(path string, v any) []string {
		got := v
		if n, ok := v.(json.Number); ok {
			if f, err := n.Float64(); err == nil {
				got = f
			}
		}
		w := want
		switch x := want.(type) {
		case int:
			w = float64(x)
		case int64:
			w = float64(x)
		}
		if !reflect.DeepEqual(got, w) {
			return mismatch(path, show(want), v)
		}
		return nil
	})
}

// OneOf matches a string equal to one of the given values.
func OneOf(values ...string) Matcher {
	return MatchFunc(func(path string, v any) []string {
		s, ok := v.(string)
		if ok {
			for _, want := range values {
				if s == want {
					return nil
				}
			}
		}
		return mismatch(path, "one of "+show(values), v)
	})
}

// Regexp matches a string that matches the pattern.
func Regexp(pattern string) Matcher {
	re := regexp.MustCompile(pattern)
	return MatchFunc(func(path string, v any) []string {
		s, ok := v.(string)
		if !ok || !re.MatchString(s) {
			return mismatch(path, "string matching /"+pattern+"/", v)
		}
		return nil
	})
}

// NullOr matches null or m.
func NullOr(m Matcher) Matcher {
	return MatchFunc(func(path string, v any) []string {
		if v == nil {
			return nil
		}
		return m.Match(path, v)
	})
}

// ArrayOf matches an array whose every element matches m.
func ArrayOf(m Matcher) Matcher {
	return MatchFunc(func(path string, v any) []string {
		a, ok := v.([]any)
		if !ok {
			return mismatch(path, "array", v)
		}
		var errs []string
		for i, e := range a {
			errs = append(errs, m.Match(fmt.Sprintf("%s[%d]", path, i), e)...)
		}
		return errs
	})
}

// NonEmptyArrayOf is ArrayOf that also requires at least one element.
func NonEmptyArrayOf(m Matcher) Matcher {
	return MatchFunc(func(path string, v any) []string {
		if a, ok := v.([]any); ok && len(a) == 0 {
			return mismatch(path, "non-empty array", v)
		}
		return ArrayOf(m).Match(path, v)
	})
}

// MapOf matches an object whose every value matches m (a Python dict[str, X]).
func MapOf(m Matcher) Matcher {
	return MatchFunc(func(path string, v any) []string {
		o, ok := v.(map[string]any)
		if !ok {
			return mismatch(path, "object", v)
		}
		var errs []string
		for k, e := range o {
			errs = append(errs, m.Match(path+"."+k, e)...)
		}
		sort.Strings(errs)
		return errs
	})
}

// All matches when every matcher matches.
func All(ms ...Matcher) Matcher {
	return MatchFunc(func(path string, v any) []string {
		var errs []string
		for _, m := range ms {
			errs = append(errs, m.Match(path, v)...)
		}
		return errs
	})
}

// TimestampUTC matches an ISO-8601 timestamp carrying a zone ("…Z" or "…+00:00"), which is
// what pydantic emits for timezone-aware datetimes.
var TimestampUTC Matcher = MatchFunc(func(path string, v any) []string {
	s, ok := v.(string)
	if ok {
		if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return nil
		}
	}
	return mismatch(path, "RFC 3339 timestamp with zone", v)
})

// TimestampNaive matches an ISO-8601 timestamp with no zone ("2026-10-06T22:12:31.921459"),
// which is what Python emits for naive datetimes (datetime.utcnow(), timezone-less columns).
var TimestampNaive Matcher = MatchFunc(func(path string, v any) []string {
	s, ok := v.(string)
	if ok {
		if _, err := time.Parse("2006-01-02T15:04:05.999999999", s); err == nil {
			return nil
		}
	}
	return mismatch(path, "naive ISO-8601 timestamp (no zone)", v)
})

// UUID matches a canonical lowercase UUID string.
var UUID Matcher = Regexp(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidationError matches FastAPI's 422 body: {"detail": [{type, loc, msg, input, ctx?, url?}]}
// with at least one error whose loc equals wantLoc. The msg/type strings are pydantic's and are
// not frozen; clients only read the status and, at most, loc.
func ValidationError(wantLoc ...any) Matcher {
	item := Open{"type": String, "loc": Array, "msg": String}
	return MatchFunc(func(path string, v any) []string {
		errs := Obj{"detail": NonEmptyArrayOf(item)}.Match(path, v)
		if len(errs) > 0 {
			return errs
		}
		for _, e := range v.(map[string]any)["detail"].([]any) {
			loc, _ := e.(map[string]any)["loc"].([]any)
			if len(loc) != len(wantLoc) {
				continue
			}
			same := true
			for i := range loc {
				if fmt.Sprint(loc[i]) != fmt.Sprint(wantLoc[i]) {
					same = false
					break
				}
			}
			if same {
				return nil
			}
		}
		return mismatch(path+".detail[*].loc", show(wantLoc), v)
	})
}
