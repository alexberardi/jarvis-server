package computeruse

import "testing"

func TestSnapElement(t *testing.T) {
	s := Snapshot{Gen: 3, Elements: []SnapElement{
		{Ref: "e1", Role: "textbox", Name: "Password", Type: "password"},
		{Ref: "e2", Role: "button", Name: "Place order", Type: "submit"},
	}}
	if e := s.Find("e1").Element(); !e.Secure || e.Name != "Password" {
		t.Errorf("password field: %+v", e)
	}
	if e := s.Find("e2").Element(); e.Secure || e.Role != "button" || e.Type != "submit" {
		t.Errorf("button: %+v", e)
	}
	if s.Find("e9") != nil {
		t.Error("found a ref the snapshot doesn't have")
	}
}
