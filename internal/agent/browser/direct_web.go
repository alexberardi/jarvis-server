package browser

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Funnel stage 1 for the web (VOICE-FLOWS.md Q27, Q32): "search for dogs on Google", "google
// dogs", "open a new tab", "go to youtube.com" open in a new tab of the user's browser, at once.
// The model answered "search for dogs on Google" with a web search of its own, read out, and
// opened sites through the system's handler, a new window each time.

var (
	// A place said before the request: "in a new tab, search …", "open a new tab and search …".
	reWebLead = regexp.MustCompile(`(?i)^(?:(?:open|start)\s+(?:a\s+|another\s+)?new\s+tab\s+and\s+|in\s+(?:a\s+|another\s+)?new\s+tab,?\s+|in\s+(?:firefox|chrome|(?:the\s+|my\s+)?browser),?\s+|on\s+google,?\s+)`)
	// The browser named after it: "… on Google", "… in a new tab", "… in Firefox".
	reWebTail = regexp.MustCompile(`(?i)[,]?\s+(?:on|in|with|using)\s+(?:(?:a|another|the|my)\s+)?(?:google(?:\s+(?:page|tab))?|the\s+web|the\s+internet|firefox|chrome|browser|new\s+tab)$`)
	reSearch  = regexp.MustCompile(`(?i)^(?:search|look\s+up|google)(?:\s+(?:google|the\s+web|online))?(?:\s+for)?\s+(.+)$`)
	reNewTab  = regexp.MustCompile(`(?i)^(?:open|start|give\s+me)\s+(?:a\s+|another\s+)?new\s+tab$|^new\s+tab$`)
	reGoTo    = regexp.MustCompile(`(?i)^(?:go\s+to|open|navigate\s+to|pull\s+up|bring\s+up)\s+((?:[a-z0-9-]+\.)+[a-z]{2,}(?:/\S*)?)$`)
	rePolite  = regexp.MustCompile(`(?i)^(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+|can\s+you\s+|could\s+you\s+)?`)
)

// webRequest is the page "search …", "new tab" or "go to …" means, or "" when it isn't one.
func webRequest(said string) string {
	s := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(said), ".!?"))
	s = rePolite.ReplaceAllString(s, "")
	browserSaid := false
	if l := reWebLead.FindString(s); l != "" {
		s, browserSaid = s[len(l):], true
	}
	for {
		loc := reWebTail.FindStringIndex(s)
		if loc == nil {
			break
		}
		s, browserSaid = strings.TrimSpace(s[:loc[0]]), true
	}
	switch {
	case reNewTab.MatchString(s):
		return "https://www.google.com/"
	case reGoTo.MatchString(s):
		return "https://" + reGoTo.FindStringSubmatch(s)[1]
	}
	m := reSearch.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	// A bare "search for X" may be a question for Jarvis ("search my notes"); "google X" or a
	// browser named is the page.
	if !browserSaid && !strings.HasPrefix(strings.ToLower(s), "google") {
		return ""
	}
	q := strings.TrimSpace(m[1])
	if q == "" {
		return ""
	}
	return "https://www.google.com/search?q=" + url.QueryEscape(q)
}

// directWeb opens what webRequest names in a new tab; ok false passes it on.
func (t *Tools) directWeb(ctx context.Context, said string) (map[string]any, bool) {
	u := webRequest(said)
	if u == "" || !node.Enabled(ctx, ToolOpen) {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := t.openURL(ctx, u)
	if err != nil {
		return nil, false // the model can say why
	}
	out["success"], out[node.IslandOnlyKey] = true, true
	t.refreshHints(ctx)
	return out, true
}
