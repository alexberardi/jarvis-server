package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/logging"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

type fakeAuth struct{}

func (fakeAuth) ValidateApp(_ context.Context, id, key string) (authn.App, bool, error) {
	return authn.App{ID: id}, id == "jarvis-cc" && key == "k", nil
}

func (fakeAuth) ValidateNode(_ context.Context, id, key, svc string) (authn.NodeValidation, error) {
	switch {
	case id != "n1" && id != "n2":
		return authn.NodeValidation{Reason: "Node not found"}, nil
	case key != "nk":
		return authn.NodeValidation{Reason: "Invalid node credentials"}, nil
	case id == "n2":
		return authn.NodeValidation{Reason: "Node is not authorized to access service '" + svc + "'"}, nil
	}
	return authn.NodeValidation{Valid: true, Node: authn.Node{ID: id, HouseholdID: "hh"}}, nil
}

func (fakeAuth) HouseholdRole(context.Context, int64, string) (authn.Role, bool, error) {
	return "", false, nil
}

var appH = []string{"X-Jarvis-App-Id", "jarvis-cc", "X-Jarvis-App-Key", "k"}

func setup(t *testing.T) (*Module, http.Handler) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "logs", Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &Module{Auth: fakeAuth{}}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log, Queue: queue.New(d, log)})
	if err := m.settings.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return m, mux
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr ...string) (int, string) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func detail(t *testing.T, body string) any {
	t.Helper()
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	return m["detail"]
}

