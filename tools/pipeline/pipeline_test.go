package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is a log the tap writes after the response is done; wait blocks until it has n lines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) wait(t *testing.T, n int) []byte {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		out := slices.Clone(s.b.Bytes())
		s.mu.Unlock()
		if bytes.Count(out, []byte("\n")) >= n {
			return out
		}
	}
	t.Fatal("tap never logged")
	return nil
}

func TestTapPassesThroughAndLogsJSON(t *testing.T) {
	const reply = `{"choices":[{"message":{"content":null,"reasoning_content":"think","tool_calls":[{"id":"c1","type":"function","function":{"name":"set_timer","arguments":"{\"minutes\":5}"}}]},"finish_reason":"tool_calls"}]}`
	var gotPath, gotBody, gotApp, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.Path, string(b)
		gotApp, gotAuth = r.Header.Get("X-Jarvis-App-Key"), r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}))
	defer up.Close()
	log := &syncBuf{}
	tap := httptest.NewServer(&Tap{Upstream: func() (string, error) { return up.URL, nil }, Log: log,
		Header: http.Header{"X-Jarvis-App-Key": {"k"}}})
	defer tap.Close()

	req, _ := http.NewRequest(http.MethodPost, tap.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sandbox-remote-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if gotApp != "k" || gotAuth != "" {
		t.Fatalf("upstream headers: app key %q, authorization %q (the sandbox's must not leak)", gotApp, gotAuth)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != reply || gotPath != "/v1/chat/completions" || gotBody != `{"priority":"background","model":"m","messages":[]}` {
		t.Fatalf("passthrough: body %q path %q upstream body %q", body, gotPath, gotBody)
	}
	var e TapEntry
	if b := log.wait(t, 1); json.Unmarshal(b, &e) != nil {
		t.Fatalf("log %q", b)
	}
	if e.Seq != 1 || e.Status != 200 || e.Reply.Reasoning != "think" || e.Reply.Finish != "tool_calls" ||
		len(e.Reply.ToolCalls) != 1 || e.Reply.ToolCalls[0].Args()["minutes"] != 5.0 || string(e.Request) != `{"priority":"background","model":"m","messages":[]}` {
		t.Fatalf("entry %+v", e)
	}
}

func TestTapSendsBackgroundPriority(t *testing.T) {
	var mu sync.Mutex
	var gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	log := &syncBuf{}
	tap := httptest.NewServer(&Tap{Upstream: func() (string, error) { return up.URL, nil }, Log: log})
	defer tap.Close()

	for i, c := range []struct{ path, in, want string }{
		{"/v1/chat/completions", `{"model":"m","messages":[]}`, `{"priority":"background","model":"m","messages":[]}`},
		{"/openai/v1/chat/completions", `{"priority":"live","model":"m"}`, `{"priority":"live","model":"m"}`},
		{"/v1/chat/completions", `{}`, `{"priority":"background"}`},
		{"/v1/embeddings", `{"input":"x"}`, `{"input":"x"}`},
	} {
		resp, err := http.Post(tap.URL+c.path, "application/json", strings.NewReader(c.in))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		mu.Lock()
		got := gotBody
		mu.Unlock()
		if got != c.want {
			t.Errorf("%s %s: upstream got %s, want %s", c.path, c.in, got, c.want)
		}
		lines := bytes.Split(bytes.TrimSpace(log.wait(t, i+1)), []byte("\n"))
		var e TapEntry
		if err := json.Unmarshal(lines[i], &e); err != nil || string(e.Request) != c.want {
			t.Errorf("%s %s: logged %s, want %s", c.path, c.in, e.Request, c.want)
		}
	}
}

func TestParseSSEAssemblesDeltas(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hm\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\",\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"get_\",\"arguments\":\"{\\\"ci\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"weather\",\"arguments\":\"ty\\\":\\\"Rome\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	r := parseReply("text/event-stream", []byte(sse))
	if r.Content != "Hello" || r.Reasoning != "hm" || r.Finish != "tool_calls" || len(r.ToolCalls) != 1 ||
		r.ToolCalls[0].Function.Name != "get_weather" || r.ToolCalls[0].Args()["city"] != "Rome" {
		t.Fatalf("%+v", r)
	}
}

func TestTapUpstreamDown(t *testing.T) {
	log := &syncBuf{}
	tap := httptest.NewServer(&Tap{Upstream: func() (string, error) { return "", io.ErrUnexpectedEOF }, Log: log})
	defer tap.Close()
	resp, err := http.Post(tap.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if b := log.wait(t, 1); resp.StatusCode != http.StatusBadGateway || !bytes.Contains(b, []byte(`"error"`)) {
		t.Fatalf("status %d log %q", resp.StatusCode, b)
	}
}

func TestExpectCheck(t *testing.T) {
	tl := TurnLog{
		NodeCalls:   []CallLog{{Name: "set_timer", Args: map[string]any{"minutes": 12.0, "label": "Pasta"}}},
		ServerCalls: []CallLog{{Name: "recall", Args: map[string]any{"query": "pasta"}}},
		Reply:       "Timer set for 12 minutes.", Stop: "complete", Seconds: 3,
	}
	ok := []Expect{
		{Tool: "set_timer", Args: map[string]any{"minutes": 12}, ArgsMatch: map[string]string{"label": "(?i)pasta"}},
		{ServerTool: "recall", ArgsMatch: map[string]string{"query": "pasta"}},
		{AnyTool: []string{"recall", "remember"}},
		{Reply: `12 minutes`, NotReply: `(?i)sorry`, Stop: "complete", MaxSeconds: 5},
		{NotTool: []string{"cancel_timer"}},
		{NotCall: &CallMatch{Tool: "set_timer", ArgsMatch: map[string]string{"label": "(?i)rice"}}},
		{NotCall: &CallMatch{Tool: "cancel_timer"}},
	}
	for i, e := range ok {
		if f := e.Check(tl); len(f) != 0 {
			t.Errorf("ok[%d]: %v", i, f)
		}
	}
	bad := []Expect{
		{Tool: "cancel_timer"},
		{Tool: "set_timer", Args: map[string]any{"minutes": 10}},
		{Tool: "set_timer", ArgsMatch: map[string]string{"label": "^rice$"}},
		{ServerTool: "remember"},
		{NoTool: true},
		{NotTool: []string{"recall"}},
		{Reply: "hours"},
		{NotReply: "Timer"},
		{Stop: "tool_calls"},
		{MaxSeconds: 1},
		{NotCall: &CallMatch{Tool: "set_timer", ArgsMatch: map[string]string{"label": "(?i)pasta"}}},
		{NotCall: &CallMatch{Tool: "recall"}},
	}
	for i, e := range bad {
		if f := e.Check(tl); len(f) != 1 {
			t.Errorf("bad[%d]: want one failure, got %v", i, f)
		}
	}
	if f := (Expect{}).Check(TurnLog{Error: "boom"}); len(f) != 1 {
		t.Errorf("error turn: %v", f)
	}
}

func TestLoadScenarios(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"name":"one","turns":[{"say":"hi"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`[{"name":"two","turns":[{"say":"x"}]},{"name":"three","turns":[{"say":"y"}]}]`), 0o644)
	scs, err := LoadScenarios([]string{dir})
	if err != nil || len(scs) != 3 || scs[0].Name != "one" || scs[2].File != filepath.Join(dir, "b.json") {
		t.Fatalf("%v %+v", err, scs)
	}
	for _, bad := range []string{
		`{"turns":[{"say":"x"}]}`,
		`{"name":"n","turns":[]}`,
		`{"name":"n","turns":[{"say":"x","expect":{"reply":"("}}]}`,
		`{"name":"n","turns":[{"say":"x","expect":{"args":{"a":1}}}]}`,
		`{"name":"n","turns":[{"say":"x","expect":{"not_call":{"args_match":{"a":"x"}}}}]}`,
		`{"name":"n","turns":[{"say":"x","expect":{"not_call":{"tool":"t","args_match":{"a":"("}}}}]}`,
	} {
		p := filepath.Join(t.TempDir(), "x.json")
		os.WriteFile(p, []byte(bad), 0o644)
		if _, err := LoadScenarios([]string{p}); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestShippedScenariosLoad(t *testing.T) {
	scs, err := LoadScenarios([]string{"scenarios"})
	if err != nil || len(scs) == 0 {
		t.Fatalf("%d scenarios, %v", len(scs), err)
	}
}

// The pasta timer has 263 s left: any fair rounding passes (#93), a wrong one does not.
func TestTimeLeftReplyExpectation(t *testing.T) {
	scs, err := LoadScenarios([]string{"scenarios/timers.json"})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(scs, func(s Scenario) bool { return s.Name == "timer: how much time is left" })
	if i < 0 {
		t.Fatal("scenario not found")
	}
	e := scs[i].Turns[0].Expect
	check := func(reply string) []string {
		return e.Check(TurnLog{NodeCalls: []CallLog{{Name: "check_timers"}}, Reply: reply, Stop: "complete"})
	}
	for _, r := range []string{
		"There are about 4 and a half minutes left on the pasta timer.",
		"About four and a half minutes left.",
		"Your pasta timer has 4 minutes and 23 seconds left.",
		"About 4.4 minutes to go.",
		"Just over four minutes left.",
		"4:23 remaining.",
		"263 seconds left.",
		"About 4.5 minutes left.",
		"About 4.3 minutes left.",
		"About 4 mins left.",
		"4 min 23 sec left.",
	} {
		if f := check(r); len(f) != 0 {
			t.Errorf("%q: %v", r, f)
		}
	}
	for _, r := range []string{
		"About 5 minutes left on the pasta timer.",
		"Three minutes left.",
		"About 14 minutes left.",
		"4 seconds left.",
		"About 3 and a half minutes left.",
		"About 5.5 minutes left.",
		"About 5 mins left.",
		"5 minutes left.",
	} {
		if f := check(r); len(f) == 0 {
			t.Errorf("%q: want a failure", r)
		}
	}
}

func TestLoadToolSet(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	node, err := LoadToolSet(root, "node")
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(node)
	has := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	if !has("set_timer") || !has("control_device") || has("remember") || has("recall") {
		t.Fatalf("node tools %v", names)
	}
	if none, _ := LoadToolSet(root, "none"); len(none) != 0 {
		t.Fatal("none")
	}
	desk, err := LoadToolSet(root, "desktop")
	if err != nil || !slices.Contains(toolNames(desk), "desktop_type_text") {
		t.Fatalf("desktop tools %v, %v", toolNames(desk), err)
	}
}

// The checked-in desktop toolset is what the agent's own toolsets render, so a desktop scenario
// offers what a real agent sends (keywords included: the must-call guard reads them).
// Regenerate with: go run ./tools/pipeline toolset desktop > tools/pipeline/toolsets/desktop.json
func TestDesktopToolsetIsCurrent(t *testing.T) {
	want, err := RenderDesktopToolset()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("toolsets/desktop.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("toolsets/desktop.json is stale: go run ./tools/pipeline toolset desktop > tools/pipeline/toolsets/desktop.json")
	}
	var tools []map[string]any
	if err := json.Unmarshal(got, &tools); err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if kw, _ := tl["keywords"].([]any); len(kw) == 0 {
			t.Errorf("%v: no keywords", tl["function"])
		}
	}
}

func TestDesktopNodeDefaults(t *testing.T) {
	if got := resolveToolSpec(Scenario{Node: "desktop"}); got != "desktop" {
		t.Errorf("desktop node tools %q", got)
	}
	if got := resolveToolSpec(Scenario{Node: "desktop", Tools: "none"}); got != "none" {
		t.Errorf("explicit tools %q", got)
	}
	if got := resolveToolSpec(Scenario{}); got != "" {
		t.Errorf("seeded node tools %q", got)
	}
}

// fakeCC answers one tool round-trip: the first command calls set_timer, the continue
// completes with a reply; anything else completes with no tools.
type fakeCC struct {
	onCommand func()
	speakers  []any
	mu        sync.Mutex
	started   map[string]any
	results   []any
	ended     bool
}

func (f *fakeCC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != "node:key" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v0/conversation/start":
		f.started = body
		io.WriteString(w, `{"status":"success"}`)
	case "/api/v0/voice/command":
		f.speakers = append(f.speakers, body["speaker_user_id"])
		if f.onCommand != nil {
			f.onCommand()
			f.onCommand = nil
		}
		if strings.Contains(body["voice_command"].(string), "timer") {
			io.WriteString(w, `{"stop_reason":"tool_calls","tool_calls":[{"id":"t1","type":"function","function":{"name":"set_timer","arguments":"{\"minutes\":3}"}}],"assistant_message":null}`)
			return
		}
		if strings.Contains(body["voice_command"].(string), "broken") {
			io.WriteString(w, `{"stop_reason":"error","tool_calls":[],"assistant_message":null,"commands":[{"success":false,"errors":{"type":"llm_error","message":"model not loaded"}}]}`)
			return
		}
		io.WriteString(w, `{"stop_reason":"complete","tool_calls":[],"assistant_message":"Hi there."}`)
	case "/api/v0/voice/command/continue":
		f.results = append(f.results, body["tool_results"])
		io.WriteString(w, `{"stop_reason":"complete","tool_calls":[],"assistant_message":"Done, three minutes."}`)
	case "/api/v0/conversation/end":
		f.ended = true
		io.WriteString(w, `{"status":"ok"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestDriverRunsScenario(t *testing.T) {
	cc := &fakeCC{}
	srv := httptest.NewServer(cc)
	defer srv.Close()
	root, _ := filepath.Abs("../..")
	tapLog := filepath.Join(t.TempDir(), "llm.jsonl")
	os.WriteFile(tapLog, nil, 0o600)
	// The model calls the turn "made" land in the tap log while it runs: a server tool (not in
	// the node's set) must come back as a server call.
	cc.onCommand = func() {
		f, _ := os.OpenFile(tapLog, os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(`{"seq":1,"reply":{"tool_calls":[{"id":"r","type":"function","function":{"name":"recall","arguments":"{\"query\":\"x\"}"}},{"id":"t1","type":"function","function":{"name":"set_timer","arguments":"{}"}}]}}` + "\n")
		f.Close()
	}
	d := &Driver{Node: &Node{BaseURL: srv.URL, ID: "node", Key: "key"}, TapLog: tapLog, Root: root, Primary: 7}
	sc := Scenario{
		Name:    "timer",
		Speaker: "primary",
		Results: map[string]any{"set_timer": map[string]any{"success": true, "message": "Timer set."}},
		Turns: []Turn{
			{Say: "set a timer for 3 minutes", Expect: Expect{Tool: "set_timer", Args: map[string]any{"minutes": 3}, Reply: "three"}},
			{Say: "hello", Expect: Expect{NoTool: true, Reply: "Hi"}},
		},
	}
	l := d.Run(context.Background(), sc)
	if !l.Pass || len(l.Turns) != 2 {
		t.Fatalf("%+v", l)
	}
	if jsonText(cc.speakers) != "[7,7]" {
		t.Fatalf("speaker hints %v", cc.speakers)
	}
	if t0 := l.Turns[0]; len(t0.LLM) != 1 || len(t0.ServerCalls) != 1 || t0.ServerCalls[0].Name != "recall" || t0.Seconds <= 0 {
		t.Fatalf("turn 0 capture: %+v", t0)
	}
	if got := jsonText(cc.results); got != `[[{"output":{"message":"Timer set.","success":true},"tool_call_id":"t1"}]]` {
		t.Fatalf("results sent %s", got)
	}
	nc, _ := cc.started["node_context"].(map[string]any)
	if agents, _ := nc["agents"].(map[string]any); agents["home_assistant"] == nil {
		t.Fatalf("default home not sent: %v", nc)
	}
	if _, ok := nc["stt_vocabulary"]; ok {
		t.Fatalf("no vocabulary in the scenario, but sent: %v", nc)
	}
	if tools, _ := cc.started["client_tools"].([]any); len(tools) == 0 || !cc.ended {
		t.Fatalf("start %v ended %v", cc.started, cc.ended)
	}
	// A desktop scenario's app names go to command-center as the agent sends them, for whisper.
	apps := "Alacritty, Firefox, Omawrite"
	vocab := Scenario{Name: "v", STTVocabulary: &apps, Turns: []Turn{{Say: "hello"}}}
	if l := d.Run(context.Background(), vocab); !l.Pass {
		t.Fatalf("%+v", l)
	}
	if nc, _ := cc.started["node_context"].(map[string]any); nc["stt_vocabulary"] != "Alacritty, Firefox, Omawrite" {
		t.Fatalf("vocabulary not sent: %v", nc)
	}
	// A command-center error is the turn's error, with its message.
	if l := d.Run(context.Background(), Scenario{Name: "e", Turns: []Turn{{Say: "broken"}}}); l.Pass || l.Turns[0].Error != "command-center: model not loaded" {
		t.Fatalf("error turn %+v", l.Turns[0])
	}
	// A failing expectation fails the run and says why.
	sc.Turns[0].Expect.Args = map[string]any{"minutes": 4}
	if l := d.Run(context.Background(), sc); l.Pass || len(l.Turns[0].Failures) != 1 {
		t.Fatalf("%+v", l.Turns[0])
	}
}

func TestReadTapFromOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "llm.jsonl")
	os.WriteFile(p, []byte(`{"seq":1}`+"\n"), 0o600)
	mark := tapSize(p)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":2,"reply":{"tool_calls":[{"id":"x","type":"function","function":{"name":"recall","arguments":"{}"}}]}}` + "\n")
	f.Close()
	if es := readTap(p, mark); len(es) != 1 || es[0].Seq != 2 {
		t.Fatalf("%+v", es)
	}
}

func TestDriverSpeaker(t *testing.T) {
	d := &Driver{}
	if id, err := d.speakerID(""); id != 0 || err != nil {
		t.Fatal("none")
	}
	if _, err := d.speakerID("primary"); err == nil {
		t.Fatal("primary without SBX_USER_ID")
	}
	d.Primary = 3
	if id, _ := d.speakerID("primary"); id != 3 {
		t.Fatal("primary")
	}
	if id, _ := d.speakerID("12"); id != 12 {
		t.Fatal("numeric")
	}
	if _, err := d.speakerID("jordan"); err == nil {
		t.Fatal("name")
	}
}

func TestLoadHome(t *testing.T) {
	root, _ := filepath.Abs("../..")
	if b, err := LoadHome(root, ""); err != nil || !bytes.Contains(b, []byte("light.kitchen")) {
		t.Fatalf("default home: %v", err)
	}
	if b, err := LoadHome(root, "none"); b != nil || err != nil {
		t.Fatal("none")
	}
	if _, err := LoadHome(root, "nowhere"); err == nil {
		t.Fatal("missing home")
	}
}

func TestExpandNow(t *testing.T) {
	at := time.Date(2026, 10, 14, 0, 6, 30, 0, time.Local)
	if got := expandNow(`It's {now}`, at); got != `It's (?:12:06|12:05|12:07)` {
		t.Fatalf("%s", got)
	}
	if f := (Expect{Reply: "{now}"}).Check(TurnLog{Reply: "It's 12:07 AM.", End: at}); len(f) != 0 {
		t.Fatal(f)
	}
	if f := (Expect{Reply: "{now}"}).Check(TurnLog{Reply: "It's 2:47 PM.", End: at}); len(f) != 1 {
		t.Fatal("a made-up time passed")
	}
}

// "node": "desktop" pairs a desktop agent as the primary once (login, provisioning token,
// register, heartbeat with node_kind desktop), keeps its key, and plays the scenario as it.
func TestDriverDesktopNode(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	beats := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/login":
			if body["email"] != "p@example.com" || body["password"] != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"access_token":"jwt"}`)
		case "/households":
			io.WriteString(w, `[{"id":"hh-other","role":"member"},{"id":"hh-1","role":"admin"}]`)
		case "/api/v0/provisioning/token":
			if r.Header.Get("Authorization") != "Bearer jwt" || body["household_id"] != "hh-1" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusCreated) // as jarvisd answers
			io.WriteString(w, `{"token":"prov","node_id":"desk-1"}`)
		case "/api/v0/nodes/register":
			io.WriteString(w, `{"node_key":"dkey"}`)
		case "/api/v0/admin/nodes/heartbeat":
			if r.Header.Get("X-API-Key") != "desk-1:dkey" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			beats[r.Header.Get("X-API-Key")] = body["node_kind"]
			io.WriteString(w, `{"status":"ok"}`)
		case "/api/v0/conversation/start", "/api/v0/conversation/end":
			if r.Header.Get("X-API-Key") != "desk-1:dkey" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"status":"success"}`)
		case "/api/v0/voice/command":
			if r.Header.Get("X-API-Key") != "desk-1:dkey" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"stop_reason":"complete","tool_calls":[],"assistant_message":"You're Jordan."}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	root, _ := filepath.Abs("../..")
	state := filepath.Join(t.TempDir(), "desktop-node.json")
	newDriver := func() *Driver {
		return &Driver{Node: &Node{BaseURL: srv.URL, ID: "node", Key: "key"}, Root: root,
			Desktop: &DesktopSetup{AuthURL: srv.URL, Email: "p@example.com", Password: "pw", StateFile: state}}
	}
	sc := Scenario{Name: "who", Node: "desktop", Tools: "none", Home: "none",
		Turns: []Turn{{Say: "who am I?", Expect: Expect{Reply: "Jordan"}}}}
	if l := newDriver().Run(context.Background(), sc); !l.Pass {
		t.Fatalf("%+v", l)
	}
	if beats["desk-1:dkey"] != "desktop" {
		t.Fatalf("no desktop heartbeat: %v", beats)
	}
	if b, _ := os.ReadFile(state); !strings.Contains(string(b), `"node_key":"dkey"`) {
		t.Fatalf("state %s", b)
	}
	// A later run reuses the saved key: a heartbeat, no second pairing.
	calls = nil
	if l := newDriver().Run(context.Background(), sc); !l.Pass {
		t.Fatalf("%+v", l)
	}
	if slices.Contains(calls, "POST /auth/login") || !slices.Contains(calls, "POST /api/v0/admin/nodes/heartbeat") {
		t.Fatalf("second run %v", calls)
	}
	if l := newDriver().Run(context.Background(), Scenario{Name: "x", Node: "garage", Turns: []Turn{{Say: "hi"}}}); l.Pass {
		t.Fatal("unknown node kind")
	}
	if l := newDriver().Run(context.Background(), Scenario{Name: "x", Owner: "removed", Turns: []Turn{{Say: "hi"}}}); l.Pass {
		t.Fatal("an owner on the seeded node")
	}
}

