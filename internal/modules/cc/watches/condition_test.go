package watches

import "testing"

func TestParseNumber(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"$1,299.00", 1299, true},
		{"-3.5°F", -3.5, true},
		{"Price: 99", 99, true},
		{"in stock", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := ParseNumber(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("ParseNumber(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestEval(t *testing.T) {
	tests := []struct {
		name string
		c    Condition
		cur  string
		prev string
		want bool
	}{
		{"lt below", Condition{Op: OpLT, Value: "100"}, "$99", "", true},
		{"lt above", Condition{Op: OpLT, Value: "100"}, "$129", "", false},
		{"lt unparseable cur", Condition{Op: OpLT, Value: "100"}, "n/a", "", false},
		{"gt above", Condition{Op: OpGT, Value: "100"}, "105", "", true},
		{"changed same after norm", Condition{Op: OpChanged}, "In stock", "in  stock", false},
		{"changed different", Condition{Op: OpChanged}, "Sold out", "In stock", true},
		{"changed no prev", Condition{Op: OpChanged}, "In stock", "", false},
		{"equals case-insensitive", Condition{Op: OpEquals, Value: "open"}, "Open", "", true},
		{"equals numeric", Condition{Op: OpEquals, Value: "10"}, "10.0", "", true},
		{"contains", Condition{Op: OpContains, Value: "in stock"}, "Back in stock today", "", true},
		{"absent_for is timer-driven", Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "10m"}, "open", "", false},
	}
	for _, tt := range tests {
		if got := Eval(tt.c, tt.cur, tt.prev); got != tt.want {
			t.Errorf("%s: Eval(%+v, %q, %q) = %v; want %v", tt.name, tt.c, tt.cur, tt.prev, got, tt.want)
		}
	}
	for _, op := range []string{OpLT, OpGT, OpChanged, OpEquals, OpContains, OpAbsentFor} {
		if got := Eval(Condition{Op: op, Value: "100"}, "", "x"); got {
			t.Errorf("Eval op %s with empty cur = true; want false", op)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		c       Condition
		source  string
		wantErr string
	}{
		{"unknown op", Condition{Op: "bogus"}, "web", `condition: unknown op "bogus"`},
		{"lt non-numeric value", Condition{Op: OpLT, Value: "soon"}, "web", "condition: lt needs a number"},
		{"gt non-numeric value", Condition{Op: OpGT, Value: "soon"}, "web", "condition: gt needs a number"},
		{"equals empty value", Condition{Op: OpEquals}, "web", "condition: equals needs a value"},
		{"contains empty value", Condition{Op: OpContains}, "web", "condition: contains needs a value"},
		{"absent_for on web", Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "10m"}, "web",
			"condition: absent_for only works on signals"},
		{"absent_for missing field", Condition{Op: OpAbsentFor, Value: "closed", For: "10m"}, "signal",
			"condition: absent_for needs a field and a value"},
		{"absent_for missing value", Condition{Op: OpAbsentFor, Field: "state", For: "10m"}, "signal",
			"condition: absent_for needs a field and a value"},
		{"absent_for unparsable duration", Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "ten"}, "signal",
			"condition: absent_for needs a duration between 1m and 24h"},
		{"absent_for too short", Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "30s"}, "signal",
			"condition: absent_for needs a duration between 1m and 24h"},
		{"absent_for too long", Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "48h"}, "signal",
			"condition: absent_for needs a duration between 1m and 24h"},
		{"absent_for empty duration", Condition{Op: OpAbsentFor, Field: "state", Value: "closed"}, "signal",
			"condition: absent_for needs a duration between 1m and 24h"},
	}
	for _, tt := range tests {
		err := tt.c.Validate(tt.source)
		if err == nil || err.Error() != tt.wantErr {
			t.Errorf("%s: Validate(%q) = %v; want %q", tt.name, tt.source, err, tt.wantErr)
		}
	}

	valid := []struct {
		name   string
		c      Condition
		source string
	}{
		{"lt on web", Condition{Op: OpLT, Value: "100"}, "web"},
		{"changed on web", Condition{Op: OpChanged}, "web"},
		{"equals on web", Condition{Op: OpEquals, Value: "open"}, "web"},
		{"contains on web", Condition{Op: OpContains, Value: "in stock"}, "web"},
		{"absent_for on signal", Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "10m"}, "signal"},
	}
	for _, tt := range valid {
		if err := tt.c.Validate(tt.source); err != nil {
			t.Errorf("%s: Validate(%q) = %v; want nil", tt.name, tt.source, err)
		}
	}
}
