package computer

import (
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// The tests don't wait out a page the way a real one needs.
func init() { actSettle, actPoll = time.Millisecond, time.Millisecond }

func TestSameSnapshot(t *testing.T) {
	a := computeruse.Snapshot{URL: "https://x.example/", Elements: []computeruse.SnapElement{{Role: "tab", Name: "Carryout", State: []string{"selected"}}}}
	b := a
	b.Gen = 7 // a later snapshot of the same page
	if !sameSnapshot(a, b) {
		t.Fatal("the same page under a new gen differs")
	}
	b.Elements = append([]computeruse.SnapElement{}, a.Elements...)
	b.Elements = append(b.Elements, computeruse.SnapElement{Role: "button", Name: "Order from this store"})
	if sameSnapshot(a, b) {
		t.Fatal("a page with the store list is the same as one without")
	}
}
