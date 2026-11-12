package packages

import (
	"errors"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"

	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
			{"filename": "jarvis_command.yaml", "content": "name: hello\npackages:\n  - name: websockets\n    version: \">=10.0\"\n"},
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
		"t1": {"success": true, "details": map[string]any{"agents": []any{}, "commands": []any{"hello_cmd"}, "files_installed": 4.0, "managers": []any{}, "package_name": "hello", "pip_packages_added": 2.0, "protocols": []any{}}},
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
	// And into the test's own folder, not the shared venv.
	if pip := strings.Join(fr.calls[len(fr.calls)-1], " "); !strings.HasSuffix(pip, " --target "+filepath.Join(m.Root, "tests", "hello", ".deps")+" websockets>=10.0 httpx>=0.27") {
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

// A Forge draft that is a device protocol (a Zigbee2MQTT bridge, say) has no commands: its
// family is served for the test window like a test command, so the node can scan and control.
func TestTestInstallDeviceProtocol(t *testing.T) {
	m, _, sc := newTestManager(t, newRepos(t))
	proto := "from jarvis_command_sdk import IJarvisDeviceProtocol\nclass Z2M(IJarvisDeviceProtocol): ...\n"
	files := []DraftFile{{Filename: "device_families/zigbee2mqtt/protocol.py", Content: proto}}
	details, err := m.TestInstall(context.Background(), "zigbee2mqtt", "r1", files)
	if err != nil {
		t.Fatal(err)
	}
	if got := details["protocols"]; !reflect.DeepEqual(got, []string{"zigbee2mqtt"}) {
		t.Fatalf("protocols = %v", got)
	}
	list, _ := readList(filepath.Join(m.Root, "commands.json"))
	if len(list) != 1 || list[0].Name != "test:zigbee2mqtt" || len(list[0].Commands) != 0 ||
		!reflect.DeepEqual(list[0].Families, []string{"device_families/zigbee2mqtt/protocol.py"}) {
		t.Fatalf("commands.json = %+v", list)
	}
	if got := sc.livePackages(); !reflect.DeepEqual(got, []string{"test:zigbee2mqtt"}) {
		t.Fatalf("in service %v", got)
	}

	// Nothing a node loads at all is still refused.
	_, err = m.TestInstall(context.Background(), "notes", "r2", []DraftFile{{Filename: "README.md", Content: "hi"}})
	if err == nil || err.Error() != "notes has no commands, device protocols or agents" {
		t.Fatalf("err = %v", err)
	}
}

// A Forge draft that is a background agent has no commands either: it is listed for the sidecar
// on any node (a desktop runs no installed package's agents), and the install reports it.
func TestTestInstallAgent(t *testing.T) {
	m, _, sc := newTestManager(t, newRepos(t))
	agent := "from jarvis_command_sdk import IJarvisAgent\nclass Watch(IJarvisAgent): ...\n"
	files := []DraftFile{{Filename: "agents/zigbee_watch/agent.py", Content: agent}}
	details, err := m.TestInstall(context.Background(), "zigbee-watch", "r1", files)
	if err != nil {
		t.Fatal(err)
	}
	if got := details["agents"]; !reflect.DeepEqual(got, []string{"zigbee_watch"}) {
		t.Fatalf("agents = %v", got)
	}
	list, _ := readList(filepath.Join(m.Root, "commands.json"))
	if len(list) != 1 || list[0].Name != "test:zigbee-watch" ||
		!reflect.DeepEqual(list[0].Agents, []string{"agents/zigbee_watch/agent.py"}) {
		t.Fatalf("commands.json = %+v", list)
	}
	if got := sc.livePackages(); !reflect.DeepEqual(got, []string{"test:zigbee-watch"}) {
		t.Fatalf("in service %v", got)
	}
}

// A draft whose command doesn't load is not kept.
func TestTestInstallThatDoesNotLoad(t *testing.T) {
	r := newRepos(t)
	m, _, sc := newTestManager(t, r)
	sc.fail = map[string]string{"test:hello": "command.py: SyntaxError"}
	_, err := m.TestInstall(context.Background(), "hello", "r3", []DraftFile{{Filename: "command.py", Content: "class X(IJarvisCommand"}})
	if err == nil || !strings.Contains(err.Error(), "SyntaxError") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "tests", "hello")); !os.IsNotExist(err) {
		t.Fatalf("kept: %v", err)
	}
}

