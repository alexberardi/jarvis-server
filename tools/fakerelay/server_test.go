package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newServer(t *testing.T) (*Server, *httptest.Server, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	s := &Server{Log: &log}
	hs := httptest.NewServer(s)
	t.Cleanup(hs.Close)
	return s, hs, &log
}

func do(t *testing.T, method, url string, headers map[string]string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func register(t *testing.T, hs *httptest.Server, hh string) string {
	t.Helper()
	code, out := do(t, "POST", hs.URL+"/v1/register", nil, map[string]any{"household_id": hh})
	if code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	jwt, _ := out["jwt"].(string)
	if jwt == "" {
		t.Fatalf("register: no jwt in %v", out)
	}
	return jwt
}

func TestSendRecordsThePushAndAnswersPerToken(t *testing.T) {
	_, hs, log := newServer(t)
	jwt := register(t, hs, "hh-1")
	code, out := do(t, "POST", hs.URL+"/v1/send", map[string]string{"Authorization": "Bearer " + jwt, "X-Household-Id": "hh-1"},
		map[string]any{"tokens": []string{"ExponentPushToken[a]", "fail:gone"}, "title": "Timer done", "body": "Pasta",
			"data": map[string]any{"type": "alert", "inbox_item_id": "i-1"}, "priority": "high"})
	if code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
	res, _ := out["results"].([]any)
	if len(res) != 2 {
		t.Fatalf("results: %v", out)
	}
	ok, dead := res[0].(map[string]any), res[1].(map[string]any)
	if ok["status"] != "ok" || ok["token"] != "ExponentPushToken[a]" {
		t.Errorf("first result = %v, want ok", ok)
	}
	// A fail:* token stands for an uninstalled app, so jarvisd's pruning path runs.
	if dead["status"] != "error" || dead["error"] != "DeviceNotRegistered" || dead["token"] != "fail:gone" {
		t.Errorf("second result = %v, want DeviceNotRegistered", dead)
	}

	code, out = do(t, "GET", hs.URL+"/control/pushes", nil, nil)
	if code != 200 {
		t.Fatalf("pushes: %d", code)
	}
	ps, _ := out["pushes"].([]any)
	if len(ps) != 1 {
		t.Fatalf("pushes = %v, want 1", out)
	}
	p := ps[0].(map[string]any)
	if p["title"] != "Timer done" || p["body"] != "Pasta" || p["priority"] != "high" || p["household_id"] != "hh-1" {
		t.Errorf("recorded push = %v", p)
	}
	if d, _ := p["data"].(map[string]any); d["inbox_item_id"] != "i-1" {
		t.Errorf("recorded data = %v", p["data"])
	}
	if toks, _ := p["tokens"].([]any); len(toks) != 2 {
		t.Errorf("recorded tokens = %v", p["tokens"])
	}
	if !strings.Contains(log.String(), `"title":"Timer done"`) {
		t.Errorf("log = %q, want the push as a JSON line", log.String())
	}
}

func TestSendNeedsTheHouseholdsJWT(t *testing.T) {
	_, hs, _ := newServer(t)
	jwt := register(t, hs, "hh-1")
	body := map[string]any{"tokens": []string{"t"}, "title": "x", "body": "y", "data": map[string]any{}, "priority": "default"}
	for name, h := range map[string]map[string]string{
		"no auth":         {"X-Household-Id": "hh-1"},
		"stale jwt":       {"Authorization": "Bearer nope", "X-Household-Id": "hh-1"},
		"other household": {"Authorization": "Bearer " + jwt, "X-Household-Id": "hh-2"},
	} {
		if code, _ := do(t, "POST", hs.URL+"/v1/send", h, body); code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, code)
		}
	}
	_, out := do(t, "GET", hs.URL+"/control/pushes", nil, nil)
	if ps, _ := out["pushes"].([]any); len(ps) != 0 {
		t.Errorf("refused sends were recorded: %v", ps)
	}
}

func TestRegisterNeedsAHousehold(t *testing.T) {
	_, hs, _ := newServer(t)
	if code, _ := do(t, "POST", hs.URL+"/v1/register", nil, map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("register without household_id: %d, want 400", code)
	}
}

func TestControlFiltersAndClears(t *testing.T) {
	_, hs, _ := newServer(t)
	jwt := register(t, hs, "hh-1")
	h := map[string]string{"Authorization": "Bearer " + jwt, "X-Household-Id": "hh-1"}
	for _, tc := range []struct{ tok, title string }{{"ExponentPushToken[a]", "One"}, {"ExponentPushToken[b]", "Two"}} {
		do(t, "POST", hs.URL+"/v1/send", h, map[string]any{"tokens": []string{tc.tok}, "title": tc.title, "body": "", "priority": "default"})
	}
	_, out := do(t, "GET", hs.URL+"/control/pushes?token=ExponentPushToken%5Bb%5D", nil, nil)
	if ps, _ := out["pushes"].([]any); len(ps) != 1 || ps[0].(map[string]any)["title"] != "Two" {
		t.Errorf("?token= filter: %v", out)
	}
	_, out = do(t, "GET", hs.URL+"/control/pushes?household_id=hh-2", nil, nil)
	if ps, _ := out["pushes"].([]any); len(ps) != 0 {
		t.Errorf("?household_id= filter: %v", out)
	}
	if code, _ := do(t, "DELETE", hs.URL+"/control/pushes", nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	_, out = do(t, "GET", hs.URL+"/control/pushes", nil, nil)
	if ps, _ := out["pushes"].([]any); len(ps) != 0 {
		t.Errorf("after DELETE: %v", out)
	}
}

func TestControlCanFailTheRelay(t *testing.T) {
	_, hs, _ := newServer(t)
	jwt := register(t, hs, "hh-1")
	if code, _ := do(t, "POST", hs.URL+"/control/fail", nil, map[string]any{"status": 503, "count": 1}); code != 200 {
		t.Fatalf("control/fail: %d", code)
	}
	h := map[string]string{"Authorization": "Bearer " + jwt, "X-Household-Id": "hh-1"}
	body := map[string]any{"tokens": []string{"t"}, "title": "x", "body": "", "priority": "default"}
	if code, _ := do(t, "POST", hs.URL+"/v1/send", h, body); code != 503 {
		t.Errorf("first send after control/fail: %d, want 503", code)
	}
	if code, _ := do(t, "POST", hs.URL+"/v1/send", h, body); code != 200 {
		t.Errorf("second send: %d, want 200 (count 1)", code)
	}
	_, out := do(t, "GET", hs.URL+"/control/pushes", nil, nil)
	if ps, _ := out["pushes"].([]any); len(ps) != 1 {
		t.Errorf("only the delivered send is recorded: %v", out)
	}
}
