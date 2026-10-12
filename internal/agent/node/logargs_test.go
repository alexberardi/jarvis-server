package node

import (
	"strings"
	"testing"
)

func TestLogArgs(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := LogArgs(map[string]any{"window": "firefox", "scope": "window", "text": long, "n": 3, "opts": map[string]any{"a": long}})
	if !strings.HasPrefix(got, `{n=3 opts={"a":"xxx`) || !strings.Contains(got, `scope="window" text="xxx`) ||
		!strings.HasSuffix(got, `window="firefox"}`) || len(got) > 300 {
		t.Errorf("LogArgs = %s", got)
	}
	if LogArgs(nil) != "{}" {
		t.Error("empty args")
	}
	if Truncate("héllo", 2) != "hé…" || Truncate("hi", 2) != "hi" {
		t.Error("Truncate")
	}
}
