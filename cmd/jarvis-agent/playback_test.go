package main

import "testing"

func TestDisplayState(t *testing.T) {
	if displayState(map[string]any{"active": false}) != nil {
		t.Fatal("inactive display counted as playing")
	}
	if displayState(map[string]any{"active": true, "controllable": false}) != nil {
		t.Fatal("a player it can't control counted as playing")
	}
	st := displayState(map[string]any{"active": true, "controllable": true, "paused": true, "position": 12.5, "duration": 90.0})
	if st == nil || st.State != "paused" || *st.Position != 12.5 || *st.Duration != 90 || len(st.Actions) != 3 {
		t.Fatalf("state = %+v", st)
	}
	if st := displayState(map[string]any{"active": true, "controllable": true, "paused": false}); st.State != "playing" {
		t.Fatalf("state = %+v", st)
	}
}
