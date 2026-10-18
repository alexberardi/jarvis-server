package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeCC struct {
	mu       sync.Mutex
	valid    map[string]bool
	verified []string
	reports  map[string]any
	results  map[string]map[string]any
}

func newFakeCC() *fakeCC {
	return &fakeCC{valid: map[string]bool{}, reports: map[string]any{}, results: map[string]map[string]any{}}
}

func (f *fakeCC) Verify(_ context.Context, rid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verified = append(f.verified, rid)
	ok := f.valid[rid]
	delete(f.valid, rid) // single use, like jarvisd
	return ok, nil
}
func (f *fakeCC) PostToolReport(_ context.Context, rid string, r any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports[rid] = r
	return nil
}
func (f *fakeCC) PostToolResult(_ context.Context, rid string, o map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[rid] = o
	return nil
}

type echoTools struct{ calls int }

func (e *echoTools) Available(context.Context) []ToolDef {
	return []ToolDef{Spec{Name: "echo", Description: "echo", Params: []Param{{Name: "x", Type: "string"}}}.Render()}
}
func (e *echoTools) Call(_ context.Context, name string, args map[string]any, req Request) (map[string]any, bool) {
	if name != "echo" {
		return nil, false
	}
	e.calls++
	return map[string]any{"success": true, "x": args["x"], "voice": req.VoiceCommand}, true
}

