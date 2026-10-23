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

// An action without an intent gets the reply's thought when it is the only one, else its kind
// and ref (risk comes from the element, never from the intent).
func TestParseMissingIntent(t *testing.T) {
	r, err := parseReply(`{"thought": "Open the watch list.\nThen pick one.", "actions": [{"kind": "click", "ref": "e5"}]}`)
	if err != nil || r.Actions[0].Intent != "Open the watch list." {
		t.Fatalf("%+v %v", r.Actions, err)
	}
	// In a batch, each its own: one thought for every action would say nothing.
	r, err = parseReply(`{"thought": "Fill it in.", "actions": [{"kind": "set_value", "ref": "e2", "text": "Avery"}, {"kind": "wait", "ms": 500, "intent": " "}, {"kind": "press", "ref": "e3", "intent": "Accept"}]}`)
	if err != nil || r.Actions[0].Intent != "set_value e2" || r.Actions[1].Intent != "wait" || r.Actions[2].Intent != "Accept" {
		t.Fatalf("%+v %v", r.Actions, err)
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

// The house model sometimes stops right before the closing brace, and at temperature 0 the
// retry is byte-identical, so the run died on "unexpected end of JSON input" (#528). An answer
// missing only closers is repaired; one cut mid-string stays an error (its content is lost).
func TestParseReplyTruncated(t *testing.T) {
	r, err := parseReply(`{"thought": "t", "actions": [{"kind": "keypress", "keys": ["enter"]}]`)
	if err != nil {
		t.Fatalf("truncated reply: %v", err)
	}
	if len(r.Actions) != 1 || r.Actions[0].Kind != "keypress" {
		t.Fatalf("truncated reply parsed to %+v", r)
	}
	for _, s := range []string{
		`{"thought": "t", "actions": [{"kind": "type", "text": "mi`, // mid-string
		`{"done": true, "summary": "s"}}`,                           // over-closed
	} {
		if _, err := parseReply(s); err == nil {
			t.Errorf("parseReply(%q) = nil error, want one", s)
		}
	}
}
