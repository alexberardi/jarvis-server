package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheRepoCasesLoad(t *testing.T) {
	cs, err := LoadCases("../../e2e/voice/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var silent, model, pw int
	for _, c := range cs {
		if c.Line == 0 {
			t.Errorf("%s: no line", c.Name)
		}
		if c.Expect.Silent {
			silent++
		}
		if c.Model {
			model++
		}
		if c.Needs == "pipewire" {
			pw++
		}
	}
	if silent < 3 || model < 4 || pw != 1 {
		t.Errorf("silent %d, model %d, pipewire-only %d", silent, model, pw)
	}
}

func TestLoadCasesRefusesBadCases(t *testing.T) {
	ok := `{"say": "Hey Jarvis."}`
	for name, c := range map[string]string{
		"unknown field":    `{"name": "a", "audio": [` + ok + `], "expect": {"outcome": "spoken"}, "bogus": 1}`,
		"two in one part":  `{"name": "a", "audio": [{"say": "x", "silence": 1}], "expect": {"outcome": "spoken"}}`,
		"empty part":       `{"name": "a", "audio": [{}], "expect": {"outcome": "spoken"}}`,
		"unknown noise":    `{"name": "a", "audio": [{"noise": "brown", "secs": 1}], "expect": {"silent": true}}`,
		"noise no secs":    `{"name": "a", "audio": [{"noise": "pink"}], "expect": {"silent": true}}`,
		"over first":       `{"name": "a", "audio": [{"say": "x", "over": 1}], "expect": {"outcome": "spoken"}}`,
		"silent and reply": `{"name": "a", "audio": [` + ok + `], "expect": {"silent": true, "reply": "x"}}`,
		"silent model":     `{"name": "a", "model": true, "audio": [` + ok + `], "expect": {"silent": true}}`,
		"expects nothing":  `{"name": "a", "audio": [` + ok + `], "expect": {}}`,
		"bad regexp":       `{"name": "a", "audio": [` + ok + `], "expect": {"reply": "("}}`,
		"bad needs":        `{"name": "a", "needs": "alsa", "audio": [` + ok + `], "expect": {"outcome": "spoken"}}`,
		"no audio":         `{"name": "a", "audio": [], "expect": {"outcome": "spoken"}}`,
		"no name":          `{"name": "--", "audio": [` + ok + `], "expect": {"outcome": "spoken"}}`,
		"same slug twice": `{"name": "A b", "audio": [` + ok + `], "expect": {"outcome": "spoken"}},
			{"name": "a-b", "audio": [` + ok + `], "expect": {"outcome": "spoken"}}`,
	} {
		p := writeFile(t, t.TempDir(), "cases.json", `{"cases": [`+c+`]}`)
		if _, err := LoadCases(p); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := LoadCases(writeFile(t, t.TempDir(), "c.json", `{"cases": []}`)); err == nil {
		t.Error("no cases: loaded")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"wake + timer": "wake-timer", "The time, through the clock tool": "the-time-through-the-clock-tool", " x ": "x"} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// printTurn's output, as cmd/jarvis-agent writes it.
const agentOut = `time=… level=INFO msg="voice: listening"
turn c1: wake 0.922, recorded 2.88 s (silence threshold 300), heard "What time is it?", ran get_current_time{}, ran other_tool{"a":"b -> c"} (failed: nope) -> spoken, said "It's \"ten\" past -> three." (1.20 s of audio)
  follow-up window: disabled
  took 5.873s
turn c2: wake 0.500, recorded 1.00 s (silence threshold 300), heard "", -> non_speech
  took 1.5s
`

func TestParseTurns(t *testing.T) {
	got := parseTurns(agentOut)
	want := []Turn{
		{Heard: "What time is it?", Outcome: "spoken", Said: `It's "ten" past -> three.`, ClientTools: []string{"get_current_time", "other_tool"}, Recorded: 2.88, ReplySecs: 1.2, TookSecs: 5.873},
		{Heard: "", Outcome: "non_speech", Recorded: 1, TookSecs: 1.5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if got := parseTurns("turn c3: wake 0.9, recorded 1.00 s (silence threshold 300), heard \"x\" -> spoken\n"); len(got) != 0 {
		t.Errorf("an unfinished turn counted: %+v", got)
	}
}

func TestWakes(t *testing.T) {
	log := `time=… level=INFO msg="voice: listening"
time=… level=INFO msg=wake conversation_id=a2068bde score=0.435
time=… level=INFO msg=wake conversation_id=b score=0.991
`
	if got := wakes(log); !reflect.DeepEqual(got, []float64{0.435, 0.991}) {
		t.Errorf("wakes = %v", got)
	}
	if got := wakes(agentOut); len(got) != 0 {
		t.Errorf("turn lines counted as wakes: %v", got)
	}
}

func TestJudge(t *testing.T) {
	spoken := &Turn{Heard: "what time is it", Outcome: "spoken", Said: "It is three.", ClientTools: []string{"get_current_time"}}
	for _, tc := range []struct {
		name string
		e    Expect
		r    Run
		want []string // substrings of the failures, in order; none = passes
	}{
		{"silent passes", Expect{Silent: true}, Run{}, nil},
		{"silent fails three ways", Expect{Silent: true}, Run{Turn: spoken, ReplySecs: 1.5, ModelCalls: 2},
			[]string{"took a turn", "played 1.5 s", "called 2 times"}},
		{"woke without a turn", Expect{Silent: true}, Run{Wakes: []float64{0.431}}, []string{"the node woke (score 0.431)"}},
		{"no turn", Expect{Reply: "x"}, Run{}, []string{"wake word was not heard"}},
		{"all good", Expect{Reply: "(?i)three", Tool: "get_current_time", MaxSecs: 5}, Run{Turn: spoken, Reply: "It is three.", Response: 3}, nil},
		{"tool from the model", Expect{Tool: "remember"}, Run{Turn: spoken, ModelTools: []string{"remember"}}, nil},
		{"wrong outcome", Expect{Outcome: "interrupted"}, Run{Turn: spoken}, []string{"ended spoken, want interrupted"}},
		{"wrong reply", Expect{Reply: "seven"}, Run{Turn: spoken, Reply: "It is three."}, []string{`"It is three.", want /seven/`}},
		{"no tool", Expect{Tool: "set_timer"}, Run{Turn: spoken, ModelTools: []string{"x"}}, []string{"no set_timer call"}},
		{"too slow", Expect{Reply: ".", MaxSecs: 5}, Run{Turn: spoken, Reply: "x", Response: 7}, []string{"took 7.0 s, the budget is 5.0 s"}},
	} {
		r := tc.r
		judge(tc.e, &r)
		if len(r.Failures) != len(tc.want) {
			t.Errorf("%s: failures %q, want %d", tc.name, r.Failures, len(tc.want))
			continue
		}
		for i, w := range tc.want {
			if !strings.Contains(r.Failures[i], w) {
				t.Errorf("%s: failure %q lacks %q", tc.name, r.Failures[i], w)
			}
		}
	}
}

func wav(t *testing.T, s []float32, rate int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := audio.WriteWAV(&b, s, rate); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func tone(n int, v float32) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = v
	}
	return s
}

func TestRenderCase(t *testing.T) {
	speak := func(_ context.Context, text string) ([]byte, error) {
		return wav(t, tone(len(text)*100, 0.5), 24000), nil
	}
	parts := []AudioPart{{Say: "Hey"}, {Silence: 0.5}, {Noise: "pink", Secs: 0.25, Gain: 0.2}, {Say: "Jarvis", Over: 0.1}}
	s, rate, err := renderCase(context.Background(), speak, parts, 1)
	if err != nil {
		t.Fatal(err)
	}
	// 300 + 12000 + 6000 samples; "Jarvis" (600) starts 2400 before the end, so it fits.
	if rate != 24000 || len(s) != 300+12000+6000 {
		t.Fatalf("rate %d, %d samples", rate, len(s))
	}
	if s[0] != 0.5 || s[300] != 0 {
		t.Errorf("speech then silence: %v %v", s[0], s[300])
	}
	for i, v := range s {
		if v > 1 || v < -1 {
			t.Fatalf("sample %d = %v: not clipped", i, v)
		}
	}
	again, _, _ := renderCase(context.Background(), speak, parts, 1)
	if !reflect.DeepEqual(s, again) {
		t.Error("the same seed made different noise")
	}

	// No speech: 16 kHz. Speech at two rates: refused.
	if _, rate, err := renderCase(context.Background(), nil, []AudioPart{{Silence: 1}}, 1); err != nil || rate != 16000 {
		t.Errorf("silence only: %d %v", rate, err)
	}
	n := 0
	mixed := func(_ context.Context, _ string) ([]byte, error) {
		n++
		return wav(t, tone(10, 0.1), 16000*n), nil
	}
	// A second rate (a recorded voice beside the TTS) is resampled to the first.
	if s, rate, err := renderCase(context.Background(), mixed, []AudioPart{{Say: "a"}, {Say: "b"}}, 1); err != nil || rate != 16000 || len(s) != 15 {
		t.Errorf("two sample rates: %d Hz, %d samples, %v", rate, len(s), err)
	}
	if _, _, err := renderCase(context.Background(), func(context.Context, string) ([]byte, error) { return nil, fmt.Errorf("503") }, []AudioPart{{Say: "a"}}, 1); err == nil || !strings.Contains(err.Error(), `tts "a"`) {
		t.Errorf("tts failure: %v", err)
	}
}

func TestNoiseIsBoundedAndLoud(t *testing.T) {
	for name, s := range map[string][]float32{
		"pink":  pinkNoise(rand.New(rand.NewSource(1)), 16000, 0.3),
		"music": music(16000, 16000, 0.3),
	} {
		peak, sum := 0.0, 0.0
		for _, v := range s {
			peak = math.Max(peak, math.Abs(float64(v)))
			sum += float64(v) * float64(v)
		}
		if peak > 0.3001 || math.Sqrt(sum/float64(len(s))) < 0.03 {
			t.Errorf("%s: peak %.3f, rms %.3f", name, peak, math.Sqrt(sum/float64(len(s))))
		}
	}
}

func TestVoiced(t *testing.T) {
	s := append(make([]float32, 8000), tone(4000, 0.1)...) // 0.5 s silence, 0.25 s sound
	s = append(s, make([]float32, 1600)...)
	secs, first := voiced(s, 16000)
	if math.Abs(secs-0.25) > 0.011 || math.Abs(first-0.5) > 0.011 {
		t.Errorf("voiced = %.3f s from %.3f s", secs, first)
	}
	if secs, first := voiced(make([]float32, 16000), 16000); secs != 0 || first != 0 {
		t.Errorf("silence: %v %v", secs, first)
	}
	if secs, _ := voiced(tone(16000, 0.01), 16000); secs != 0 {
		t.Errorf("below the floor counted: %v", secs)
	}
}

func TestModelCalls(t *testing.T) {
	dir := t.TempDir()
	old := `{"seq":1,"reply":{"tool_calls":[{"function":{"name":"old"}}]}}` + "\n"
	p := writeFile(t, dir, "llm.jsonl", old+
		`{"seq":2,"reply":{"tool_calls":[{"id":"1","type":"function","function":{"name":"get_current_time","arguments":"{}"}}]}}`+"\n"+
		`{"seq":3,"reply":{"content":"<tool_call>\n{\"name\": \"remember\", \"arguments\": {\"fact\": \"x\"}}\n</tool_call>"}}`+"\n"+
		`{"seq":4,"reply":{"content":"Done."}}`+"\n"+
		"not json\n")
	n, tools := modelCalls(p, int64(len(old)))
	if n != 3 || !reflect.DeepEqual(tools, []string{"get_current_time", "remember"}) {
		t.Errorf("%d calls, tools %v", n, tools)
	}
	if n, _ := modelCalls(filepath.Join(dir, "missing"), 0); n != 0 {
		t.Errorf("missing log: %d", n)
	}
}

func TestAgentEnvIsPrivate(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("XDG_CONFIG_HOME", "/home/someone/.config")
	env := agentEnv("/run/node")
	seen := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := seen[k]; dup && strings.HasPrefix(k, "XDG_") || k == "HOME" && seen[k] != "" {
			t.Errorf("%s twice", k)
		}
		seen[k] = v
	}
	if seen["HOME"] != "/run/node" || seen["XDG_CONFIG_HOME"] != "/run/node/config" || seen["XDG_CACHE_HOME"] != "/run/node/cache" {
		t.Errorf("env: HOME %q, config %q, cache %q", seen["HOME"], seen["XDG_CONFIG_HOME"], seen["XDG_CACHE_HOME"])
	}
}

func TestPipewireNeedsThePrivateSession(t *testing.T) {
	t.Setenv("JARVIS_E2E_AUDIO", "")
	if _, err := newPWRig(context.Background(), &lane{}, "pipewire", "m", "s"); err == nil || !strings.Contains(err.Error(), "private PipeWire") {
		t.Errorf("err = %v", err)
	}
}

func TestResultsShape(t *testing.T) {
	// The fields scripts/lib/e2e_report.py reads from every test.
	b, _ := json.Marshal(Test{Screenshots: []string{}, ConsoleErrors: []string{}})
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"suite", "id", "title", "file", "line", "status", "duration_ms", "error", "screenshots", "trace", "console_errors"} {
		if _, ok := m[k]; !ok {
			t.Errorf("no %s", k)
		}
	}
}