// "owner": "removed" pairs the desktop agent as a new member the primary invited, then has the
// primary remove that member: an agent whose owner left the household.
func TestDriverDesktopRemovedOwner(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		switch r.Method + " " + r.URL.Path {
		case "POST /auth/login":
			io.WriteString(w, `{"access_token":"jwt"}`)
		case "GET /households":
			io.WriteString(w, `[{"id":"hh-1","role":"admin"}]`)
		case "POST /households/hh-1/invites":
			if auth != "Bearer jwt" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"code":"INV12345"}`)
		case "POST /auth/register":
			if body["invite_code"] != "INV12345" || !strings.HasSuffix(body["email"].(string), "@example.com") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"access_token":"jwt-owner","user":{"id":42}}`)
		case "POST /api/v0/provisioning/token":
			if auth != "Bearer jwt-owner" { // the new member pairs it, so they are its owner
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"token":"prov","node_id":"desk-2"}`)
		case "POST /api/v0/nodes/register":
			io.WriteString(w, `{"node_key":"k2"}`)
		case "POST /api/v0/admin/nodes/heartbeat", "POST /api/v0/conversation/start", "POST /api/v0/conversation/end":
			io.WriteString(w, `{"status":"ok"}`)
		case "DELETE /households/hh-1/members/42":
			if auth != "Bearer jwt" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "POST /api/v0/voice/command":
			io.WriteString(w, `{"stop_reason":"complete","tool_calls":[],"assistant_message":"Sign in again."}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	root, _ := filepath.Abs("../..")
	dir := t.TempDir()
	d := &Driver{Node: &Node{BaseURL: srv.URL, ID: "node", Key: "key"}, Root: root,
		Desktop: &DesktopSetup{AuthURL: srv.URL, Email: "p@example.com", Password: "pw", StateFile: filepath.Join(dir, "desktop-node.json")}}
	sc := Scenario{Name: "who", Node: "desktop", Owner: "removed", Tools: "none", Home: "none",
		Turns: []Turn{{Say: "who am I?", Expect: Expect{Reply: "Sign in"}}}}
	if l := d.Run(context.Background(), sc); !l.Pass {
		t.Fatalf("%+v", l)
	}
	removed := slices.Index(calls, "DELETE /households/hh-1/members/42")
	if removed < 0 || removed < slices.Index(calls, "POST /api/v0/nodes/register") {
		t.Fatalf("owner not removed after pairing: %v", calls)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "desktop-removed-owner-node.json")); !strings.Contains(string(b), `"node_id":"desk-2"`) {
		t.Fatalf("state %s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "desktop-node.json")); err == nil {
		t.Fatal("wrote the primary's desktop state")
	}
	if l := d.Run(context.Background(), Scenario{Name: "x", Node: "desktop", Owner: "someone", Turns: []Turn{{Say: "hi"}}}); l.Pass {
		t.Fatal("unknown owner")
	}
}

