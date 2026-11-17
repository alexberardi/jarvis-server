package computer

import (
	"context"
	"strconv"
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

func TestOtherSite(t *testing.T) {
	nav := func(u string) []computeruse.Action { return []computeruse.Action{{Kind: "navigate", URL: u}} }
	if otherSite("amazon.com", nav("https://www.bestbuy.com/")) == "" {
		t.Error("another shop opened from a task for amazon.com")
	}
	for _, u := range []string{"https://www.amazon.com/s?k=x", "https://smile.amazon.com/"} {
		if got := otherSite("amazon.com", nav(u)); got != "" {
			t.Errorf("%s refused: %s", u, got)
		}
	}
	if got := otherSite("", nav("https://www.bestbuy.com/")); got != "" {
		t.Errorf("a task with no site refused: %s", got)
	}
}

// The same button pressed round after round is stuck, even when each new snapshot gives it
// another ref and flips what the page shows.
func TestRepeatsAreCountedByElementName(t *testing.T) {
	page := func(order string) Observation {
		return Observation{URL: "https://shop.example.com/list", Title: order, Gen: 1,
			Elements: []Element{{Ref: "e1", Role: "button", Name: "price"}, {Ref: "e2", Role: "button", Name: "price"}}}
	}
	e := newEnv(t, page("start"))
	var script []string
	for i := 0; i < 12; i++ {
		e.exec.pages = append(e.exec.pages, page(strconv.Itoa(i)))
		script = append(script, actions(press("e"+strconv.Itoa(1+i%2), "Sort by price")))
	}
	e.script(script...)
	if out := e.loop.Run(context.Background(), task()); out.State != StateFailed || len(e.llm.reqs) > stuckGiveUp+1 {
		t.Fatalf("outcome %+v after %d calls", out, len(e.llm.reqs))
	}
}

func TestKeysAtLastElement(t *testing.T) {
	obs := Observation{Elements: []Element{{Ref: "e4", Role: "searchbox", Name: "Search"}, {Ref: "e5", Role: "button", Name: "Go"}}}
	hist := []Entry{{Kind: "set_value", On: `searchbox "Search"`}, {Kind: "said"}}
	got := keysAtLastElement(obs, hist, []computeruse.Action{{Kind: "keypress", Keys: []string{"Enter"}}})
	if got[0].Ref != "e4" {
		t.Errorf("Enter went to %q, not the search box just typed into", got[0].Ref)
	}
	got = keysAtLastElement(obs, []Entry{{Kind: "press", On: `link "Gone"`}}, []computeruse.Action{{Kind: "keypress", Keys: []string{"Enter"}}})
	if got[0].Ref != "" {
		t.Errorf("Enter went to %q though the last element is gone", got[0].Ref)
	}
}
