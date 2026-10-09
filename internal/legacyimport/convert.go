package legacyimport

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Timestamp formats: cc and the settings tables write milliseconds (cc dbTime, the SQLite
// strftime('%f') defaults), auth and notifications microseconds (their dbTime). All UTC, Z.
const (
	msTime = "2006-01-02T15:04:05.000Z"
	usTime = "2006-01-02T15:04:05.000000Z"
)

// legacyLayouts are what Postgres' JSON renders for timestamptz (with an offset) and
// timestamp (naive, UTC on every legacy service).
var legacyLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
}

// parseTime reads a legacy timestamp; a naive one is UTC.
func parseTime(s string) (time.Time, error) {
	for _, l := range legacyLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable timestamp %q", s)
}

// conv converts one row's values, remembering the first error.
type conv struct {
	r      Row
	layout string
	err    error
}

func (c *conv) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

// S is a text column (nil stays NULL).
func (c *conv) S(k string) any {
	switch v := c.r[k].(type) {
	case nil:
		return nil
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// str is S as a Go string ("" for NULL).
func (c *conv) str(k string) string {
	if s, ok := c.S(k).(string); ok {
		return s
	}
	return ""
}

// T is a timestamp column rendered ISO-8601 UTC with Z (nil stays NULL).
func (c *conv) T(k string) any {
	v := c.r[k]
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		c.fail(fmt.Errorf("%s: not a timestamp", k))
		return nil
	}
	t, err := parseTime(s)
	if err != nil {
		c.fail(fmt.Errorf("%s: %w", k, err))
		return nil
	}
	return t.Format(c.layout)
}

// TOr is T with a fallback for NULL (for columns jarvisd declares NOT NULL).
func (c *conv) TOr(k string, def any) any {
	if v := c.T(k); v != nil {
		return v
	}
	return def
}

// time reads a timestamp column as a time; ok is false for NULL or unreadable.
func (c *conv) time(k string) (time.Time, bool) {
	s, isStr := c.r[k].(string)
	if !isStr {
		return time.Time{}, false
	}
	t, err := parseTime(s)
	return t, err == nil
}

// B is a boolean column as 0/1; NULL becomes def (which may be nil for a nullable column).
func (c *conv) B(k string, def any) any {
	switch v := c.r[k].(type) {
	case nil:
		return def
	case bool:
		if v {
			return 1
		}
		return 0
	case json.Number:
		if v.String() == "0" {
			return 0
		}
		return 1
	default:
		c.fail(fmt.Errorf("%s: not a boolean", k))
		return def
	}
}

// truthy reads a boolean column; NULL is def.
func (c *conv) truthy(k string, def bool) bool {
	v := c.B(k, nil)
	if v == nil {
		return def
	}
	return v == 1
}

// N is a number column (int64 when integral, else float64; nil stays NULL).
func (c *conv) N(k string) any {
	switch v := c.r[k].(type) {
	case nil:
		return nil
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		f, err := v.Float64()
		if err != nil {
			c.fail(fmt.Errorf("%s: not a number", k))
			return nil
		}
		return f
	case string: // Postgres renders NaN/Infinity as strings
		c.fail(fmt.Errorf("%s: not a finite number", k))
		return nil
	default:
		c.fail(fmt.Errorf("%s: not a number", k))
		return nil
	}
}

// id reads an integer id column; ok is false for NULL.
func (c *conv) id(k string) (int64, bool) {
	if i, ok := c.N(k).(int64); ok {
		return i, true
	}
	return 0, false
}

// MaskEmail hides most of an address for printing: a***@e***.com.
func MaskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "***"
	}
	out := local[:1] + "***@"
	if domain == "" {
		return out + "***"
	}
	tld := ""
	if i := strings.LastIndexByte(domain, '.'); i > 0 {
		tld = domain[i:]
		domain = domain[:i]
	}
	if domain == "" {
		return out + "***" + tld
	}
	return out + domain[:1] + "***" + tld
}