// "fresh_memories" deletes the primary's own memories before the run, through the app's API.
func TestDriverFreshMemories(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/auth/login":
			io.WriteString(w, `{"access_token":"jwt"}`)
		case r.URL.Path == "/households":
			io.WriteString(w, `[{"id":"hh-1","role":"admin"}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v0/mobile/memories":
			if r.Header.Get("Authorization") != "Bearer jwt" || r.URL.Query().Get("household_id") != "hh-1" ||
				r.URL.Query().Get("include_household") != "false" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, `[{"id":3},{"id":5}]`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v0/mobile/memories/"):
			deleted = append(deleted, r.URL.Path+"?"+r.URL.RawQuery)
			io.WriteString(w, `{"status":"deleted"}`)
		case r.URL.Path == "/api/v0/conversation/start", r.URL.Path == "/api/v0/conversation/end":
			io.WriteString(w, `{"status":"success"}`)
		case r.URL.Path == "/api/v0/voice/command":
			io.WriteString(w, `{"stop_reason":"complete","tool_calls":[],"assistant_message":"Okay."}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	root, _ := filepath.Abs("../..")
	d := &Driver{Node: &Node{BaseURL: srv.URL, ID: "node", Key: "key"}, Root: root,
		Desktop: &DesktopSetup{AuthURL: srv.URL, Email: "p@example.com", Password: "pw"}}
	sc := Scenario{Name: "fresh", Tools: "none", Home: "none", FreshMemories: true, Turns: []Turn{{Say: "hi"}}}
	if l := d.Run(context.Background(), sc); !l.Pass {
		t.Fatalf("%+v", l)
	}
	want := []string{"/api/v0/mobile/memories/3?household_id=hh-1", "/api/v0/mobile/memories/5?household_id=hh-1"}
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted %v", deleted)
	}
	// Without the sandbox's sign-in it fails the run instead of playing it on stale memories.
	d.Desktop = nil
	if l := d.Run(context.Background(), sc); l.Pass {
		t.Fatal("ran without clearing")
	}
}

