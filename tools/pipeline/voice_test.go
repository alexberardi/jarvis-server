package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// fakeVoiceCC is command-center with a media proxy: TTS "synthesizes" the text into a WAV's
// samples, STT reads it back with mishearings applied, the way whisper turns Obsidian into
// Omerite. Turns go to the wrapped fakeCC.
type fakeVoiceCC struct {
	*fakeCC
	mishear  map[string]string
	notReady int // transcriptions answered 503 "not ready" before working

	vmu      sync.Mutex
	commands []string // voice_command as received
	convs    []string // conversation_id on each transcription
}

func textWAV(text string) []byte {
	var b bytes.Buffer
	audio.WritePCM16WAV(&b, []byte(text+strings.Repeat(" ", len(text)%2)), 1, 16000)
	return b.Bytes()
}

func (f *fakeVoiceCC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != "node:key" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/v0/media/tts/speak":
		var body struct{ Text string }
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(textWAV(body.Text))
	case "/api/v0/media/whisper/transcribe":
		f.vmu.Lock()
		defer f.vmu.Unlock()
		if f.notReady > 0 {
			f.notReady--
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"detail":"stt: speech-to-text engine unavailable: loading: label stt not ready: starting"}`+"\n")
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		wav, _ := io.ReadAll(file)
		f.convs = append(f.convs, r.FormValue("conversation_id"))
		text := strings.TrimSpace(string(wav[44:]))
		for from, to := range f.mishear {
			text = strings.ReplaceAll(text, from, to)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"text": " " + text, "segments": []any{}, "speaker": map[string]any{"outcome": "unknown"}})
	default:
		if r.URL.Path == "/api/v0/voice/command" {
			b, _ := io.ReadAll(r.Body)
			var body struct {
				VoiceCommand string `json:"voice_command"`
			}
			json.Unmarshal(b, &body)
			f.vmu.Lock()
			f.commands = append(f.commands, body.VoiceCommand)
			f.vmu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		f.fakeCC.ServeHTTP(w, r)
	}
}

func voiceDriver(t *testing.T, cc http.Handler) *Driver {
	t.Helper()
	srv := httptest.NewServer(cc)
	t.Cleanup(srv.Close)
	root, _ := filepath.Abs("../..")
	return &Driver{Node: &Node{BaseURL: srv.URL, ID: "node", Key: "key"}, Root: root, Primary: 7}
}

func TestVoiceTurnSendsWhatWasHeard(t *testing.T) {
	cc := &fakeVoiceCC{fakeCC: &fakeCC{}, mishear: map[string]string{"Obsidian": "Omerite"}}
	d := voiceDriver(t, cc)
	zero := 0.0
	sc := Scenario{Name: "open", Voice: true, Turns: []Turn{
		{Say: "open Obsidian", Expect: Expect{Heard: "(?i)obsidian"}},
		{Say: "set a timer for 3 minutes", Expect: Expect{Tool: "set_timer", MaxWER: &zero, NotHeard: "Omerite"}},
	}}
	l := d.Run(context.Background(), sc)
	if l.Pass || len(l.Turns) != 2 {
		t.Fatalf("pass %v turns %d", l.Pass, len(l.Turns))
	}
	t0, t1 := l.Turns[0], l.Turns[1]
	if t0.Voice == nil || t0.Voice.Heard != "open Omerite" || t0.Voice.WER != 0.5 || t0.Voice.Speaker != "unknown" {
		t.Fatalf("turn 1 voice %+v", t0.Voice)
	}
	if len(t0.Failures) != 1 || !strings.Contains(t0.Failures[0], `heard "open Omerite"`) {
		t.Fatalf("turn 1 failures %v", t0.Failures)
	}
	if t1.Voice == nil || t1.Voice.WER != 0 || len(t1.Failures) != 0 || len(t1.NodeCalls) != 1 {
		t.Fatalf("turn 2 %+v", t1)
	}
	// command-center got the transcript, not the script, and transcriptions name the
	// conversation (its speaker pass is recorded there, as on a Pi).
	if jsonText(cc.commands) != `["open Omerite","set a timer for 3 minutes"]` {
		t.Fatalf("voice commands %v", cc.commands)
	}
	if len(cc.convs) != 2 || cc.convs[0] != l.ConversationID {
		t.Fatalf("transcribe conversation ids %v, want %s", cc.convs, l.ConversationID)
	}
	if v := t0.Voice; math.Abs(v.AudioSeconds-float64(len("open Obsidian")+1)/32000) > 1e-9 || len(v.WAV) == 0 {
		t.Fatalf("audio %+v", v)
	}
	var out bytes.Buffer
	printLog(&out, l, false)
	if !strings.Contains(out.String(), `heard: "open Omerite" (WER 0.50)`) {
		t.Fatalf("transcript:\n%s", out.String())
	}
	// The scenario's text, unvoiced, goes straight through; a hearing expectation then fails.
	cc.commands = nil
	sc.Voice = false
	l = d.Run(context.Background(), Scenario{Name: "text", Turns: []Turn{{Say: "open Obsidian", Expect: Expect{Heard: "x"}}}})
	if l.Pass || l.Turns[0].Voice != nil || jsonText(cc.commands) != `["open Obsidian"]` || !strings.Contains(l.Turns[0].Failures[0], "not a voice turn") {
		t.Fatalf("text turn %+v, commands %v", l.Turns[0], cc.commands)
	}
	// --voice voices a text scenario.
	d.Voice = true
	if l := d.Run(context.Background(), Scenario{Name: "forced", Turns: []Turn{{Say: "hello"}}}); l.Turns[0].Voice == nil || !l.Pass {
		t.Fatalf("--voice %+v", l.Turns[0])
	}
}

func TestVoiceTurnWaitsForStartingEngine(t *testing.T) {
	old, oldWait := engineRetry, engineWait
	engineRetry, engineWait = time.Millisecond, time.Second
	defer func() { engineRetry, engineWait = old, oldWait }()
	cc := &fakeVoiceCC{fakeCC: &fakeCC{}, notReady: 2}
	d := voiceDriver(t, cc)
	d.Voice = true
	if l := d.Run(context.Background(), Scenario{Name: "s", Turns: []Turn{{Say: "hello"}}}); !l.Pass || l.Turns[0].Voice.Heard != "hello" {
		t.Fatalf("%+v", l.Turns[0])
	}
	// Nothing heard is the turn's error; command-center is not called.
	cc.mishear = map[string]string{"hello": ""}
	cc.commands = nil
	l := d.Run(context.Background(), Scenario{Name: "s", Turns: []Turn{{Say: "hello"}}})
	if l.Pass || l.Turns[0].Error != "stt: heard nothing" || len(cc.commands) != 0 {
		t.Fatalf("%+v commands %v", l.Turns[0], cc.commands)
	}
}

func TestVoiceTurnWithoutVoiceModels(t *testing.T) {
	d := voiceDriver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/media/tts/speak" {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"detail":"tts: no voice model installed"}`)
			return
		}
		io.WriteString(w, `{"status":"success"}`)
	}))
	d.Voice = true
	l := d.Run(context.Background(), Scenario{Name: "s", Turns: []Turn{{Say: "hello"}}})
	if e := l.Turns[0].Error; l.Pass || !strings.HasPrefix(e, "tts: ") || !strings.HasSuffix(e, "(is the sandbox up with --voice?)") {
		t.Fatalf("%q", e)
	}
}