func publish(t *testing.T, verb string, details map[string]any) []byte {
	b, err := json.Marshal([]map[string]any{{"command": verb, "details": details}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestToolCallVerifiesRunsAndReplies(t *testing.T) {
	cc := newFakeCC()
	cc.valid["r1"] = true
	tools := &echoTools{}
	d := &Dispatcher{CC: cc, Tools: tools, Log: quiet}
	// Mobile chat sends arguments as the model's JSON string.
	d.Handle(context.Background(), publish(t, "tool_call", map[string]any{
		"command_name": "echo", "arguments": `{"x":"hi"}`, "tool_call_id": "c1",
		"reply_request_id": "r1", "request_id": "r1", "user_id": 7, "voice_command": "say hi"}))
	got := cc.results["r1"]
	if got["success"] != true || got["x"] != "hi" || got["voice"] != "say hi" {
		t.Fatalf("result %v", got)
	}
}

// signalTools answers with node-local signals, as the sidecar does for a media command.
type signalTools struct{ deferred func() error }

func (signalTools) Available(context.Context) []ToolDef { return nil }
func (s signalTools) Call(context.Context, string, map[string]any, Request) (map[string]any, bool) {
	return map[string]any{"success": true, SignalsKey: Signals{WaitForInput: true, OnResponseComplete: s.deferred}}, true
}

func TestToolCallNeverPostsSignalsAndRunsTheDeferredStepAfterPosting(t *testing.T) {
	cc := newFakeCC()
	cc.valid["r1"] = true
	posted := false
	d := &Dispatcher{CC: cc, Log: quiet, Tools: signalTools{deferred: func() error {
		cc.mu.Lock()
		defer cc.mu.Unlock()
		_, posted = cc.results["r1"]
		return nil
	}}}
	d.Handle(context.Background(), publish(t, "tool_call", map[string]any{"command_name": "play", "reply_request_id": "r1", "request_id": "r1"}))
	if got := cc.results["r1"]; len(got) != 1 || got["success"] != true {
		t.Fatalf("posted %v: the signals must stay on the node", got)
	}
	if !posted {
		t.Fatal("the deferred step did not run after the result was posted")
	}
}

func TestUnverifiedToolCallIsDroppedUnrun(t *testing.T) {
	cc := newFakeCC()
	tools := &echoTools{}
	d := &Dispatcher{CC: cc, Tools: tools, Log: quiet}
	d.Handle(context.Background(), publish(t, "tool_call", map[string]any{
		"command_name": "echo", "arguments": map[string]any{"x": "hi"}, "reply_request_id": "forged", "request_id": "forged"}))
	if tools.calls != 0 || len(cc.results) != 0 {
		t.Fatalf("forged call ran: calls=%d results=%v", tools.calls, cc.results)
	}
	if len(cc.verified) != 1 {
		t.Fatalf("verify not asked: %v", cc.verified)
	}
}

func TestUnknownCommandAnswersAnError(t *testing.T) {
	cc := newFakeCC()
	cc.valid["r2"] = true
	d := &Dispatcher{CC: cc, Tools: &echoTools{}, Log: quiet}
	d.Handle(context.Background(), publish(t, "tool_call", map[string]any{"command_name": "rm_rf", "reply_request_id": "r2", "request_id": "r2"}))
	if r := cc.results["r2"]; r["success"] != false || !strings.Contains(r["error"].(string), "Unknown command") {
		t.Fatalf("result %v", r)
	}
}

func TestReportTools(t *testing.T) {
	cc := newFakeCC()
	d := &Dispatcher{CC: cc, Tools: Multi{&echoTools{}, &echoTools{}}, Log: quiet}
	d.Handle(context.Background(), publish(t, "report_tools", map[string]any{"reply_request_id": "r3", "request_id": "r3"}))
	rep := cc.reports["r3"].(map[string]any)
	if n := len(rep["client_tools"].([]any)); n != 1 { // Multi dedupes by name
		t.Fatalf("client_tools %d", n)
	}
	if len(rep["available_commands"].([]any)) != 1 || rep["installed_packages"] == nil {
		t.Fatalf("report %v", rep)
	}
}

// The installed packages go out with the tools, as a Pi reports them (the app's Update, Revert
// and red health dot on a package card).
func TestReportToolsListsInstalledPackages(t *testing.T) {
	cc := newFakeCC()
	pkgs := []map[string]any{{"name": "get_weather_meteo", "version": "1.0.0", "previous_version": "0.9.0", "health": "ok"}}
	d := &Dispatcher{CC: cc, Tools: &echoTools{}, Log: quiet, Packages: func() []map[string]any { return pkgs }}
	d.Handle(context.Background(), publish(t, "report_tools", map[string]any{"reply_request_id": "r4"}))
	got, _ := json.Marshal(cc.reports["r4"].(map[string]any)["installed_packages"])
	if string(got) != `[{"health":"ok","name":"get_weather_meteo","previous_version":"0.9.0","version":"1.0.0"}]` {
		t.Fatalf("installed_packages = %s", got)
	}
	d.Packages = func() []map[string]any { return nil }
	d.Handle(context.Background(), publish(t, "report_tools", map[string]any{"reply_request_id": "r5"}))
	if got, _ := json.Marshal(cc.reports["r5"].(map[string]any)["installed_packages"]); string(got) != `[]` {
		t.Fatalf("none = %s", got)
	}
}

func TestOtherVerbsAndJunkAreIgnored(t *testing.T) {
	cc := newFakeCC()
	d := &Dispatcher{CC: cc, Tools: &echoTools{}, Log: quiet}
	d.Handle(context.Background(), publish(t, "tts", map[string]any{"message": "hi"}))
	d.Handle(context.Background(), []byte("not json"))
	if len(cc.results)+len(cc.reports)+len(cc.verified) != 0 {
		t.Fatal("ignored verbs produced traffic")
	}
}

func TestCredsRoundTripPrivately(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "node.json")
	c := Creds{ConfigURL: "http://x:7700", NodeID: "n1", NodeKey: "SECRET-KEY"}
	if err := SaveCreds(p, c); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
		_ = os.Chmod(p, 0o644)
		if _, err := LoadCreds(p); err == nil {
			t.Fatal("loaded a world-readable key file")
		}
		_ = os.Chmod(p, 0o600)
	}
	got, err := LoadCreds(p)
	if err != nil || got.NodeKey != "SECRET-KEY" {
		t.Fatalf("%v %v", got.NodeID, err)
	}
	var sb strings.Builder
	slog.New(slog.NewTextHandler(&sb, nil)).Info("x", "creds", got)
	if strings.Contains(sb.String()+got.String(), "SECRET") {
		t.Fatal("the node key leaked into a log line or String()")
	}
	if _, err := LoadCreds(filepath.Join(t.TempDir(), "none.json")); err != ErrNotEnrolled {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverAsksForRemoteStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("style") != "remote" || r.URL.Query().Get("remote_host") != "127.0.0.1" {
			http.Error(w, "bad style", 400)
			return
		}
		_, _ = w.Write([]byte(`{"services":[{"name":"jarvis-mqtt-broker","url":"mqtt://10.0.0.5:1884"}]}`))
	}))
	defer srv.Close()
	s, err := Discover(context.Background(), srv.Client(), srv.URL)
	if err != nil || s[SvcMQTT] != "mqtt://10.0.0.5:1884" {
		t.Fatalf("%v %v", s, err)
	}
	if _, err := s.Need(SvcCommandCenter); err == nil {
		t.Fatal("missing service did not error")
	}
}

