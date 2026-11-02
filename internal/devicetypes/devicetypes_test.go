package devicetypes

import "testing"

func TestLookup(t *testing.T) {
	if l, ok := Lookup("light"); !ok || l.ControlType != "light" || l.Actions[0].Action != "turn_on" {
		t.Fatalf("light = %+v, %v", l, ok)
	}
	if s, ok := Lookup("sensor"); !ok || s.Controllable {
		t.Fatalf("sensor = %+v", s)
	}
	if f, ok := Lookup("dishwasher"); ok || f.Controllable || len(f.Actions) != 0 {
		t.Fatalf("fallback = %+v, %v", f, ok)
	}
}