func TestAdminVoiceLogsInOnce(t *testing.T) {
	var logins, samples int
	var lastVoice, lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			logins++
			io.WriteString(w, `{"access_token":"jwt"}`)
		case "/api/tts/sample":
			samples++
			var body struct{ Voice, Text string }
			json.NewDecoder(r.Body).Decode(&body)
			lastVoice, lastAuth = body.Voice, r.Header.Get("Authorization")
			w.Write(textWAV(body.Text))
		}
	}))
	defer srv.Close()
	a := &AdminVoice{AdminURL: srv.URL, AuthURL: srv.URL, Email: "e", Password: "p"}
	for range 2 {
		if _, err := a.Speak(context.Background(), "af_heart", "hi"); err != nil {
			t.Fatal(err)
		}
	}
	if logins != 1 || samples != 2 || lastVoice != "af_heart" || lastAuth != "Bearer jwt" {
		t.Fatalf("logins %d samples %d voice %q auth %q", logins, samples, lastVoice, lastAuth)
	}
	if _, err := a.Speak(context.Background(), "af_heart", strings.Repeat("x", 201)); err == nil {
		t.Fatal("over the preview's cap")
	}
	// A scenario's voice name goes to the admin; the house voice to the node route.
	cc := &fakeVoiceCC{fakeCC: &fakeCC{}}
	d := voiceDriver(t, cc)
	d.Voices = a
	l := d.Run(context.Background(), Scenario{Name: "v", Voice: true, VoiceName: "bf_emma", Turns: []Turn{{Say: "hello"}}})
	if !l.Pass || lastVoice != "bf_emma" || l.Turns[0].Voice.Voice != "bf_emma" {
		t.Fatalf("voice name: %+v, admin got %q", l.Turns[0], lastVoice)
	}
}

func TestWordErrorRate(t *testing.T) {
	for _, c := range []struct {
		said, heard string
		want        float64
	}{
		{"set a timer for 12 minutes", "Set a timer for 12 minutes.", 0},
		{"what's the time", "What’s the time?", 0},
		{"open Obsidian", "Open Omerite.", 0.5},
		{"turn off the oven", "turn off the oven light", 0.25},
		{"set a timer for 12 minutes", "set a timer for twelve minutes", 1.0 / 6},
		{"hello", "", 1},
		{"", "", 0},
	} {
		if got := wordErrorRate(c.said, c.heard); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("WER(%q, %q) = %v, want %v", c.said, c.heard, got, c.want)
		}
	}
}

func TestWAVSeconds(t *testing.T) {
	if s := wavSeconds(textWAV(strings.Repeat("ab", 16000))); s != 1 {
		t.Fatalf("%v", s)
	}
	if wavSeconds([]byte("nope")) != 0 {
		t.Fatal("not a wav")
	}
}

func TestVoiceScenariosLoad(t *testing.T) {
	b, err := os.ReadFile("scenarios/voice.json")
	if err != nil {
		t.Fatal(err)
	}
	var scs []Scenario
	if err := json.Unmarshal(b, &scs); err != nil || len(scs) == 0 {
		t.Fatalf("%d, %v", len(scs), err)
	}
	for _, sc := range scs {
		if !sc.Voice {
			t.Errorf("%s: voice.json scenarios are voiced", sc.Name)
		}
	}
}
