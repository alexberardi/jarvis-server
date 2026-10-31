package computeruse

import "testing"

func TestSnapElement(t *testing.T) {
	s := Snapshot{Gen: 3, Elements: []SnapElement{
		{Ref: "e1", Role: "textbox", Name: "Password", Type: "password"},
		{Ref: "e2", Role: "button", Name: "Place order", Type: "submit"},
		{Ref: "a1", Role: "AXSecureTextField", Name: "Password"},
		{Ref: "a2", Role: "passwordtext", Name: "Password"},
	}}
	if e := s.Find("e1").Element(); !e.Secure || e.Name != "Password" {
		t.Errorf("password field: %+v", e)
	}
	if e := s.Find("e2").Element(); e.Secure || e.Role != "button" || e.Type != "submit" {
		t.Errorf("button: %+v", e)
	}
	if e := s.Find("a1").Element(); !e.Secure {
		t.Errorf("secure text field (accessibility): %+v", e)
	}
	if e := s.Find("a2").Element(); !e.Secure {
		t.Errorf("password text (AT-SPI): %+v", e)
	}
	if s.Find("e9") != nil {
		t.Error("found a ref the snapshot doesn't have")
	}
}

// Position is one of nine labels (ELEMENT-LAYER-WORK.md §2); anything else is no position.
func TestValidPosition(t *testing.T) {
	for _, v := range []string{"top", "middle", "bottom"} {
		for _, h := range []string{"left", "center", "right"} {
			if !ValidPosition(v + "-" + h) {
				t.Errorf("%s-%s rejected", v, h)
			}
		}
	}
	for _, p := range []string{"", "below-the-fold", "TOP-LEFT", "top_left", "middle", "top-left "} {
		if ValidPosition(p) {
			t.Errorf("%q accepted", p)
		}
	}
}

// PositionIn: where an element's centre sits in its window, for sources that know both as screen
// bounds (AT-SPI now; Windows UIA and macOS AX later). Nothing when the centre is outside.
func TestPositionIn(t *testing.T) {
	win := [4]int{100, 50, 900, 600} // x, y, width, height on screen
	for _, c := range []struct {
		el   [4]int
		want string
	}{
		{[4]int{110, 60, 20, 20}, "top-left"},
		{[4]int{500, 300, 100, 50}, "middle-center"},
		{[4]int{960, 600, 30, 30}, "bottom-right"},
		{[4]int{1100, 300, 20, 20}, ""}, // right of the window
		{[4]int{500, 700, 20, 20}, ""},  // below it
		{[4]int{500, 300, 0, 0}, ""},    // no size
	} {
		if got := PositionIn(c.el, win); got != c.want {
			t.Errorf("PositionIn(%v) = %q, want %q", c.el, got, c.want)
		}
	}
	if got := PositionIn([4]int{10, 10, 5, 5}, [4]int{0, 0, 0, 0}); got != "" {
		t.Errorf("an empty window gave %q", got)
	}
}
