package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fake jarvis-agent is this test binary run with VOICELANE_FAKE_AGENT set: it "wakes" when
// its WAV input has the fake TTS's wake level (0.5) in it, writes a reply WAV, logs a model call
// to the tap log and prints the turn as printTurn does; otherwise it says what `listen --once`
// says when no wake word was heard and exits 1.
func TestMain(m *testing.M) {
	switch os.Getenv("VOICELANE_FAKE_AGENT") {
	case "":
	case "pi":
		os.Exit(fakePiAgent(os.Args[1:]))
	default:
		os.Exit(fakeAgent(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeAgent(args []string) int {
	var in, out string
	for i, a := range args {
		if i+1 < len(args) {
			switch a {
			case "--voice-input":
				in = strings.TrimPrefix(args[i+1], "wav:")
			case "--voice-output":
				out = strings.TrimPrefix(args[i+1], "wav:")
			}
		}
	}
	if os.Getenv("HOME") != os.Getenv("VOICELANE_WANT_HOME") {
		fmt.Fprintln(os.Stderr, "jarvis-agent: HOME is", os.Getenv("HOME"))
		return 2
	}
	s, rate, err := readWAVFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jarvis-agent:", err)
		return 2
	}
	woke := false
	for _, v := range s {
		if v > 0.49 && v < 0.51 {
			woke = true
			break
		}
	}
	if !woke {
		fmt.Fprintln(os.Stderr, "jarvis-agent: no wake word heard")
		return 1
	}
	fmt.Fprintln(os.Stderr, "time=… level=INFO msg=wake conversation_id=c1 score=0.950")
	writeWAVFile(out, tone(rate, 0.3), rate)
	f, _ := os.OpenFile(os.Getenv("VOICELANE_TAP"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	fmt.Fprintln(f, `{"reply":{"tool_calls":[{"function":{"name":"get_current_time"}}]}}`)
	f.Close()
	fmt.Println(`turn c1: wake 0.950, recorded 2.00 s (silence threshold 300), heard "what time is it", ran get_current_time{} -> spoken, said "It is three." (1.00 s of audio)`)
	fmt.Println("  took 5s")
	return 0
}

// fakeSandbox is a sandbox under root/.sandbox/t whose cc speaks a "Hey Jarvis" WAV at the
// fake node's wake level (0.5) and transcribes every reply as "It is three.".
func fakeSandbox(t *testing.T, root string) (cc *httptest.Server, sbx string) {
	t.Helper()
	cc = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "node-1:key-1" {
			http.Error(w, "who", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v0/media/tts/speak":
			var b struct{ Text string }
			json.NewDecoder(r.Body).Decode(&b)
			level := float32(0.25)
			if strings.Contains(b.Text, "Hey Jarvis") {
				level = 0.5
			}
			w.Write(wav(t, tone(8000, level), 16000))
		case "/api/v0/media/whisper/transcribe":
			io.Copy(io.Discard, r.Body)
			w.Write([]byte(`{"text": " It is three. "}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cc.Close)
	sbx = filepath.Join(root, ".sandbox", "t")
	os.MkdirAll(sbx, 0o755)
	writeFile(t, sbx, "sandbox.env", fmt.Sprintf("export SBX_CC=%s\nexport SBX_CONFIG=%s\nexport SBX_NODE_ID=node-1\nexport SBX_NODE_KEY=key-1\nexport SBX_VOICE=whisper-small.en\n", cc.URL, cc.URL))
	return cc, sbx
}

func TestRunAgainstAFakeNodeAndSandbox(t *testing.T) {
	root := t.TempDir()
	cc, sbx := fakeSandbox(t, root)
	writeFile(t, sbx, "llm.jsonl", `{"reply":{"content":"from before the run"}}`+"\n")
	cases := writeFile(t, root, "cases.json", `{"cases": [
  {"name": "the time", "model": true, "audio": [{"say": "Hey Jarvis."}, {"say": "What time is it?"}],
   "expect": {"tool": "get_current_time", "reply": "(?i)three", "max_secs": 4}},
  {"name": "no wake", "audio": [{"say": "What time is it?"}], "expect": {"silent": true}},
  {"name": "barge-in", "needs": "pipewire", "audio": [{"say": "Hey Jarvis."}], "expect": {"outcome": "interrupted"}},
  {"name": "wrong reply", "audio": [{"say": "Hey Jarvis."}], "expect": {"reply": "seven"}}
]}`)
	self, _ := os.Executable()
	out := filepath.Join(root, "out")
	t.Setenv("VOICELANE_FAKE_AGENT", "1")
	t.Setenv("VOICELANE_TAP", filepath.Join(sbx, "llm.jsonl"))
	t.Setenv("VOICELANE_WANT_HOME", filepath.Join(out, "node"))
	var stdout strings.Builder
	err := run(context.Background(), []string{"--root", root, "--sandbox", "t", "--cases", cases, "--out", out, "--rel", "voice",
		"--agent", self, "--repeat", "2", "--silent-wait", "1s", "--reply-wait", "10s"}, &stdout)
	var fail errFailed
	if !errors.As(err, &fail) || fail.n != 1 {
		t.Fatalf("run: %v\n%s", err, stdout.String())
	}
	for _, want := range []string{"passed  the time (2/2)", "passed  no wake (1/1)", "skipped barge-in (0/0)", "failed  wrong reply (0/1)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
	var res struct {
		Suite, Status string
		Tests         []Test
	}
	b, err := os.ReadFile(filepath.Join(out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &res)
	if res.Suite != "voice" || res.Status != "failed" || len(res.Tests) != 4 {
		t.Fatalf("results: %s %s, %d tests", res.Suite, res.Status, len(res.Tests))
	}
	time := res.Tests[0]
	if time.Status != "passed" || time.Passes != 2 || len(time.Runs) != 2 || time.Line != 2 {
		t.Errorf("the time: %+v", time)
	}
	r := time.Runs[0]
	if r.ModelCalls != 1 || r.Reply != "It is three." || len(r.Wakes) != 1 || r.Response != 3 || r.Turn == nil || r.Turn.Said != "It is three." {
		t.Errorf("the time, run 1: %+v", r)
	}
	if want := "voice/cases/the-time/run-1/utterance.wav"; time.Artifacts[0] != want {
		t.Errorf("artifacts %v, want %s first", time.Artifacts, want)
	}
	if _, err := os.Stat(filepath.Join(out, "cases", "the-time", "run-2", "run.json")); err != nil {
		t.Error(err)
	}
	if nw := res.Tests[1]; nw.Status != "passed" || nw.Runs[0].Turn != nil || nw.Runs[0].ModelCalls != 0 {
		t.Errorf("no wake: %+v", nw)
	}
	if bi := res.Tests[2]; bi.Status != "skipped" || bi.Error == nil || !strings.Contains(*bi.Error, "--audio pipewire") {
		t.Errorf("barge-in: %+v", bi)
	}
	if wr := res.Tests[3]; wr.Status != "failed" || wr.Error == nil || !strings.Contains(*wr.Error, "want /seven/") {
		t.Errorf("wrong reply: %+v", wr)
	}
	// The node's identity is the sandbox's seeded node, in the run's own config dir.
	var creds map[string]any
	cb, _ := os.ReadFile(filepath.Join(out, "node", "config", "credentials.json"))
	json.Unmarshal(cb, &creds)
	if creds["node_id"] != "node-1" || creds["config_url"] != cc.URL {
		t.Errorf("creds %v", creds)
	}
}

func TestRunRefusesASandboxWithoutVoice(t *testing.T) {
	root := t.TempDir()
	sbx := filepath.Join(root, ".sandbox", "t")
	os.MkdirAll(sbx, 0o755)
	writeFile(t, sbx, "sandbox.env", "export SBX_CC=http://x\nexport SBX_CONFIG=http://x\nexport SBX_NODE_ID=a\nexport SBX_NODE_KEY=b\n")
	err := run(context.Background(), []string{"--root", root, "--sandbox", "t", "--out", t.TempDir(), "--agent", "x"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--voice") {
		t.Errorf("err = %v", err)
	}
}
