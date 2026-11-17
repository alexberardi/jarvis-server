package computer

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

func TestPastCheckout(t *testing.T) {
	obs := Observation{Elements: []Element{{Ref: "e1", Role: "button", Name: "CHECK OUT · $15.98"}, {Ref: "e2", Role: "button", Name: "Add to cart"}}}
	press := func(ref string) []computeruse.Action { return []computeruse.Action{{Kind: "press", Ref: ref}} }
	if pastCheckout("add a pizza to the cart, stop before checkout", obs, press("e1")) == "" {
		t.Error("checkout pressed though the request says to stop before it")
	}
	if got := pastCheckout("add a pizza to the cart, stop before checkout", obs, press("e2")); got != "" {
		t.Errorf("add to cart refused: %s", got)
	}
	if got := pastCheckout("order me a pizza", obs, press("e1")); got != "" {
		t.Errorf("checkout refused on an order: %s", got)
	}
}

func TestMadeUpDetail(t *testing.T) {
	obs := Observation{Elements: []Element{
		{Ref: "e1", Role: "textbox", Name: "Street Address"},
		{Ref: "e2", Role: "textbox", Name: "Search products"},
		{Ref: "e3", Role: "textbox", Name: "Mobile", Autocomplete: "tel-national"},
	}}
	typ := func(ref, text string) []computeruse.Action {
		return []computeruse.Action{{Kind: "type", Ref: ref, Text: text}}
	}
	said := "order a pizza to 12 Elm Street, Springfield"
	if madeUpDetail(said, obs, typ("e1", "123 Main St")) == "" {
		t.Error("an address the request doesn't hold was typed")
	}
	if madeUpDetail(said, obs, typ("e3", "555-0100")) == "" {
		t.Error("a phone number the request doesn't hold was typed")
	}
	if got := madeUpDetail(said, obs, typ("e1", "12 Elm Street")); got != "" {
		t.Errorf("the request's own address asked about: %s", got)
	}
	if got := madeUpDetail(said, obs, typ("e2", "pepperoni")); got != "" {
		t.Errorf("a search asked about: %s", got)
	}
}