func TestBrokerURL(t *testing.T) {
	for in, want := range map[string]string{
		"mqtt://10.0.0.1:1884":       "tcp://10.0.0.1:1884",
		"mqtt://h":                   "tcp://h:1883",
		"wss://mqtt.example.io/mqtt": "wss://mqtt.example.io:443/mqtt",
	} {
		if got, err := brokerURL(in); err != nil || got != want {
			t.Fatalf("%s -> %s %v", in, got, err)
		}
	}
	if _, err := brokerURL("http://x"); err == nil {
		t.Fatal("http accepted as a broker")
	}
}

func TestDecodeArgs(t *testing.T) {
	if DecodeArgs(`{"a":1}`)["a"] != 1.0 || len(DecodeArgs("nope")) != 0 || len(DecodeArgs(nil)) != 0 {
		t.Fatal("DecodeArgs")
	}
}

// The heartbeat says what kind of node this is: jarvisd keys remote updates off install_mode
// (only "tarball"), and version_info.runtime tells a Go node from the Python one.
func TestHeartbeatIdentity(t *testing.T) {
	var got map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/admin/nodes/heartbeat" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		key = r.Header.Get("X-API-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	c := NewCC(srv.URL, Creds{NodeID: "n1", NodeKey: "k1", ConfigURL: srv.URL})
	if err := c.Heartbeat(context.Background(), Beat{Version: "1.2.3", InstallMode: "desktop-agent"}); err != nil {
		t.Fatal(err)
	}
	if key != "n1:k1" {
		t.Errorf("X-API-Key = %q", key)
	}
	vi, _ := got["version_info"].(map[string]any)
	if vi["version"] != "1.2.3" || vi["install_mode"] != "desktop-agent" || vi["runtime"] != "go" {
		t.Errorf("version_info = %v", vi)
	}
	if got["needs_k2"] != false {
		t.Errorf("needs_k2 = %v", got["needs_k2"])
	}
	// A Pi-style beat says nothing about its kind or capabilities.
	if _, ok := got["node_kind"]; ok {
		t.Errorf("node_kind sent: %v", got)
	}
	if _, ok := got["capabilities"]; ok {
		t.Errorf("capabilities sent: %v", got)
	}
	err := c.Heartbeat(context.Background(), Beat{Version: "1.2.3", InstallMode: "desktop-agent", NodeKind: "desktop",
		Capabilities: map[string]any{"browser_extension": "connected"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["node_kind"] != "desktop" || got["capabilities"].(map[string]any)["browser_extension"] != "connected" {
		t.Errorf("desktop beat = %v", got)
	}
	// Without K2 the node asks the app for one (DESIGN §16).
	if err := c.Heartbeat(context.Background(), Beat{Version: "1.2.3", NeedsK2: true}); err != nil {
		t.Fatal(err)
	}
	if got["needs_k2"] != true {
		t.Errorf("needs_k2 = %v", got["needs_k2"])
	}
}

// A capability change sends a heartbeat at once, not at the next tick.
func TestRunnerHeartbeatsOnChange(t *testing.T) {
	var srvURL string
	beats := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]string{
				{"name": SvcCommandCenter, "url": srvURL},
				{"name": SvcMQTT, "url": "mqtt://127.0.0.1:1"},
			}})
		case "/api/v0/node/mqtt-credentials":
			_, _ = w.Write([]byte(`{"username":null,"password":null}`))
		case "/api/v0/admin/nodes/heartbeat":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			beats <- b
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	var mu sync.Mutex
	state := "disconnected"
	ch := make(chan struct{})
	r := &Runner{Creds: Creds{ConfigURL: srv.URL, NodeID: "n1", NodeKey: "k"}, Tools: Multi{}, Log: quiet,
		NodeKind: "desktop", Heartbeat: time.Hour,
		Capabilities: func() map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return map[string]any{"browser_extension": state}
		},
		Changed: func() <-chan struct{} {
			mu.Lock()
			defer mu.Unlock()
			return ch
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	first := <-beats
	if first["node_kind"] != "desktop" || first["capabilities"].(map[string]any)["browser_extension"] != "disconnected" {
		t.Fatalf("first beat %v", first)
	}
	mu.Lock()
	state = "connected"
	old := ch
	ch = make(chan struct{})
	mu.Unlock()
	close(old)
	second := <-beats
	if second["capabilities"].(map[string]any)["browser_extension"] != "connected" {
		t.Fatalf("second beat %v", second)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// OnConnected (the voice pipeline's start) runs once jarvisd is found, gets the node's CC
// client, and Run waits for it to return after ctx ends.
func TestRunnerStartsOnConnectedAndWaitsForIt(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]string{
				{"name": SvcCommandCenter, "url": srvURL},
				{"name": SvcMQTT, "url": "mqtt://127.0.0.1:1"}, // nothing listens; paho retries quietly
			}})
		case "/api/v0/node/mqtt-credentials":
			_, _ = w.Write([]byte(`{"username":null,"password":null}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan *CC, 1)
	returned := make(chan struct{})
	r := &Runner{Creds: Creds{ConfigURL: srv.URL, NodeID: "n1", NodeKey: "k"}, Tools: Multi{}, Log: quiet,
		OnConnected: func(ctx context.Context, cc *CC) {
			got <- cc
			<-ctx.Done()
			close(returned)
		}}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	cc := <-got
	if cc.BaseURL != srv.URL || cc.Creds.NodeID != "n1" {
		t.Fatalf("cc %s %s", cc.BaseURL, cc.Creds.NodeID)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("Run returned before OnConnected did")
	}
}

// G8: jarvisd accepts a temp-password session's token only for changing the password, so
// enrolment stops at login with a sentence that says what to do.
func TestLoginRefusesATemporaryPassword(t *testing.T) {
	must := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"access_token":"tok","must_change_password":%v}`, must)
	}))
	defer srv.Close()
	if _, err := Login(context.Background(), srv.Client(), srv.URL, "a@example.com", "pw"); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("err = %v", err)
	}
	must = false
	if tok, err := Login(context.Background(), srv.Client(), srv.URL, "a@example.com", "pw"); err != nil || tok != "tok" {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestRegisterPreferringFallsBackOnlyWhenUnreachable(t *testing.T) {
	var hits []string
	cc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "cc")
		fmt.Fprint(w, `{"node_id":"n1","room":"desktop","node_key":"k"}`)
	}))
	defer cc.Close()
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "node-cc")
		http.Error(w, `{"detail":"used"}`, http.StatusConflict)
	}))
	defer refusing.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()
	ctx := context.Background()

	var noted error
	c, err := RegisterPreferring(ctx, cc.Client(), goneURL, cc.URL, "http://cfg", "n1", "t", Registration{}, func(e error) { noted = e })
	if err != nil || c.NodeKey != "k" || noted == nil || len(hits) != 1 {
		t.Fatalf("unreachable node URL: %+v %v noted=%v hits=%v", c, err, noted, hits)
	}
	hits = nil
	// An answer (even an error) from the preferred URL means the token may be spent: no retry.
	if _, err := RegisterPreferring(ctx, cc.Client(), refusing.URL, cc.URL, "http://cfg", "n1", "t", Registration{}, nil); err == nil || len(hits) != 1 || hits[0] != "node-cc" {
		t.Fatalf("refused: err=%v hits=%v", err, hits)
	}
	hits = nil
	if _, err := RegisterPreferring(ctx, cc.Client(), "", cc.URL, "http://cfg", "n1", "t", Registration{}, nil); err != nil || len(hits) != 1 {
		t.Fatalf("no node URL: err=%v hits=%v", err, hits)
	}
}

