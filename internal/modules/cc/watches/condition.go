// Package watches is the watches subsystem (docs/autonomy/PLAN.md §2): a source that observes a
// value, a deterministic condition over it, and a gated notification when it holds.
package watches

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Condition struct {
	Op    string `json:"op"`
	Field string `json:"field,omitempty"` // a signal fact; "" = the observed value
	Value string `json:"value,omitempty"`
	For   string `json:"for,omitempty"` // absent_for: a Go duration, 1m..24h
}

const (
	OpLT        = "lt"
	OpGT        = "gt"
	OpChanged   = "changed"
	OpEquals    = "equals"
	OpContains  = "contains"
	OpAbsentFor = "absent_for"
)

var numRe = regexp.MustCompile(`-?\d[\d,]*(?:\.\d+)?`)

func ParseNumber(s string) (float64, bool) {
	m := numRe.FindString(s)
	if m == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64)
	return f, err == nil
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func (c Condition) Validate(sourceKind string) error {
	switch c.Op {
	case OpLT, OpGT:
		if _, ok := ParseNumber(c.Value); !ok {
			return fmt.Errorf("condition: %s needs a number", c.Op)
		}
	case OpChanged:
	case OpEquals, OpContains:
		if c.Value == "" {
			return fmt.Errorf("condition: %s needs a value", c.Op)
		}
	case OpAbsentFor:
		if sourceKind != "signal" {
			return fmt.Errorf("condition: absent_for only works on signals")
		}
		if c.Field == "" || c.Value == "" {
			return fmt.Errorf("condition: absent_for needs a field and a value")
		}
		d, err := time.ParseDuration(c.For)
		if err != nil || d < time.Minute || d > 24*time.Hour {
			return fmt.Errorf("condition: absent_for needs a duration between 1m and 24h")
		}
	default:
		return fmt.Errorf("condition: unknown op %q", c.Op)
	}
	return nil
}

func Eval(c Condition, cur, prev string) bool {
	if cur == "" {
		return false
	}
	switch c.Op {
	case OpLT, OpGT:
		a, ok1 := ParseNumber(cur)
		b, ok2 := ParseNumber(c.Value)
		if !ok1 || !ok2 {
			return false
		}
		if c.Op == OpLT {
			return a < b
		}
		return a > b
	case OpChanged:
		return prev != "" && norm(cur) != norm(prev)
	case OpEquals:
		a, ok1 := ParseNumber(cur)
		b, ok2 := ParseNumber(c.Value)
		if ok1 && ok2 {
			return a == b
		}
		return norm(cur) == norm(c.Value)
	case OpContains:
		return strings.Contains(norm(cur), norm(c.Value))
	}
	return false
}
