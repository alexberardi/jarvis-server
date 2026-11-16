package browser

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
)

// TopicMatchTabs is the request filter for jarvisd's "which of my open tabs match this
// pattern?" (docs/webapp/03 step 3, the web's webview_pick). The answer carries only what the
// pattern captured and each tab's title: tab addresses never leave the computer.
const TopicMatchTabs = "browser/+"

// maxMatches bounds an answer; a pick needs one.
const maxMatches = 10

// Tab is an open tab as list_tabs reports it.
type Tab struct{ URL, Title string }

// MatchTabs answers browser/match-tabs requests.
type MatchTabs struct {
	// Tabs lists the open tabs of every connected browser; ErrNotConnected when there are none.
	Tabs func(ctx context.Context) ([]Tab, error)
	Log  *slog.Logger
}

// Handle answers jarvis/nodes/{id}/browser/match-tabs with
// {"ok": true, "matches": [{"value", "title"}]} or {"ok": false, "error": {"code", "message"}}.
// A request without a usable correlation id, or another op, gets no answer.
func (m *MatchTabs) Handle(ctx context.Context, topic string, payload []byte) (string, []byte) {
	if topic[strings.LastIndex(topic, "/")+1:] != "match-tabs" {
		return "", nil
	}
	var p struct {
		CorrelationID string `json:"correlation_id"`
		Pattern       string `json:"pattern"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.CorrelationID == "" || strings.ContainsAny(p.CorrelationID, "/+#") {
		return "", nil
	}
	body, _ := json.Marshal(m.match(ctx, p.Pattern))
	return topic + "/response/" + p.CorrelationID, body
}

func (m *MatchTabs) match(ctx context.Context, pattern string) map[string]any {
	re, err := regexp.Compile(pattern)
	if err != nil || re.NumSubexp() < 1 {
		return fail("bad_pattern", "The pattern isn't a regular expression with a capture group.")
	}
	tabs, err := m.Tabs(ctx)
	if errors.Is(err, ErrNotConnected) {
		return fail("not_connected", "No browser with the Jarvis extension is connected on this computer.")
	}
	if err != nil {
		if m.Log != nil {
			m.Log.Warn("browser: match-tabs: listing tabs failed", "err", err)
		}
		return fail("unavailable", "The browser didn't answer.")
	}
	matches := []map[string]string{}
	seen := map[string]bool{}
	for _, t := range tabs {
		g := re.FindStringSubmatch(t.URL)
		if len(g) < 2 || g[1] == "" || seen[g[1]] {
			continue
		}
		seen[g[1]] = true
		matches = append(matches, map[string]string{"value": g[1], "title": t.Title})
		if len(matches) == maxMatches {
			break
		}
	}
	return map[string]any{"ok": true, "matches": matches}
}

func fail(code, msg string) map[string]any {
	return map[string]any{"ok": false, "error": map[string]string{"code": code, "message": msg}}
}

// HubTabs lists the open tabs of every browser connected to h; a browser that fails to answer
// is skipped while another answers.
func HubTabs(h *Hub) func(ctx context.Context) ([]Tab, error) {
	return func(ctx context.Context) ([]Tab, error) {
		bs := h.Browsers()
		if len(bs) == 0 {
			return nil, ErrNotConnected
		}
		var out []Tab
		var firstErr error
		answered := false
		for _, b := range bs {
			res, _, err := h.Call(ctx, b.ID, "list_tabs", map[string]any{})
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			answered = true
			list, _ := res["tabs"].([]any)
			for _, e := range list {
				t, _ := e.(map[string]any)
				u, _ := t["url"].(string)
				title, _ := t["title"].(string)
				if u != "" {
					out = append(out, Tab{URL: u, Title: title})
				}
			}
		}
		if !answered {
			return nil, firstErr
		}
		return out, nil
	}
}
