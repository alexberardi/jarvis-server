package main

import "testing"

func TestContainsReadsSpokenDigits(t *testing.T) {
	said := "The member ID is D, X, dash, seven, seven, one, two, zero. My number is 7 3 2, 5 5 5, 0 1 4 2."
	if !contains("My ID is D as in Dog, X as in X-ray, 7, 7, 1, 2, 0.", "DX-77120") {
		t.Error("missed a phonetic spelling")
	}
	for _, want := range []string{"DX-77120", "DX77120", "732-555-0142", "member id"} {
		if !contains(said, want) {
			t.Errorf("missed %q", want)
		}
	}
	if contains(said, "DX-77121") {
		t.Error("matched a wrong ID")
	}
}
