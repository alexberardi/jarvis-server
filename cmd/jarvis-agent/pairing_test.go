package main

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestPairingQuestionNamesHousehold(t *testing.T) {
	res := node.PairingResult{HouseholdID: "hh1", HouseholdName: "The Smiths", ApprovedBy: "sam@example.com", Room: "office"}
	want := "Approved by sam@example.com into household \"The Smiths\" (room \"office\").\nJoin this household? [y/N] "
	if got := pairingQuestion(res); got != want {
		t.Fatalf("got %q", got)
	}
	// An older server sends no name: the id stands in.
	if got := pairingQuestion(node.PairingResult{HouseholdID: "hh1", Room: "desktop"}); got != "Approved into household \"hh1\" (room \"desktop\").\nJoin this household? [y/N] " {
		t.Fatalf("got %q", got)
	}
}

func TestPairingAnswerYes(t *testing.T) {
	for line, want := range map[string]bool{"y\n": true, "YES\n": true, " yes": true, "\n": false, "n\n": false, "": false, "sure\n": false} {
		if got := pairingAnswerYes(line); got != want {
			t.Errorf("%q: %v", line, got)
		}
	}
}