// A test install's events (logs, scans, controls the sidecar recorded) go to jarvisd under its
// request, each once; a new sidecar's events, counted from 0 again, are read from the start.
func TestShipTestEvents(t *testing.T) {
	m, _, _ := newTestManager(t, newRepos(t))
	proto := "from jarvis_command_sdk import IJarvisDeviceProtocol\nclass Z(IJarvisDeviceProtocol): ...\n"
	if _, err := m.TestInstall(context.Background(), "zig", "rid-1", []DraftFile{{Filename: "device_families/zig/protocol.py", Content: proto}}); err != nil {
		t.Fatal(err)
	}
	queue := []map[string]any{
		{"events": []any{map[string]any{"seq": 1.0}, map[string]any{"seq": 2.0}}, "last": 2.0, "head": 2.0},
		{"events": []any{}, "last": 2.0, "head": 2.0},
		{"events": []any{}, "last": 2.0, "head": 1.0}, // a new sidecar
		{"events": []any{map[string]any{"seq": 1.0}}, "last": 1.0, "head": 1.0},
	}
	var asked []any
	src := func(method string, params map[string]any) (map[string]any, error) {
		if method != "test_events" || params["package"] != "test:zig" {
			t.Fatalf("asked %s %v", method, params)
		}
		asked = append(asked, params["after"])
		res := queue[0]
		queue = queue[1:]
		return res, nil
	}
	api := &fakeAPI{results: map[string]map[string]any{}}
	q := &Requests{NodeID: "n1", Manager: m, Log: quiet}
	sent := map[string]int{}
	for range 3 {
		q.shipTestEventsOnce(context.Background(), api, src, sent)
	}
	if !reflect.DeepEqual(asked, []any{0, 2, 2, 0}) {
		t.Fatalf("after = %v", asked)
	}
	var posts []string
	for _, c := range api.calls {
		if strings.HasPrefix(c, "POST ") {
			posts = append(posts, c)
		}
	}
	if !reflect.DeepEqual(posts, []string{"POST /api/v0/nodes/n1/test-install/rid-1/events", "POST /api/v0/nodes/n1/test-install/rid-1/events"}) {
		t.Fatalf("posts = %v", posts)
	}
}

// fakeSecrets records the secrets forgotten and the command data cleared.
type fakeSecrets struct{ forgot, cleared []string }

func (f *fakeSecrets) ForgetSecret(key, scope string) error {
	f.forgot = append(f.forgot, scope+"/"+key)
	return nil
}

func (f *fakeSecrets) DeleteAllData(command string) (int, error) {
	f.cleared = append(f.cleared, command)
	return 1, nil
}

// A test install's settings go with it (user 2026-11-10): when it expires, the secrets it
// declared are forgotten, except a key a package still loaded declares too. A reinstall of the
// same draft keeps them, so a Forge fix doesn't ask for the MQTT broker again.
func TestTestInstallSecretsGoWithIt(t *testing.T) {
	m, _, sc := newTestManager(t, newRepos(t))
	now := time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	fs := &fakeSecrets{}
	m.Secrets = fs
	host := sidecar.SecretKey{Key: "MQTT_HOST", Scope: "integration"}
	sc.secrets = map[string][]sidecar.SecretKey{
		"test:z2m": {host, {Key: "Z2M_BASE_TOPIC", Scope: "integration"}},
		"test:hue": {host},
	}
	proto := func(name string) []DraftFile {
		return []DraftFile{{Filename: "device_families/" + name + "/protocol.py",
			Content: "from jarvis_command_sdk import IJarvisDeviceProtocol\nclass P(IJarvisDeviceProtocol): ...\n"}}
	}
	ctx := context.Background()
	install := func(name string) {
		t.Helper()
		if _, err := m.TestInstall(ctx, name, "r-"+name, proto(name)); err != nil {
			t.Fatal(err)
		}
	}
	install("z2m")
	install("z2m") // Reinstall after a fix
	now = now.Add(10 * time.Minute)
	install("hue")
	if len(fs.forgot) != 0 {
		t.Fatalf("forgot %v before anything expired", fs.forgot)
	}

	now = now.Add(15 * time.Minute) // z2m's 20 minutes are up, hue's aren't
	m.ExpireTests(ctx)
	if want := []string{"integration/Z2M_BASE_TOPIC"}; !reflect.DeepEqual(fs.forgot, want) {
		t.Fatalf("forgot %v, want %v (hue still declares MQTT_HOST)", fs.forgot, want)
	}

	now = now.Add(time.Hour)
	m.ExpireTests(ctx)
	if want := []string{"integration/Z2M_BASE_TOPIC", "integration/MQTT_HOST"}; !reflect.DeepEqual(fs.forgot, want) {
		t.Fatalf("forgot %v, want %v", fs.forgot, want)
	}
}

