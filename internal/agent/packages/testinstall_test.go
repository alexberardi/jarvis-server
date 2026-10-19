package packages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// pantry serves Forge drafts at /v1/forge/drafts/{code}, unauthenticated as Pantry does.
func pantry(t *testing.T, drafts map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
			t.Errorf("the draft download sent credentials")
		}
		d, ok := drafts[strings.TrimPrefix(r.URL.Path, "/v1/forge/drafts/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(d)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The test-install topic (N:services/test_install_handler.py): verify with jarvisd, download
// the Forge draft from Pantry, serve it as a temporary test command, post the result; it goes
// after 20 minutes.
func TestRequestsTestInstall(t *testing.T) {
	r := newRepos(t)
	m, fr, sc := newTestManager(t, r)
	cmd := "from jarvis_command_sdk import IJarvisCommand\nclass Hello(IJarvisCommand): ...\n"
	p := pantry(t, map[string]any{
		"ABC123": map[string]any{"package_name": "hello", "files": []map[string]string{
			{"filename": "command.py", "content": cmd},
			{"filename": "helpers/util.py", "content": "X = 1\n"},
			{"filename": "requirements.txt", "content": "httpx>=0.27\n# a comment\n\n--index-url http://evil\n"},
		}},
		"EMPTY1": map[string]any{"package_name": "empty", "files": []any{}},
		"ESCAPE": map[string]any{"package_name": "esc", "files": []map[string]string{{"filename": "../../x.py", "content": cmd}}},
	})
	m.HTTP = p.Client()
	api := &fakeAPI{results: map[string]map[string]any{}, tests: map[string]map[string]any{
		"t1": {"confirmed": true, "package_name": "hello", "pantry_download_url": p.URL + "/v1/forge/drafts/ABC123"},
		"t2": {"confirmed": true, "package_name": "empty", "pantry_download_url": p.URL + "/v1/forge/drafts/EMPTY1"},
		"t3": {"confirmed": true, "package_name": "esc", "pantry_download_url": p.URL + "/v1/forge/drafts/ESCAPE"},
		"t4": {"confirmed": false},
		"t5": {"confirmed": true, "package_name": "gone", "pantry_download_url": p.URL + "/v1/forge/drafts/NOPE00"},
	}}
	q := &Requests{NodeID: "n1", Manager: m, Log: quiet}
	ctx := context.Background()
	for _, rid := range []string{"t1", "t2", "t3", "t4", "t5"} {
		q.Handle(ctx, api, TopicTestInstall, []byte(`{"request_id":"`+rid+`"}`))
	}
	want := map[string]map[string]any{
		"t1": {"success": true, "details": map[string]any{"package_name": "hello", "files_installed": 3.0, "pip_packages_added": 1.0}},
		"t2": {"success": false, "error": "No files in draft"},
		"t3": {"success": false, "error": `test install: path "../../x.py" is not inside the package`},
		"t4": {"success": false, "error": "Verification failed"},
		"t5": {"success": false, "error": "Failed to download from Pantry"},
	}
	for rid, w := range want {
		got, _ := json.Marshal(api.results["/api/v0/nodes/n1/test-install/"+rid+"/results"])
		wb, _ := json.Marshal(w)
		if string(got) != string(wb) {
			t.Errorf("%s:\n got %s\nwant %s", rid, got, wb)
		}
	}
	if !strings.Contains(strings.Join(api.calls, "\n"), "GET /api/v0/nodes/n1/test-install/t1/verify") {
		t.Fatalf("calls = %v", api.calls)
	}
	// Only the requirement, never uv options.
	if pip := strings.Join(fr.calls[len(fr.calls)-1], " "); !strings.HasSuffix(pip, " httpx>=0.27") {
		t.Fatalf("pip = %s", pip)
	}
	// It is served next to the installed packages, but isn't one.
	dir := filepath.Join(m.Root, "tests", "hello")
	list, _ := readList(filepath.Join(m.Root, "commands.json"))
	if len(list) != 1 || list[0].Name != "test:hello" || list[0].Root != dir || !reflect.DeepEqual(list[0].Commands, []string{"command.py"}) {
		t.Fatalf("commands.json = %+v", list)
	}
	if got := sc.livePackages(); !reflect.DeepEqual(got, []string{"test:hello"}) || len(m.Report()) != 0 {
		t.Fatalf("in service %v, report %v", got, m.Report())
	}

	// 20 minutes later it is gone, and the sidecar restarts without it.
	m.now = func() time.Time { return time.Now().Add(21 * time.Minute) }
	m.ExpireTests(ctx)
	if exists(dir) || len(sc.livePackages()) != 0 {
		t.Fatalf("still there: %v", sc.livePackages())
	}
}

// A draft whose command doesn't load is not kept.
func TestTestInstallThatDoesNotLoad(t *testing.T) {
	r := newRepos(t)
	m, _, sc := newTestManager(t, r)
	sc.fail = map[string]string{"test:hello": "command.py: SyntaxError"}
	_, err := m.TestInstall(context.Background(), "hello", []DraftFile{{Filename: "command.py", Content: "class X(IJarvisCommand"}})
	if err == nil || !strings.Contains(err.Error(), "SyntaxError") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "tests", "hello")); !os.IsNotExist(err) {
		t.Fatalf("kept: %v", err)
	}
}