// Register sends device_id and node_kind only when set, so a Pi's body is unchanged.
func TestRegisterSendsIdentityWhenSet(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = nil
		_ = json.NewDecoder(r.Body).Decode(&sent)
		fmt.Fprint(w, `{"node_id":"n1","room":"office","node_key":"k"}`)
	}))
	defer srv.Close()
	ctx := context.Background()
	if _, err := Register(ctx, srv.Client(), srv.URL, "http://cfg", "n1", "t",
		Registration{Room: "office", DeviceID: "d1", NodeKind: "desktop"}); err != nil {
		t.Fatal(err)
	}
	if sent["device_id"] != "d1" || sent["node_kind"] != "desktop" || sent["room"] != "office" {
		t.Fatalf("sent %v", sent)
	}
	if _, err := Register(ctx, srv.Client(), srv.URL, "http://cfg", "n1", "t", Registration{}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"device_id", "node_kind", "room"} {
		if _, has := sent[k]; has {
			t.Fatalf("empty %s was sent: %v", k, sent)
		}
	}
}

func TestSpecRenderRunWithoutConfirmation(t *testing.T) {
	for _, want := range []bool{false, true} {
		def := Spec{Name: "lookup", Description: "lookup", RunWithoutConfirmation: want}.Render()
		if got := def.ClientTool["run_without_confirmation"]; got != want {
			t.Errorf("ClientTool run_without_confirmation = %v, want %v", got, want)
		}
		if got := def.Command["run_without_confirmation"]; got != want {
			t.Errorf("Command run_without_confirmation = %v, want %v", got, want)
		}
	}
}