// A test install's saved data goes with it (user 2026-11-10: nothing it brought may stay
// behind). When it expires, the JarvisStorage records its commands and agents own are cleared,
// except a name an installed package's agent owns.
func TestTestInstallSavedDataGoesWithIt(t *testing.T) {
	m, _, _ := newTestManager(t, newRepos(t))
	now := time.Date(2026, 11, 11, 21, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	fs := &fakeSecrets{}
	m.Secrets = fs
	// An installed package with an agents/digest: its data stays, whatever a draft calls its own.
	if err := os.MkdirAll(m.dir("news"), 0o755); err != nil {
		t.Fatal(err)
	}
	man := "name: news\ncomponents:\n  - type: agent\n    name: digest\n"
	if err := os.WriteFile(filepath.Join(m.dir("news"), ManifestFile), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	draft := []DraftFile{
		{Filename: "commands/z2m.py", Content: "from jarvis_command_sdk import IJarvisCommand\nclass C(IJarvisCommand): ...\n"},
		{Filename: "agents/digest/agent.py", Content: "from jarvis_command_sdk import IJarvisAgent\nclass A(IJarvisAgent): ...\n"},
		{Filename: "agents/sales/agent.py", Content: "from jarvis_command_sdk import IJarvisAgent\nclass A(IJarvisAgent): ...\n"},
	}
	ctx := context.Background()
	if _, err := m.TestInstall(ctx, "z2m", "r-1", draft); err != nil {
		t.Fatal(err)
	}
	if len(fs.cleared) != 0 {
		t.Fatalf("cleared %v while the test was still being served", fs.cleared)
	}

	now = now.Add(21 * time.Minute)
	m.ExpireTests(ctx)
	// z2m_cmd is the command the fake sidecar loaded for test:z2m; digest is the installed one's.
	cleared := slices.Clone(fs.cleared)
	slices.Sort(cleared)
	if want := []string{"sales", "z2m_cmd"}; !reflect.DeepEqual(cleared, want) {
		t.Fatalf("cleared %v, want %v (news owns digest)", cleared, want)
	}
}

// A draft whose declared Python packages don't install is refused, not served without them.
func TestTestInstallPipFailure(t *testing.T) {
	r := newRepos(t)
	m, fr, _ := newTestManager(t, r)
	fr.pipFail = errors.New("exit status 1")
	_, err := m.TestInstall(context.Background(), "hello", "r9", []DraftFile{
		{Filename: "command.py", Content: "class X(IJarvisCommand): ...\n"},
		{Filename: "jarvis_command.yaml", Content: "name: hello\npackages:\n  - name: nosuchpkg\n"},
	})
	if err == nil || !strings.Contains(err.Error(), "nosuchpkg") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "tests", "hello")); !os.IsNotExist(err) {
		t.Fatalf("kept: %v", err)
	}
}

// The venv's version of a package wins over a test install's own copy, and the result says so.
func TestShadowed(t *testing.T) {
	deps, site := t.TempDir(), t.TempDir()
	for _, d := range []string{deps + "/websockets-13.1.dist-info", deps + "/idna-3.7.dist-info", deps + "/new_pkg-1.0.dist-info",
		site + "/websockets-12.0.dist-info", site + "/idna-3.7.dist-info"} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"websockets: the draft asked for 13.1, the node already has 12.0 and uses that"}
	if got := shadowed(deps, site); !reflect.DeepEqual(got, want) {
		t.Fatalf("shadowed = %v", got)
	}
}
