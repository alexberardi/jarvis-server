package computeruse

import (
	"strings"
	"testing"
)

func ip(n int) *int { return &n }

func TestValidate(t *testing.T) {
	click := func(mod func(*Action)) Action {
		a := Action{Kind: "click", Intent: "open the cart", X: ip(10), Y: ip(20)}
		if mod != nil {
			mod(&a)
		}
		return a
	}
	cases := []struct {
		name string
		a    Action
		ok   bool
	}{
		{"click", click(nil), true},
		{"unknown kind", Action{Kind: "teleport", Intent: "x"}, false},
		{"intent empty", click(func(a *Action) { a.Intent = "" }), false},
		{"intent blank", click(func(a *Action) { a.Intent = "   " }), false},
		{"intent 200", click(func(a *Action) { a.Intent = strings.Repeat("é", 200) }), true},
		{"intent 201", click(func(a *Action) { a.Intent = strings.Repeat("a", 201) }), false},
		{"intent two lines", click(func(a *Action) { a.Intent = "one\ntwo" }), false},
		{"click without coords", click(func(a *Action) { a.X, a.Y = nil, nil }), false},
		{"click without y", click(func(a *Action) { a.Y = nil }), false},
		{"click negative", click(func(a *Action) { a.X = ip(-1) }), false},
		{"click right button", click(func(a *Action) { a.Button = "right" }), true},
		{"click bad button", click(func(a *Action) { a.Button = "back" }), false},
		{"double click", Action{Kind: "double_click", Intent: "open", X: ip(1), Y: ip(1)}, true},
		{"move", Action{Kind: "move", Intent: "hover", X: ip(1), Y: ip(1)}, true},
		{"drag", Action{Kind: "drag", Intent: "drag", Path: [][2]int{{1, 1}, {5, 5}}}, true},
		{"drag one point", Action{Kind: "drag", Intent: "drag", Path: [][2]int{{1, 1}}}, false},
		{"scroll", Action{Kind: "scroll", Intent: "down", DY: 300}, true},
		{"scroll nothing", Action{Kind: "scroll", Intent: "down"}, false},
		{"keypress chord", Action{Kind: "keypress", Intent: "save", Keys: []string{"ctrl", "s"}}, true},
		{"keypress modifiers and F12", Action{Kind: "keypress", Intent: "devtools", Keys: []string{"Cmd", "alt", "shift", "meta", "super", "F12"}}, true},
		{"keypress F13", Action{Kind: "keypress", Intent: "x", Keys: []string{"f13"}}, false},
		{"keypress unknown key", Action{Kind: "keypress", Intent: "x", Keys: []string{"ctrl", "hyper"}}, false},
		{"keypress none", Action{Kind: "keypress", Intent: "x"}, false},
		{"type", Action{Kind: "type", Intent: "search", Text: "running shoes"}, true},
		{"type empty", Action{Kind: "type", Intent: "search"}, false},
		{"type 2000", Action{Kind: "type", Intent: "note", Text: strings.Repeat("ü", 2000)}, true},
		{"type 2001", Action{Kind: "type", Intent: "note", Text: strings.Repeat("a", 2001)}, false},
		{"set_value 2001", Action{Kind: "set_value", Intent: "note", Ref: "e1", Text: strings.Repeat("a", 2001)}, false},
		{"set_value clears", Action{Kind: "set_value", Intent: "clear", Ref: "e1"}, true},
		{"set_value no ref", Action{Kind: "set_value", Intent: "fill", Text: "x"}, false},
		{"press", Action{Kind: "press", Intent: "add to cart", Ref: "e12"}, true},
		{"press no ref", Action{Kind: "press", Intent: "add to cart"}, false},
		{"select", Action{Kind: "select", Intent: "size", Ref: "e3", Text: "M"}, true},
		{"wait 5000", Action{Kind: "wait", Intent: "load", MS: 5000}, true},
		{"wait 5001", Action{Kind: "wait", Intent: "load", MS: 5001}, false},
		{"wait 0", Action{Kind: "wait", Intent: "load"}, false},
		{"navigate https", Action{Kind: "navigate", Intent: "go", URL: "https://shop.example/x?q=1"}, true},
		{"navigate http", Action{Kind: "navigate", Intent: "go", URL: "http://shop.example"}, true},
		{"navigate file", Action{Kind: "navigate", Intent: "go", URL: "file:///etc/passwd"}, false},
		{"navigate javascript", Action{Kind: "navigate", Intent: "go", URL: "javascript:alert(1)"}, false},
		{"navigate no host", Action{Kind: "navigate", Intent: "go", URL: "https://"}, false},
		{"screenshot", Action{Kind: "screenshot", Intent: "look"}, true},
		{"zoom", Action{Kind: "zoom", Intent: "read price", Region: &[4]int{0, 0, 100, 50}}, true},
		{"zoom no region", Action{Kind: "zoom", Intent: "read price"}, false},
		{"zoom empty region", Action{Kind: "zoom", Intent: "read price", Region: &[4]int{0, 0, 0, 50}}, false},
	}
	for _, c := range cases {
		err := c.a.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestValidateBatch(t *testing.T) {
	a := Action{Kind: "wait", Intent: "load", MS: 100}
	batch := func(n int) []Action {
		out := make([]Action, n)
		for i := range out {
			out[i] = a
		}
		return out
	}
	if err := ValidateBatch(batch(12)); err != nil {
		t.Errorf("12 actions: %v", err)
	}
	if err := ValidateBatch(batch(13)); err == nil {
		t.Error("13 actions: want error")
	}
	if err := ValidateBatch(nil); err == nil {
		t.Error("no actions: want error")
	}
	bad := batch(3)
	bad[2].MS = 9000
	if err := ValidateBatch(bad); err == nil || !strings.Contains(err.Error(), "action 2") {
		t.Errorf("bad action: err = %v, want it to name action 2", err)
	}
}

func TestDigestStable(t *testing.T) {
	tgt := Target{Kind: "browser", Site: "shop.example", TabID: "7"}
	a := Action{Kind: "press", Intent: "place the order", Ref: "e12", CaptureID: "c1"}
	d := a.Digest("cr_1", tgt)
	if len(d) != 64 {
		t.Fatalf("digest %q is not sha256 hex", d)
	}
	if again := a.Digest("cr_1", tgt); again != d {
		t.Errorf("same action, different digest: %s vs %s", d, again)
	}
	withApproval := a
	withApproval.Approval = "tok_abc"
	if got := withApproval.Digest("cr_1", tgt); got != d {
		t.Error("approval field changed the digest")
	}
	if a.Digest("cr_2", tgt) == d {
		t.Error("other run id, same digest")
	}
	if a.Digest("cr_1", Target{Kind: "browser", Site: "evil.example"}) == d {
		t.Error("other site, same digest")
	}
	if a.Digest("cr_1", Target{Kind: "desktop", App: "com.apple.Notes"}) == d {
		t.Error("other app, same digest")
	}
	// The tab is not part of the approval: the same order on a reopened tab stays approved.
	if a.Digest("cr_1", Target{Kind: "browser", Site: "shop.example", TabID: "8"}) != d {
		t.Error("tab id changed the digest")
	}
	other := a
	other.Ref = "e13"
	if other.Digest("cr_1", tgt) == d {
		t.Error("other ref, same digest")
	}
	// X and Y are distinct fields: swapping them must change the digest.
	p := Action{Kind: "click", Intent: "x", X: ip(1), Y: ip(2)}
	q := Action{Kind: "click", Intent: "x", X: ip(2), Y: ip(1)}
	if p.Digest("cr_1", tgt) == q.Digest("cr_1", tgt) {
		t.Error("x/y swap, same digest")
	}
}