func query(t *testing.T, h http.Handler, q string) []map[string]any {
	t.Helper()
	c, body := do(t, h, "GET", "/api/v0/logs"+q, "", appH...)
	if c != 200 {
		t.Fatalf("query %s: %d %s", q, c, body)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIngestAndQuery(t *testing.T) {
	_, h := setup(t)
	batch := `{"logs":[
		{"service":"cc","level":"INFO","message":"node registered","context":{"node_id":"n9"}},
		{"service":"cc","level":"ERROR","message":"llm timeout"},
		{"service":"tts","level":"DEBUG","message":"rendered 2.1s"}]}`
	if c, b := do(t, h, "POST", "/api/v0/logs/batch", batch, appH...); c != 204 || b != "" {
		t.Fatalf("batch: %d %q", c, b)
	}
	if c, _ := do(t, h, "POST", "/api/v0/logs", `{"service":"cc","level":"WARNING","message":"slow"}`, appH...); c != 204 {
		t.Fatalf("single: %d", c)
	}

	all := query(t, h, "")
	if len(all) != 4 || all[0]["message"] != "slow" { // newest first
		t.Fatalf("all: %v", all)
	}
	if got := query(t, h, "?service=cc&level=ERROR"); len(got) != 1 || got[0]["message"] != "llm timeout" || got[0]["context"] != nil {
		t.Fatalf("filtered: %v", got)
	}
	// LogQL |~ semantics: an RE2 regexp over "message | context".
	if got := query(t, h, "?search=n9"); len(got) != 1 || got[0]["context"].(map[string]any)["node_id"] != "n9" {
		t.Fatalf("search in context: %v", got)
	}
	if got := query(t, h, "?search=time(out|r)"); len(got) != 1 {
		t.Fatalf("regexp search: %v", got)
	}
	if got := query(t, h, "?search=("); len(got) != 0 {
		t.Fatalf("bad regexp should give []: %v", got)
	}
	if got := query(t, h, "?limit=2"); len(got) != 2 {
		t.Fatalf("limit: %v", got)
	}
	ts := all[0]["timestamp"].(string)
	if strings.HasSuffix(ts, "Z") || !strings.Contains(ts, "T") {
		t.Fatalf("timestamp %q: want naive ISO like the legacy service", ts)
	}

	c, body := do(t, h, "GET", "/api/v0/services", "", appH...)
	if c != 200 || body != "[\"cc\",\"tts\"]\n" {
		t.Fatalf("services: %d %s", c, body)
	}
}

func TestTimeWindow(t *testing.T) {
	_, h := setup(t)
	old := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano)
	do(t, h, "POST", "/api/v0/logs", `{"service":"cc","level":"INFO","message":"old","timestamp":"`+old+`"}`, appH...)
	do(t, h, "POST", "/api/v0/logs", `{"service":"cc","level":"INFO","message":"naive","timestamp":"2020-01-01T00:00:00"}`, appH...)
	if got := query(t, h, ""); len(got) != 0 {
		t.Fatalf("default window is the last hour: %v", got)
	}
	if got := query(t, h, "?since="+time.Now().Add(-4*time.Hour).UTC().Format("2006-01-02T15:04:05")); len(got) != 1 {
		t.Fatalf("since: %v", got)
	}
	if got := query(t, h, "?since=2019-12-31T00:00:00&until=2020-01-02T00:00:00"); len(got) != 1 || got[0]["timestamp"] != "2020-01-01T00:00:00" {
		t.Fatalf("naive timestamps are UTC: %v", got)
	}
}

func TestValidation(t *testing.T) {
	_, h := setup(t)
	c, body := do(t, h, "POST", "/api/v0/logs/batch", `{"logs":[{"service":"cc","level":"TRACE","message":"x"}]}`, appH...)
	d, _ := detail(t, body).([]any)
	if c != 422 || len(d) != 1 {
		t.Fatalf("%d %s", c, body)
	}
	loc := d[0].(map[string]any)["loc"].([]any)
	if len(loc) != 4 || loc[0] != "body" || loc[1] != "logs" || loc[2] != float64(0) || loc[3] != "level" {
		t.Fatalf("loc %v", loc)
	}
	if c, _ := do(t, h, "POST", "/api/v0/logs", `{"service":"","level":"INFO"}`, appH...); c != 422 {
		t.Fatalf("missing message / empty service: %d", c)
	}
	for _, q := range []string{"?limit=0", "?limit=1001", "?limit=x", "?since=yesterday"} {
		if c, _ := do(t, h, "GET", "/api/v0/logs"+q, "", appH...); c != 422 {
			t.Errorf("%s: %d", q, c)
		}
	}
	if c, _ := do(t, h, "POST", "/api/v0/logs/batch", `{"logs":[]}`, appH...); c != 204 {
		t.Fatalf("empty batch: %d", c)
	}
}

func TestAppAuth(t *testing.T) {
	_, h := setup(t)
	if c, b := do(t, h, "POST", "/api/v0/logs/batch", `{"logs":[]}`); c != 401 || detail(t, b) != "Missing app credentials" {
		t.Fatalf("%d %s", c, b)
	}
	if c, b := do(t, h, "GET", "/api/v0/logs", "", "X-Jarvis-App-Id", "jarvis-cc", "X-Jarvis-App-Key", "bad"); c != 401 || detail(t, b) != "Invalid app credentials" {
		t.Fatalf("%d %s", c, b)
	}
}

func TestNodeIngest(t *testing.T) {
	_, h := setup(t)
	node := []string{"X-Node-Id", "n1", "X-Node-Key", "nk"}
	if c, _ := do(t, h, "POST", "/api/v0/node/logs/batch", `{"logs":[{"service":"node","level":"INFO","message":"boot","context":{"a":1}}]}`, node...); c != 204 {
		t.Fatalf("node batch: %d", c)
	}
	got := query(t, h, "?service=node")
	c := got[0]["context"].(map[string]any)
	if c["node_id"] != "n1" || c["a"] != float64(1) {
		t.Fatalf("node context %v", c)
	}
	if v, present := c["user_id"]; !present || v != nil {
		t.Fatalf("user_id should be present and null (legacy shape): %v", c)
	}
	for _, tc := range []struct {
		hdr    []string
		status int
		detail string
	}{
		{nil, 401, "Missing node credentials"},
		{[]string{"X-Node-Id", "n1", "X-Node-Key", "wrong"}, 403, "Invalid node credentials"},
		{[]string{"X-Node-Id", "zz", "X-Node-Key", "nk"}, 403, "Node not found"},
		{[]string{"X-Node-Id", "n2", "X-Node-Key", "nk"}, 403, "Node is not authorized to access service 'jarvis-logs'"},
	} {
		c, b := do(t, h, "POST", "/api/v0/node/logs", `{"service":"node","level":"INFO","message":"x"}`, tc.hdr...)
		if c != tc.status || detail(t, b) != tc.detail {
			t.Errorf("%v: %d %s", tc.hdr, c, b)
		}
	}
}

func TestHealthAndPing(t *testing.T) {
	_, h := setup(t)
	c, b := do(t, h, "GET", "/health", "")
	var m map[string]any
	json.Unmarshal([]byte(b), &m)
	if c != 200 || m["status"] != "healthy" || m["services"].(map[string]any)["loki"] != "available" {
		t.Fatalf("%d %s", c, b)
	}
	if c, b := do(t, h, "GET", "/ping", ""); c != 200 || !strings.Contains(b, `"pong"`) {
		t.Fatalf("%d %s", c, b)
	}
}

func TestStreamSendsEachEntryOnce(t *testing.T) {
	m, h := setup(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v0/logs/stream?service=cc", nil)
	req.Header.Set("X-Jarvis-App-Id", "jarvis-cc")
	req.Header.Set("X-Jarvis-App-Key", "k")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type %q", res.Header.Get("Content-Type"))
	}
	do(t, h, "POST", "/api/v0/logs/batch", `{"logs":[{"service":"cc","level":"INFO","message":"one"},{"service":"tts","level":"INFO","message":"other"},{"service":"cc","level":"INFO","message":"two"}]}`, appH...)

	sc := bufio.NewScanner(res.Body)
	var got []string
	deadline := time.Now().Add(3 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) && sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			var l map[string]any
			json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &l)
			got = append(got, l["message"].(string))
		}
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("streamed %v", got)
	}
	// Nothing new: the next poll must not resend "two" (the legacy bug).
	done := make(chan string, 1)
	go func() {
		if sc.Scan() {
			done <- sc.Text()
		}
	}()
	select {
	case line := <-done:
		if strings.HasPrefix(line, "data: ") {
			t.Fatalf("re-sent an entry: %s", line)
		}
	case <-time.After(1500 * time.Millisecond):
	}
	_ = m
}

func TestSinkAndPurge(t *testing.T) {
	m, h := setup(t)
	ctx := context.Background()
	err := m.Sink().Write(ctx, []logging.Record{
		{Time: time.Now(), Level: slog.LevelError, Message: "engine crashed", Source: "engines", Attrs: map[string]any{"engine": "llm-live"}},
		{Time: time.Now().Add(-10 * 24 * time.Hour), Level: slog.LevelInfo, Message: "ancient", Source: "jarvisd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := query(t, h, "?service=engines&level=ERROR"); len(got) != 1 || got[0]["context"].(map[string]any)["engine"] != "llm-live" {
		t.Fatalf("sink: %v", got)
	}
	if _, err := m.purge(ctx, queue.Job{}); err != nil {
		t.Fatal(err)
	}
	var n int
	m.deps.DB.Read.QueryRow(`SELECT count(*) FROM logs_entries`).Scan(&n)
	if n != 1 {
		t.Fatalf("after 7-day purge: %d rows", n)
	}
}
