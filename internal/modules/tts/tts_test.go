package tts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- fakes ---

type fakeAuth struct{ fail bool }

func (f fakeAuth) ValidateApp(_ context.Context, id, key string) (authn.App, bool, error) {
	if f.fail {
		return authn.App{}, false, errors.New("auth down")
	}
	return authn.App{ID: id}, id == "app" && key == "secret", nil
}
func (fakeAuth) ValidateNode(context.Context, string, string, string) (authn.NodeValidation, error) {
	return authn.NodeValidation{}, nil
}
func (fakeAuth) HouseholdRole(context.Context, int64, string) (authn.Role, bool, error) {
	return "", false, nil
}

// fakeSynth renders 100 samples of 0.25 per character, after delay, and records what it did.
type fakeSynth struct {
	key    engineKey
	delay  time.Duration
	mu     sync.Mutex
	texts  []string
	sids   []int
	speeds []float32
	closed atomic.Bool
	active atomic.Int32
	maxAct atomic.Int32
}

func (f *fakeSynth) Generate(text string, sid int, speed float32) ([]float32, error) {
	if f.closed.Load() {
		panic("generate on a closed engine")
	}
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		m := f.maxAct.Load()
		if n <= m || f.maxAct.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(f.delay)
	f.mu.Lock()
	f.texts, f.sids, f.speeds = append(f.texts, text), append(f.sids, sid), append(f.speeds, speed)
	f.mu.Unlock()
	if strings.Trim(text, ".!? ") == "" {
		return nil, errors.New("sherpa-onnx: generation produced no audio")
	}
	out := make([]float32, 100*len(text))
	for i := range out {
		out[i] = 0.25
	}
	return out, nil
}
func (f *fakeSynth) SampleRate() int  { return 24000 }
func (f *fakeSynth) NumSpeakers() int { return 54 }
func (f *fakeSynth) Close()           { f.closed.Store(true) }

func (f *fakeSynth) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// loader hands out fakeSynths and remembers them.
type loader struct {
	mu    sync.Mutex
	made  []*fakeSynth
	fail  atomic.Bool
	delay time.Duration
}

func (l *loader) load(k engineKey) (synth, error) {
	if l.fail.Load() {
		return nil, errors.New("load failed")
	}
	s := &fakeSynth{key: k, delay: l.delay}
	l.mu.Lock()
	l.made = append(l.made, s)
	l.mu.Unlock()
	return s, nil
}

func (l *loader) all() []*fakeSynth {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*fakeSynth(nil), l.made...)
}

type pathFunc struct {
	mu   sync.Mutex
	path string
	ok   bool
}

func (p *pathFunc) ModelPath(_ context.Context, kind string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if kind != "tts" {
		return "", false
	}
	return p.path, p.ok
}

func (p *pathFunc) set(path string, ok bool) {
	p.mu.Lock()
	p.path, p.ok = path, ok
	p.mu.Unlock()
}

type env struct {
	m   *Module
	h   http.Handler
	l   *loader
	p   *pathFunc
	dir string
}

