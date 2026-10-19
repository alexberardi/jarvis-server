package computer

import "testing"

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
