package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func matchReq(t *testing.T, m *MatchTabs, payload string) (string, map[string]any) {
	t.Helper()
	topic, body := m.Handle(context.Background(), "jarvis/nodes/n1/browser/match-tabs", []byte(payload))
	if topic == "" {
		return "", nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return topic, out
}

func TestMatchTabsAnswersOnlyCapturedValuesAndTitles(t *testing.T) {
	m := &MatchTabs{Tabs: func(context.Context) ([]Tab, error) {
		return []Tab{
			{URL: "https://www.walmart.com/ip/Great-Value-Milk/10450114?athbdg=L1600", Title: "Great Value Milk - Walmart.com"},
			{URL: "https://www.walmart.com/search?q=milk", Title: "milk - Walmart.com"},
			{URL: "https://mail.example/inbox/secret-thread", Title: "Inbox"},
			{URL: "https://www.walmart.com/ip/10450114", Title: "Same item again"},
		}, nil
	}}
	topic, out := matchReq(t, m, `{"correlation_id":"c1","pattern":"/ip/(?:[^/]+/)?(\\d{5,})"}`)
	if topic != "jarvis/nodes/n1/browser/match-tabs/response/c1" {
		t.Fatalf("topic %q", topic)
	}
	got, _ := json.Marshal(out)
	if string(got) != `{"matches":[{"title":"Great Value Milk - Walmart.com","value":"10450114"}],"ok":true}` {
		t.Fatalf("answer %s", got)
	}
	if strings.Contains(string(got), "http") {
		t.Fatalf("a tab address left the computer: %s", got)
	}
}

func TestMatchTabsErrors(t *testing.T) {
	none := &MatchTabs{Tabs: func(context.Context) ([]Tab, error) { return nil, ErrNotConnected }}
	broken := &MatchTabs{Tabs: func(context.Context) ([]Tab, error) { return nil, errors.New("pipe closed") }}
	for _, c := range []struct {
		m       *MatchTabs
		pattern string
		code    string
	}{
		{none, `x(\\d+)`, "not_connected"},
		{broken, `x(\\d+)`, "unavailable"},
		{none, `no group`, "bad_pattern"},
		{none, `(unclosed`, "bad_pattern"},
	} {
		_, out := matchReq(t, c.m, `{"correlation_id":"c1","pattern":"`+c.pattern+`"}`)
		e, _ := out["error"].(map[string]any)
		if out["ok"] != false || e["code"] != c.code {
			t.Errorf("%s: %v", c.pattern, out)
		}
	}
}

func TestMatchTabsIgnoresBadRequests(t *testing.T) {
	m := &MatchTabs{Tabs: func(context.Context) ([]Tab, error) { t.Fatal("listed tabs"); return nil, nil }}
	for _, p := range []string{`{"pattern":"(x)"}`, `{"correlation_id":"a/b","pattern":"(x)"}`, `not json`} {
		if topic, _ := matchReq(t, m, p); topic != "" {
			t.Errorf("%s answered on %s", p, topic)
		}
	}
	if topic, _ := m.Handle(context.Background(), "jarvis/nodes/n1/browser/other", []byte(`{"correlation_id":"c1"}`)); topic != "" {
		t.Errorf("another op answered")
	}
}