// kokoroDir makes a fake model directory with both English lexicons.
func kokoroDir(t *testing.T) string {
	dir := t.TempDir()
	for _, f := range []string{"lexicon-gb-en.txt", "lexicon-us-en.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func setup(t *testing.T, m *Module) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &env{m: m, l: &loader{}, p: &pathFunc{}, dir: kokoroDir(t)}
	e.p.set(e.dir, true)
	if m.Auth == nil {
		m.Auth = fakeAuth{}
	}
	if m.Models == nil {
		m.Models = e.p
	}
	m.newSynth = e.l.load
	m.NoWarm = true
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e.h = mux
	return e
}

var appH = map[string]string{"X-Jarvis-App-Id": "app", "X-Jarvis-App-Key": "secret"}

func (e *env) do(t *testing.T, method, path, body string, hs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	for k, v := range hs {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func jsonOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not JSON (%d): %q", rec.Code, rec.Body.String())
	}
	return v
}

// --- unit tests ---

func TestSplitSentences(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"Hello world.", []string{"Hello world."}},
		{"One. Two! Three? Four… Five", []string{"One.", "Two!", "Three?", "Four…", "Five"}},
		{"  It's 7.45 p.m. now.\n\nNext line.  ", []string{"It's 7.45 p.m.", "now.", "Next line."}},
		{"Dr. Smith is in.", []string{"Dr.", "Smith is in."}}, // legacy splits abbreviations too
		{"No punctuation here", []string{"No punctuation here"}},
		{"Wait...  what?!  Yes.", []string{"Wait...", "what?!", "Yes."}},
		{"3.14 is pi.", []string{"3.14 is pi."}},
		{"   ", nil},
	} {
		if got := splitSentences(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("split(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVoiceByName(t *testing.T) {
	v, ok := voiceByName("bm_george")
	if !ok || v.SID != 26 || v.Lexicon != "lexicon-gb-en.txt" || v.Lang != "en" {
		t.Fatalf("bm_george: %+v %v", v, ok)
	}
	v, ok = voiceByName(" AF_Heart ")
	if !ok || v.SID != 3 || v.Lexicon != "lexicon-us-en.txt" || v.Lang != "en-us" {
		t.Fatalf("af_heart: %+v %v", v, ok)
	}
	if v, _ := voiceByName("bm_fable"); v.SID != 25 {
		t.Fatalf("bm_fable sid %d", v.SID)
	}
	if v, _ := voiceByName("em_santa"); v.SID != 53 {
		t.Fatalf("em_santa sid %d", v.SID)
	}
	if _, ok := voiceByName("nope"); ok {
		t.Fatal("unknown voice resolved")
	}
	if len(Voices()) != 54 {
		t.Fatalf("voices: %d", len(Voices()))
	}
}

func TestPCM16Gain(t *testing.T) {
	b := pcm16([]float32{0.25, -0.25, 0.75, -1}, 2)
	want := []int16{16384, -16384, 32767, -32768}
	for i, w := range want {
		got := int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
		if d := int(got) - int(w); d < -1 || d > 1 {
			t.Errorf("sample %d: %d want %d", i, got, w)
		}
	}
}

func TestPingHealth(t *testing.T) {
	e := setup(t, &Module{Version: "1.2.3"})
	if rec := e.do(t, "GET", "/ping", "", nil); rec.Code != 200 || jsonOf(t, rec)["message"] != "pong" {
		t.Fatalf("ping %d %s", rec.Code, rec.Body)
	}
	rec := e.do(t, "GET", "/health", "", nil)
	if v := jsonOf(t, rec); rec.Code != 200 || v["status"] != "healthy" || v["version"] != "1.2.3" {
		t.Fatalf("health %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "POST", "/generate-wake-response", "{}", appH); rec.Code != 404 && rec.Code != 405 {
		t.Fatalf("cut wake route: %d", rec.Code)
	}
}

func TestAppAuth(t *testing.T) {
	e := setup(t, &Module{})
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/audio/format", ""},
		{"POST", "/speak", `{"text":"hi"}`},
		{"POST", "/speak/stream", `{"text":"hi"}`},
	} {
		for _, c := range []struct {
			hs     map[string]string
			code   int
			detail string
		}{
			{nil, 401, "Missing app credentials"},
			{map[string]string{"X-Jarvis-App-Id": "app"}, 401, "Missing app credentials"},
			{map[string]string{"X-Jarvis-App-Id": "app", "X-Jarvis-App-Key": "wrong"}, 401, "Invalid app credentials"},
		} {
			rec := e.do(t, rt.method, rt.path, rt.body, c.hs)
			if rec.Code != c.code || jsonOf(t, rec)["detail"] != c.detail {
				t.Errorf("%s %s %v: %d %s", rt.method, rt.path, c.hs, rec.Code, rec.Body)
			}
		}
	}
	down := setup(t, &Module{Auth: fakeAuth{fail: true}})
	if rec := down.do(t, "GET", "/audio/format", "", appH); rec.Code != 502 {
		t.Fatalf("auth down: %d", rec.Code)
	}
}

func TestAudioFormat(t *testing.T) {
	e := setup(t, &Module{})
	rec := e.do(t, "GET", "/audio/format", "", appH)
	v := jsonOf(t, rec)
	if rec.Code != 200 || v["sample_rate"] != 24000.0 || v["channels"] != 1.0 || v["sample_width"] != 2.0 || v["provider"] != "kokoro" {
		t.Fatalf("%d %v", rec.Code, v)
	}
	if len(e.l.all()) != 0 {
		t.Fatal("/audio/format loaded the engine")
	}
}

func TestEmptyText(t *testing.T) {
	e := setup(t, &Module{})
	for _, path := range []string{"/speak", "/speak/stream"} {
		for _, body := range []string{`{"text":""}`, `{}`, `{"text":null}`} {
			rec := e.do(t, "POST", path, body, appH)
			if rec.Code != 200 || jsonOf(t, rec)["error"] != "No text provided" {
				t.Errorf("%s %s: %d %s", path, body, rec.Code, rec.Body)
			}
		}
		if rec := e.do(t, "POST", path, `{"text":42}`, appH); rec.Code != 422 {
			t.Errorf("%s non-string: %d", path, rec.Code)
		}
		if rec := e.do(t, "POST", path, `not json`, appH); rec.Code != 422 {
			t.Errorf("%s bad json: %d", path, rec.Code)
		}
	}
	if len(e.l.all()) != 0 {
		t.Fatal("empty text loaded the engine")
	}
}

func TestSpeakWAV(t *testing.T) {
	e := setup(t, &Module{})
	rec := e.do(t, "POST", "/speak", `{"text":"Hello there. General Kenobi!"}`, appH)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	samples, rate, err := audio.ReadWAV(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || rate != 24000 {
		t.Fatalf("wav: rate %d err %v", rate, err)
	}
	if want := 100 * (len("Hello there.") + len("General Kenobi!")); len(samples) != want {
		t.Fatalf("samples %d want %d", len(samples), want)
	}
	if s := samples[0]; s < 0.49 || s > 0.51 { // 0.25 × default gain 2.0
		t.Fatalf("gain not applied: %v", s)
	}
	s := e.l.all()[0]
	if got := s.calls(); !reflect.DeepEqual(got, []string{"Hello there.", "General Kenobi!"}) {
		t.Fatalf("calls %q", got)
	}
	if s.sids[0] != 26 || s.speeds[0] != 1.25 || s.key.Lexicon != "lexicon-gb-en.txt" || s.key.Lang != "en" || s.key.Dir != e.dir {
		t.Fatalf("sid %d speed %v key %+v", s.sids[0], s.speeds[0], s.key)
	}
}

func TestSpeakStreamHTTP(t *testing.T) {
	e := setup(t, &Module{})
	e.l.delay = 50 * time.Millisecond
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/speak/stream", strings.NewReader(`{"text":"One. Two. Three."}`))
	for k, v := range appH {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/raw" ||
		resp.Header.Get("X-Audio-Sample-Rate") != "24000" || resp.Header.Get("X-Audio-Channels") != "1" ||
		resp.Header.Get("X-Audio-Sample-Width") != "2" || resp.Header.Get("X-Audio-Provider") != "kokoro" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if len(resp.TransferEncoding) == 0 || resp.TransferEncoding[0] != "chunked" {
		t.Fatalf("not chunked: %v", resp.TransferEncoding)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 2*100*(4+4+6) || bytes.HasPrefix(body, []byte("RIFF")) {
		t.Fatalf("body %d bytes", len(body))
	}
}

func TestSpeakStreamsPerSentence(t *testing.T) {
	e := setup(t, &Module{})
	e.l.delay = 100 * time.Millisecond
	start := time.Now()
	st, err := e.m.Speak(context.Background(), "Alpha. Beta gamma. Delta!", SpeakOptions{Voice: "af_heart", Speed: 1.0})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Format != (Format{24000, 1, 2, "kokoro"}) {
		t.Fatalf("format %+v", st.Format)
	}
	var sizes []int
	var firstAt time.Duration
	for {
		b, err := st.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if sizes == nil {
			firstAt = time.Since(start)
		}
		sizes = append(sizes, len(b))
	}
	total := time.Since(start)
	if !reflect.DeepEqual(sizes, []int{2 * 100 * 6, 2 * 100 * 11, 2 * 100 * 6}) {
		t.Fatalf("chunks %v", sizes)
	}
	if firstAt > total/2 {
		t.Fatalf("first chunk at %v of %v: not streamed", firstAt, total)
	}
	s := e.l.all()[0]
	if s.sids[0] != 3 || s.speeds[0] != 1.0 || s.key.Lexicon != "lexicon-us-en.txt" || s.key.Lang != "en-us" {
		t.Fatalf("sid %d speed %v key %+v", s.sids[0], s.speeds[0], s.key)
	}
}

func TestSpeakReader(t *testing.T) {
	e := setup(t, &Module{})
	st, err := e.m.Speak(context.Background(), "Hi. ... Bye.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(bufio.NewReaderSize(st, 16))
	if err != nil || len(b) != 2*100*(3+4) { // "..." renders nothing and is skipped
		t.Fatalf("read %d err %v", len(b), err)
	}
	if _, err := e.m.Speak(context.Background(), "", SpeakOptions{}); !errors.Is(err, ErrNoText) {
		t.Fatalf("empty: %v", err)
	}
}

func TestSettingsDriveVoice(t *testing.T) {
	e := setup(t, &Module{})
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.m.settings.Set(ctx, "tts.kokoro_voice", "bm_fable", settings.Scope{}))
	must(e.m.settings.Set(ctx, "tts.kokoro_speed", 0.9, settings.Scope{}))
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	s := e.l.all()[0]
	if s.sids[0] != 25 || s.speeds[0] != float32(0.9) {
		t.Fatalf("sid %d speed %v", s.sids[0], s.speeds[0])
	}
	// Same language: same engine. American voice: reload with the US lexicon.
	must(e.m.settings.Set(ctx, "tts.kokoro_voice", "bm_george", settings.Scope{}))
	e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH)
	if n := len(e.l.all()); n != 1 {
		t.Fatalf("reloaded for a same-language voice: %d engines", n)
	}
	must(e.m.settings.Set(ctx, "tts.kokoro_voice", "am_adam", settings.Scope{}))
	e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH)
	all := e.l.all()
	if len(all) != 2 || all[1].key.Lexicon != "lexicon-us-en.txt" || !all[0].closed.Load() {
		t.Fatalf("engines %d", len(all))
	}
	// Unknown voice: default voice, not an error.
	must(e.m.settings.Set(ctx, "tts.kokoro_voice", "xx_nobody", settings.Scope{}))
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if last := e.l.all()[2]; last.sids[0] != 26 {
		t.Fatalf("fallback sid %d", last.sids[0])
	}
}

func TestNotInstalled(t *testing.T) {
	e := setup(t, &Module{})
	e.p.set("", false)
	for _, path := range []string{"/speak", "/speak/stream"} {
		rec := e.do(t, "POST", path, `{"text":"Hi."}`, appH)
		if rec.Code != 503 || jsonOf(t, rec)["detail"] != "TTS model not installed" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := e.do(t, "GET", "/audio/format", "", appH); rec.Code != 200 {
		t.Fatalf("format: %d", rec.Code)
	}
	// Installed later: picked up without a restart.
	e.p.set(e.dir, true)
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 {
		t.Fatalf("after install: %d", rec.Code)
	}
	// Uninstalled while loaded: the loaded engine keeps serving.
	e.p.set("", false)
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 {
		t.Fatalf("after uninstall: %d", rec.Code)
	}
}

func TestReloadOnModelChange(t *testing.T) {
	e := setup(t, &Module{})
	e.l.delay = 30 * time.Millisecond
	st, err := e.m.Speak(context.Background(), "One. Two. Three.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Next(); err != nil {
		t.Fatal(err)
	}
	// The model changes mid-stream: new requests get a new engine; the old one finishes this
	// stream and only then closes.
	dir2 := kokoroDir(t)
	e.p.set(dir2, true)
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	all := e.l.all()
	if len(all) != 2 || all[1].key.Dir != dir2 {
		t.Fatalf("engines %+v", all)
	}
	if all[0].closed.Load() {
		t.Fatal("old engine closed under a live stream")
	}
	if _, err := io.ReadAll(st); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !all[0].closed.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !all[0].closed.Load() {
		t.Fatal("old engine never closed")
	}
	if len(all[0].calls()) != 3 {
		t.Fatalf("old stream sentences: %q", all[0].calls())
	}
}

func TestFailedReloadKeepsPrevious(t *testing.T) {
	e := setup(t, &Module{})
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	e.l.fail.Store(true)
	e.p.set(kokoroDir(t), true)
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 {
		t.Fatalf("failed reload: %d", rec.Code)
	}
	if n := len(e.l.all()[0].calls()); n != 2 {
		t.Fatalf("previous engine calls %d", n)
	}
	// First load failing with nothing to fall back on is a 500.
	f := setup(t, &Module{})
	f.l.fail.Store(true)
	if rec := f.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 500 {
		t.Fatalf("first load failure: %d", rec.Code)
	}
}

func TestConcurrencyCapAndCancel(t *testing.T) {
	e := setup(t, &Module{MaxConcurrent: 1})
	e.l.delay = 20 * time.Millisecond
	st, err := e.m.Speak(context.Background(), "One. Two. Three. Four.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := e.m.Speak(ctx, "Blocked.", SpeakOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second stream: %v", err)
	}
	st.Close() // frees the slot after at most the sentence in flight
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	st2, err := e.m.Speak(ctx2, "Now.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(st2)
	if n := len(e.l.all()[0].calls()); n >= 5 {
		t.Fatalf("closed stream kept rendering: %d calls", n)
	}
}

func TestParallelStreams(t *testing.T) {
	e := setup(t, &Module{MaxConcurrent: 3})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := e.m.Speak(context.Background(), "A b. C d. E f.", SpeakOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(st)
			if len(b) != 2*100*12 {
				t.Errorf("got %d bytes", len(b))
			}
		}()
	}
	wg.Wait()
	if n := len(e.l.all()); n != 1 {
		t.Fatalf("%d engines loaded", n)
	}
}
