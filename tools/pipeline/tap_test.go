package main

import "testing"

// A chat call's cap reaches an installed jarvisd that reads only max_tokens.
func TestWithMaxTokens(t *testing.T) {
	got := string(withMaxTokens("/openai/v1/chat/completions", []byte(`{"model":"live","max_completion_tokens":1024}`)))
	if got != `{"max_tokens":1024,"model":"live","max_completion_tokens":1024}` {
		t.Fatalf("body %s", got)
	}
	for _, same := range []string{`{"model":"live"}`, `{"max_tokens":5,"max_completion_tokens":9}`} {
		if got := string(withMaxTokens("/openai/v1/chat/completions", []byte(same))); got != same {
			t.Errorf("%s became %s", same, got)
		}
	}
}
