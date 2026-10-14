package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/jarvispage"
)

// The Jarvis page (DESIGN.md §14 "The Jarvis page in front"): browser_fill and browser_click
// never act on a tab of this agent's own server. Its page is the chat the user is typing in.

// pageCheckTimeout bounds the extra list_tabs a fill or click makes to see the tab's address.
const pageCheckTimeout = 3 * time.Second

// CodeJarvisPage is the error code of a fill or click refused on the Jarvis page.
const CodeJarvisPage = "jarvis_page"

// tabURL is the address of the tab a command means (the given tab_id, else the current tab of
// the browser it goes to), from list_tabs. ok false when it can't be told.
func (t *Tools) tabURL(ctx context.Context, args map[string]any) (string, bool) {
	key, tab, err := tabArg(args)
	if err != nil {
		return "", false
	}
	b, err := t.Hub.Lookup(key)
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, pageCheckTimeout)
	defer cancel()
	res, _, err := t.Hub.Call(ctx, b.ID, "list_tabs", map[string]any{})
	if err != nil {
		return "", false
	}
	list, _ := res["tabs"].([]any)
	for _, e := range list {
		m, _ := e.(map[string]any)
		if m == nil {
			continue
		}
		u, _ := m["url"].(string)
		if tab != nil {
			if id, ok := m["tab_id"].(float64); ok && int64(id) == tab.(int64) {
				return u, true
			}
			continue
		}
		if m["current"] == true {
			return u, true
		}
	}
	return "", false
}

// notJarvisPage refuses a fill or click whose tab is on this agent's server. A tab whose
// address can't be told is let through: the extension's own errors then apply.
func (t *Tools) notJarvisPage(ctx context.Context, args map[string]any, outcome string) error {
	if !t.Origins.Known() {
		return nil
	}
	u, ok := t.tabURL(ctx, args)
	if !ok || !t.Origins.MatchURL(u) {
		return nil
	}
	return &CommandError{Code: CodeJarvisPage, Message: fmt.Sprintf(
		"%s: that tab is the Jarvis page itself (the chat the user is typing in), so this would only act on the chat. "+
			"Tell the user %s and ask which page they meant: they can open it in the browser, then ask again.",
		outcome, lowerFirst(outcome))}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]|0x20) + s[1:]
}

// FocusedTab is the current tab of a connected browser that has the keyboard focus now
// (jarvispage.Tab): the address and title, from list_tabs.
func (h *Hub) FocusedTab(ctx context.Context) (string, string, bool) {
	for _, b := range h.Browsers() {
		if !b.Focused {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, pageCheckTimeout)
		res, _, err := h.Call(cctx, b.ID, "list_tabs", map[string]any{})
		cancel()
		if err != nil {
			continue
		}
		list, _ := res["tabs"].([]any)
		for _, e := range list {
			if m, _ := e.(map[string]any); m != nil && m["current"] == true {
				u, _ := m["url"].(string)
				title, _ := m["title"].(string)
				return u, title, true
			}
		}
	}
	return "", "", false
}

var _ jarvispage.Tab = (*Hub)(nil).FocusedTab