func (f *fakeCC) PostAmbientResult(_ context.Context, rid string, p map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results["ambient:"+rid] = p
	return nil
}

func TestVerbsHookGetsItsVerbsOnly(t *testing.T) {
	cc := newFakeCC()
	var got []string
	d := &Dispatcher{CC: cc, Tools: &echoTools{}, Log: quiet, Verbs: map[string]Verb{
		"preview_led_pattern": func(_ context.Context, b Backend, det map[string]any) {
			if b != cc {
				t.Error("the verb didn't get the dispatcher's backend")
			}
			got = append(got, "preview:"+str(det, "pattern"))
		},
	}}
	d.Handle(context.Background(), publish(t, "preview_led_pattern", map[string]any{"pattern": "thinking"}))
	d.Handle(context.Background(), publish(t, "tts", map[string]any{"message": "hi"}))
	if len(got) != 1 || got[0] != "preview:thinking" {
		t.Fatalf("verbs = %v", got)
	}
	if len(cc.results)+len(cc.reports)+len(cc.verified) != 0 {
		t.Fatal("an unhooked verb produced traffic")
	}
}

func TestPostAmbientResultHitsTheNodesSink(t *testing.T) {
	var path, key string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key = r.Method+" "+r.URL.Path, r.Header.Get("X-API-Key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	cc := NewCC(srv.URL, Creds{NodeID: "n1", NodeKey: "k"})
	if err := cc.PostAmbientResult(context.Background(), "r 1", map[string]any{"success": false, "error": "no_audio_captured"}); err != nil {
		t.Fatal(err)
	}
	if path != "POST /api/v0/nodes/n1/ambient-noise-measurements/r 1/result" || key != "n1:k" {
		t.Fatalf("%s with key %q", path, key)
	}
	if body["success"] != false || body["error"] != "no_audio_captured" {
		t.Fatalf("body = %v", body)
	}
}
