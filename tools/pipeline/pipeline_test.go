package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	if string(body) != reply || gotPath != "/v1/chat/completions" || gotBody != `{"model":"m","messages":[]}` {
		t.Fatalf("passthrough: body %q path %q upstream body %q", body, gotPath, gotBody)
	}
	var e TapEntry
	if b := log.wait(t, 1); json.Unmarshal(b, &e) != nil {
		t.Fatalf("log %q", b)
	}
	if e.Seq != 1 || e.Status != 200 || e.Reply.Reasoning != "think" || e.Reply.Finish != "tool_calls" ||
		len(e.Reply.ToolCalls) != 1 || e.Reply.ToolCalls[0].Args()["minutes"] != 5.0 || string(e.Request) != `{"model":"m","messages":[]}` {
		t.Fatalf("entry %+v", e)
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
	if tools, _ := cc.started["client_tools"].([]any); len(tools) == 0 || !cc.ended {
		t.Fatalf("start %v ended %v", cc.started, cc.ended)
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