// A text-path model (Qwen3 providers) writes its tool calls as <tool_call> blocks in the
// content; command-center parses and runs them, so the harness must count them too.
func TestTapReplyCallsFromText(t *testing.T) {
	cases := []struct {
		name  string
		reply TapReply
		want  []string
	}{
		{"structured", TapReply{ToolCalls: []ToolCall{{Function: FunctionCall{Name: "recall", Arguments: "{}"}}}}, []string{"recall"}},
		{"text block", TapReply{Content: "<tool_call>\n{\"name\": \"remember\", \"arguments\": {\"content\": \"Parking spot is B12\", \"key\": \"parking_spot\"}}\n</tool_call>"}, []string{"remember"}},
		{"think then two blocks", TapReply{Content: "<think>x</think><tool_call>{\"name\": \"desktop_open\", \"arguments\": {\"target\": \"omawrite\"}}</tool_call><tool_call>{\"name\": \"desktop_type\", \"arguments\": {\"text\": \"hi\"}}</tool_call>"}, []string{"desktop_open", "desktop_type"}},
		{"plain text", TapReply{Content: "Got it, saved. <exchange_complete/>"}, nil},
		{"bad json", TapReply{Content: "<tool_call>{nope</tool_call>"}, nil},
	}
	for _, c := range cases {
		var got []string
		for _, tc := range c.reply.Calls() {
			got = append(got, tc.Function.Name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: calls %v, want %v", c.name, got, c.want)
		}
	}
	args := TapReply{Content: "<tool_call>{\"name\": \"remember\", \"arguments\": {\"key\": \"parking_spot\"}}</tool_call>"}.Calls()[0].Function.Arguments
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil || m["key"] != "parking_spot" {
		t.Errorf("arguments %q, want the call's arguments as a JSON object", args)
	}
}

// A scenario's settings are put as the primary household admin before the conversation starts
// and the earlier values put back after it.
func TestDriverSettings(t *testing.T) {
	var mu sync.Mutex
	var log []string
	vals := map[string]any{"watches.enabled": false, "web_search.enabled": false}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		const base = "/api/v0/mobile/household/hh-1/settings"
		switch {
		case r.URL.Path == "/auth/login":
			io.WriteString(w, `{"access_token":"jwt"}`)
		case r.URL.Path == "/households":
			io.WriteString(w, `[{"id":"hh-0","role":"member"},{"id":"hh-1","role":"admin"}]`)
		case r.Method == http.MethodGet && r.URL.Path == base:
			b, _ := json.Marshal(map[string]any{"household_id": "hh-1", "settings": vals})
			w.Write(b)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, base+"/"):
			if r.Header.Get("Authorization") != "Bearer jwt" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			var body struct {
				Value any `json:"value"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			key := strings.TrimPrefix(r.URL.Path, base+"/")
			vals[key] = body.Value
			log = append(log, fmt.Sprintf("put %s=%v", key, body.Value))
			io.WriteString(w, `{"success":true}`)
		case r.URL.Path == "/api/v0/conversation/start":
			log = append(log, fmt.Sprintf("start watches=%v", vals["watches.enabled"]))
			io.WriteString(w, `{"status":"success"}`)
		case r.URL.Path == "/api/v0/conversation/end":
			io.WriteString(w, `{"status":"success"}`)
		case r.URL.Path == "/api/v0/voice/command":
			io.WriteString(w, `{"stop_reason":"complete","tool_calls":[],"assistant_message":"Okay."}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	root, _ := filepath.Abs("../..")
	d := &Driver{Node: &Node{BaseURL: srv.URL, ID: "node", Key: "key"}, Root: root,
		Desktop: &DesktopSetup{AuthURL: srv.URL, Email: "p@example.com", Password: "pw"}}
	sc := Scenario{Name: "settings", Tools: "none", Home: "none", Turns: []Turn{{Say: "hi"}},
		Settings: map[string]any{"watches.enabled": true, "web_search.enabled": true}}
	if l := d.Run(context.Background(), sc); !l.Pass {
		t.Fatalf("%+v", l)
	}
	want := []string{"put watches.enabled=true", "put web_search.enabled=true", "start watches=true",
		"put watches.enabled=false", "put web_search.enabled=false"}
	if !slices.Equal(log, want) {
		t.Fatalf("calls %v", log)
	}
	// A key the household can't set fails the run before it starts, and puts back what it set.
	log = nil
	sc.Settings = map[string]any{"watches.enabled": true, "zz.unknown": 1}
	if l := d.Run(context.Background(), sc); l.Pass || !strings.Contains(l.Turns[0].Error, "zz.unknown") {
		t.Fatalf("%+v", l)
	}
	if want := []string{"put watches.enabled=true", "put watches.enabled=false"}; !slices.Equal(log, want) {
		t.Fatalf("calls %v", log)
	}
	// Without the sandbox's sign-in it fails instead of playing with the settings off.
	d.Desktop = nil
	if l := d.Run(context.Background(), sc); l.Pass {
		t.Fatal("ran without the settings")
	}
}

// #221: a server tool's result is the role=tool message command-center sends the model next:
// matched by the call's id, or (a text-path model's parsed calls get fresh ids) by its name
// among the results after the request's last assistant message. An earlier turn's result for
// the same tool is not this call's.
func TestServerCallResults(t *testing.T) {
	req := func(msgs ...map[string]any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"messages": msgs})
		return b
	}
	old := map[string]any{"role": "tool", "tool_call_id": "c0", "name": "cancel_watch", "content": `{"error": "not_found"}`}
	entries := []TapEntry{
		{Request: req(old), Reply: TapReply{ToolCalls: []ToolCall{{ID: "c1", Function: FunctionCall{Name: "cancel_watch", Arguments: `{"watch": "example.com"}`}},
			{ID: "n1", Function: FunctionCall{Name: "set_timer", Arguments: `{}`}}}}},
		{Request: req(old, map[string]any{"role": "assistant"},
			map[string]any{"role": "tool", "tool_call_id": "c1", "name": "cancel_watch", "content": `{"status": "cancelled", "watch_id": "wt_1"}`}),
			Reply: TapReply{Content: `<tool_call>{"name": "list_watches", "arguments": {}}</tool_call>`}},
		{Request: req(old, map[string]any{"role": "assistant"},
			map[string]any{"role": "tool", "tool_call_id": "other-id", "name": "list_watches", "content": `{"watches": []}`}),
			Reply: TapReply{Content: `<tool_call>{"name": "cancel_watch", "arguments": {}}</tool_call>`}},
		{Request: req(old, map[string]any{"role": "assistant"}), Reply: TapReply{Content: "Done."}},
	}
	// Content as text parts reads the same.
	if r := decodeContent(json.RawMessage(`[{"type": "text", "text": "{\"status\": \"watching\"}"}]`)); jsonText(r) != `{"status":"watching"}` {
		t.Fatalf("parts: %v", r)
	}
	got := serverCallLogs(entries, []string{"set_timer"})
	if len(got) != 3 || got[0].Name != "cancel_watch" || got[1].Name != "list_watches" || got[2].Name != "cancel_watch" {
		t.Fatalf("%+v", got)
	}
	if r, _ := got[0].Result.(map[string]any); r["status"] != "cancelled" || got[0].Args["watch"] != "example.com" {
		t.Fatalf("by id: %+v", got[0])
	}
	if jsonText(got[1].Result) != `{"watches":[]}` {
		t.Fatalf("by name: %+v", got[1])
	}
	if got[2].Result != nil {
		t.Fatalf("no result of its own: %+v", got[2])
	}
}

