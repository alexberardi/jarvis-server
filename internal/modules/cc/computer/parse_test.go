package computer

import "testing"

// A click on a listed element (a ref, no coordinates) is a press: the house model writes it so.
func TestParseClickOnRefIsPress(t *testing.T) {
	r, err := parseReply(`{"actions": [{"kind": "click", "ref": "e5", "intent": "Open the watches"}, {"kind": "click", "x": 3, "y": 4, "intent": "There"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if a := r.Actions[0]; a.Kind != "press" || a.Ref != "e5" {
		t.Fatalf("action 0 = %+v", a)
	}
	if a := r.Actions[1]; a.Kind != "click" || *a.X != 3 {
		t.Fatalf("action 1 = %+v", a)
	}
	if _, err := parseReply(`{"actions": [{"kind": "click", "intent": "nowhere"}]}`); err == nil {
		t.Fatal("a click with neither a ref nor coordinates parsed")
	}
}

// An action without an intent gets the reply's thought, else its kind and ref (risk comes from
// the element, never from the intent).
func TestParseMissingIntent(t *testing.T) {
	r, err := parseReply(`{"thought": "Open the watch list.\nThen pick one.", "actions": [{"kind": "click", "ref": "e5"}, {"kind": "wait", "ms": 500, "intent": " "}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Actions[0].Intent != "Open the watch list." || r.Actions[1].Intent != "Open the watch list." {
		t.Fatalf("intents %q %q", r.Actions[0].Intent, r.Actions[1].Intent)
	}
	r, err = parseReply(`{"actions": [{"kind": "press", "ref": "e5"}]}`)
	if err != nil || r.Actions[0].Intent != "press e5" {
		t.Fatalf("%+v %v", r.Actions, err)
	}
}

func TestParseReply(t *testing.T) {
	ok := []struct{ in, want string }{
		{"```json\n{\"thought\": \"go\", \"actions\": [{\"kind\": \"press\", \"ref\": \"e1\", \"intent\": \"Open\"}]}\n```", "actions"},
		{`{"done": true, "summary": "Done."}`, "done"},
		{`{"ask": "Which size?", "reason": "unclear"}`, "ask"},
		{`{"ask": "Log in?", "reason": "whatever"}`, "ask"},
		{`{"checkout": {"product": "Soap", "price": "4.99"}, "actions": [{"kind": "press", "ref": "e5", "intent": "Pay"}]}`, "actions"},
	}
	for _, c := range ok {
		r, err := parseReply(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		got := "actions"
		if r.Done {
			got = "done"
		} else if r.Ask != "" {
			got = "ask"
			if r.Reason != ReasonUnclear {
				t.Errorf("%q: reason %q", c.in, r.Reason)
			}
		}
		if got != c.want {
			t.Errorf("%q: %s, want %s", c.in, got, c.want)
		}
	}
	for _, in := range []string{
		"", "I'll click it", `{"actions": []}`, `{"done": true}`,
		`{"actions": [{"kind": "press", "intent": "no ref"}]}`,
		`{"actions": [{"kind": "fly", "intent": "x"}]}`,
		`{"checkout": {"product": "Soap"}, "actions": [{"kind": "press", "ref": "e5", "intent": "Pay"}]}`,
	} {
		if _, err := parseReply(in); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
}
