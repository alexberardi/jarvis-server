package packages

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeAPI is jarvisd's side of a package request: the verify answer and the posted results.
type fakeAPI struct {
	mu      sync.Mutex
	verify  map[string]verifyAnswer   // by request id
	tests   map[string]map[string]any // test-install verify answers, by request id
	calls   []string
	results map[string]map[string]any // path -> body
}

func (f *fakeAPI) Do(_ context.Context, method, path string, body, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method+" "+path)
	switch {
	case method == "GET" && strings.HasSuffix(path, "/verify"):
		rid := strings.TrimSuffix(path[strings.LastIndex(path[:len(path)-len("/verify")], "/")+1:], "/verify")
		var v any
		var ok bool
		if strings.Contains(path, "/test-install/") {
			v, ok = f.tests[rid]
		} else {
			v, ok = f.verify[rid]
		}
		if !ok {
			return errors.New("404 Package install request not found")
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	case method == "POST" && strings.HasSuffix(path, "/results"):
		b, _ := json.Marshal(body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		f.results[path] = m
		return nil
	}
	return errors.New("unexpected " + method + " " + path)
}

func TestRequestsInstallUninstallRevert(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/weather", "HEAD", weather("1.0.0"))
	tag := "v2"
	r.add(t, "o/weather", tag, weather("2.0.0"))
	m, _, _ := newTestManager(t, r)
	api := &fakeAPI{results: map[string]map[string]any{}, verify: map[string]verifyAnswer{
		"r1": {Confirmed: true, CommandName: "get_weather_meteo", GithubRepoURL: repo},
		"r2": {Confirmed: true, CommandName: "get_weather_meteo", GithubRepoURL: repo, GitTag: &tag},
		"r3": {Confirmed: true, CommandName: "get_weather_meteo"},
		"r4": {Confirmed: true, CommandName: "get_weather_meteo"},
		"r5": {Confirmed: true, CommandName: "get_weather_meteo"},
		"r6": {Confirmed: true, CommandName: "get_weather_meteo"}, // install with no repo
	}}
	q := &Requests{NodeID: "n1", Manager: m, Log: quiet}
	ctx := context.Background()
	nudge := func(topic, rid string) {
		q.Handle(ctx, api, topic, []byte(`{"request_id":"`+rid+`","command_name":"spoofed"}`))
	}

	nudge(TopicInstall, "r1")
	nudge(TopicInstall, "r2")
	nudge(TopicRevert, "r3")
	nudge(TopicUninstall, "r4")
	nudge(TopicUninstall, "r5")
	nudge(TopicInstall, "r6")

	want := map[string]map[string]any{
		"/api/v0/nodes/n1/package-install/r1/results": {"success": true, "details": map[string]any{"package_name": "get_weather_meteo",
			"version": "1.0.0", "components": 2.0, "skipped_components": []any{"agent:weather_context"}}},
		"/api/v0/nodes/n1/package-install/r2/results": {"success": true, "details": map[string]any{"package_name": "get_weather_meteo",
			"version": "2.0.0", "components": 2.0, "skipped_components": []any{"agent:weather_context"}}},
		"/api/v0/nodes/n1/package-revert/r3/results":    {"success": true, "details": map[string]any{"package_name": "get_weather_meteo", "version": "1.0.0"}},
		"/api/v0/nodes/n1/package-uninstall/r4/results": {"success": true, "details": map[string]any{"package_name": "get_weather_meteo"}},
		"/api/v0/nodes/n1/package-uninstall/r5/results": {"success": false, "error": "get_weather_meteo isn't installed on this computer"},
		"/api/v0/nodes/n1/package-install/r6/results":   {"success": false, "error": "No repo URL in verify response"},
	}
	for path, w := range want {
		got, _ := json.Marshal(api.results[path])
		wb, _ := json.Marshal(w)
		if string(got) != string(wb) {
			t.Errorf("%s:\n got %s\nwant %s", path, got, wb)
		}
	}
	// Every operation verified through the install route (one request table), as a Pi does.
	if !strings.Contains(strings.Join(api.calls, "\n"), "GET /api/v0/nodes/n1/package-install/r4/verify") {
		t.Fatalf("calls = %v", api.calls)
	}
}

// A nudge jarvisd didn't issue, a repeated one or a malformed one does nothing.
func TestRequestsIgnoreWhatJarvisdDidNotIssue(t *testing.T) {
	r := newRepos(t)
	repo := r.add(t, "o/weather", "HEAD", weather("1.0.0"))
	m, fr, _ := newTestManager(t, r)
	api := &fakeAPI{results: map[string]map[string]any{}, verify: map[string]verifyAnswer{
		"unconfirmed": {Confirmed: false, GithubRepoURL: repo},
		"once":        {Confirmed: true, CommandName: "get_weather_meteo", GithubRepoURL: repo},
	}}
	q := &Requests{NodeID: "n1", Manager: m, Log: quiet}
	ctx := context.Background()
	for _, p := range []string{`{"request_id":"spoofed"}`, `{"request_id":"unconfirmed"}`, `not json`, `{}`,
		`{"request_id":"` + strings.Repeat("x", 65) + `"}`} {
		q.Handle(ctx, api, TopicInstall, []byte(p))
	}
	if len(api.results) != 0 || len(fr.calls) != 0 {
		t.Fatalf("results %v, uv %v", api.results, fr.calls)
	}
	q.Handle(ctx, api, TopicInstall, []byte(`{"request_id":"once"}`))
	q.Handle(ctx, api, TopicInstall, []byte(`{"request_id":"once"}`)) // QoS 1 redelivery
	if len(api.results) != 1 || len(fr.calls) != 2 {
		t.Fatalf("results %v, uv %v", api.results, fr.calls)
	}
}
