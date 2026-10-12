package node

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Truncate shortens s to at most n runes, marking a cut with "…".
func Truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// logArgValue is the longest a single argument value gets in a log line.
const logArgValue = 80

// LogArgs renders a tool call's arguments for an Info log line: sorted keys, each value
// shortened, so a long page text or base64 blob never fills the log. Arguments can hold what the
// user said; the local log may keep that, shortened.
func LogArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var v string
		switch x := args[k].(type) {
		case string:
			v = fmt.Sprintf("%q", Truncate(x, logArgValue))
		default:
			b, err := json.Marshal(x)
			if err != nil {
				b = []byte(fmt.Sprint(x))
			}
			v = Truncate(string(b), logArgValue)
		}
		parts = append(parts, k+"="+v)
	}
	return "{" + strings.Join(parts, " ") + "}"
}