// The tool expectation is judged on the call that meets it, the last one when none does: on
// qwen3.5-9b the first create_watch omits signal_kind, fails, and the retry succeeds.
func TestExpectJudgesTheCallThatMeetsIt(t *testing.T) {
	tl := TurnLog{ServerCalls: []CallLog{
		{Name: "create_watch", Args: map[string]any{"source": "signal"}, Result: map[string]any{"error": "invalid"}},
		{Name: "create_watch", Args: map[string]any{"source": "signal", "signal_kind": "presence.left"},
			Result: map[string]any{"status": "watching", "watch_id": "wt_2"}},
	}}
	ok := []Expect{
		{ServerTool: "create_watch", ArgsMatch: map[string]string{"signal_kind": `presence\.left`},
			ResultMatch: map[string]string{"status": "^watching$"}},
		{ServerTool: "create_watch", ResultMatch: map[string]string{"error": "invalid"}},
		{ServerTool: "create_watch", NotResultMatch: map[string]string{"error": "."}},
	}
	for i, e := range ok {
		if f := e.Check(tl); len(f) != 0 {
			t.Errorf("ok[%d]: %v", i, f)
		}
	}
	bad := []Expect{
		{ServerTool: "create_watch", ResultMatch: map[string]string{"status": "^cancelled$"}},
		{ServerTool: "create_watch", ArgsMatch: map[string]string{"signal_kind": `presence\.arrived`}},
		{ServerTool: "create_watch", NotResultMatch: map[string]string{"status": "watching", "error": "invalid"}},
	}
	for i, e := range bad {
		if f := e.Check(tl); len(f) != 1 {
			t.Errorf("bad[%d]: want one failure, got %v", i, f)
		}
	}
	// A node tool's result is the node's answer.
	node := TurnLog{NodeCalls: []CallLog{{Name: "set_timer", Args: map[string]any{}, Result: map[string]any{"success": true}}}}
	if f := (Expect{Tool: "set_timer", ResultMatch: map[string]string{"success": "true"}}).Check(node); len(f) != 0 {
		t.Errorf("node result: %v", f)
	}
}
