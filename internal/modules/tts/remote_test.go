package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// fakeProvider is an OpenAI-compatible /v1/audio/speech. reply answers each request (its
// index, the request); the default plays pcmFor(input).
type fakeProvider struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []recorded
	reply func(i int, w http.ResponseWriter, r *http.Request, body map[string]any)
}

type recorded struct {
	method, path, auth, ctype string
	raw                       string
	body                      map[string]any
}

// pcmFor is a recognisable s16le body for text: 50 samples per character, valued len(text).
func pcmFor(text string) []byte {
	out := make([]byte, 100*len(text))
	for i := 0; i < len(out); i += 2 {
		binary.LittleEndian.PutUint16(out[i:], uint16(len(text)))
	}
	return out
}

func newFakeProvider(t *testing.T) *fakeProvider {
	f := &fakeProvider{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		i := len(f.reqs)
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(raw), body})
		reply := f.reply
		f.mu.Unlock()
		if reply != nil {
			reply(i, w, r, body)
			return
		}
		input, _ := body["input"].(string)
		_, _ = w.Write(pcmFor(input))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// remoteSetup is a module with tts.provider=remote at f, and no Kokoro model installed.
func remoteSetup(t *testing.T, f *fakeProvider, extra map[string]any) *env {
	t.Helper()
	e := setup(t, &Module{})
	e.p.set("", false)
	vals := map[string]any{
		"tts.provider": "remote", "tts.remote_url": f.srv.URL + "/v1/", "tts.remote_model": "gpt-4o-mini-tts",
		"tts.remote_voice": "marin", "tts.remote_api_key": "sk-test",
	}
	for k, v := range extra {
		vals[k] = v
	}
	for k, v := range vals {
		if err := e.m.settings.Set(context.Background(), k, v, settings.Scope{}); err != nil {
			t.Fatal(k, err)
		}
	}
	return e
}

func readAll(t *testing.T, st *Stream) ([][]byte, error) {
	t.Helper()
	var chunks [][]byte
	for {
		b, err := st.Next()
		if errors.Is(err, io.EOF) {
			return chunks, nil
		}
		if err != nil {
			return chunks, err
		}
		chunks = append(chunks, b)
	}
}

func TestRemoteRequestAndPassthrough(t *testing.T) {
	f := newFakeProvider(t)
	e := remoteSetup(t, f, nil)
	st, err := e.m.Speak(context.Background(), "Hello there. How are you?", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if want := (Format{24000, 1, 2, "remote"}); st.Format != want {
		t.Fatalf("format %+v", st.Format)
	}
	chunks, err := readAll(t, st)
	if err != nil {
		t.Fatal(err)
	}
	got := bytes.Join(chunks, nil)
	if want := append(pcmFor("Hello there."), pcmFor("How are you?")...); !bytes.Equal(got, want) {
		t.Fatalf("pcm: %d bytes, want %d (passthrough)", len(got), len(want))
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want one per sentence", len(reqs))
	}
	r := reqs[0]
	if r.method != "POST" || r.path != "/v1/audio/speech" || r.auth != "Bearer sk-test" || r.ctype != "application/json" {
		t.Fatalf("request %+v", r)
	}
	if want := `{"input":"Hello there.","model":"gpt-4o-mini-tts","response_format":"pcm","speed":1,"voice":"marin"}`; r.raw != want {
		t.Fatalf("body\n got %s\nwant %s", r.raw, want)
	}
	if in := reqs[1].body["input"]; in != "How are you?" {
		t.Fatalf("second input %v", in)
	}
	if n := len(e.l.all()); n != 0 {
		t.Fatalf("loaded %d Kokoro engines for a remote provider", n)
	}
}

func TestRemoteNoKeyNoAuthHeaderAndOptions(t *testing.T) {
	f := newFakeProvider(t)
	e := remoteSetup(t, f, map[string]any{"tts.remote_api_key": ""})
	st, err := e.m.Speak(context.Background(), "Hi.", SpeakOptions{Voice: "coral", Speed: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, st); err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	if r.auth != "" || r.body["voice"] != "coral" || r.body["speed"] != 1.5 {
		t.Fatalf("request %+v", r)
	}
}

// TestRemoteFormatWithoutKokoro: /audio/format and the stream's headers say 24 kHz remote with
// no Kokoro model installed, and the HTTP routes speak.
func TestRemoteFormatWithoutKokoro(t *testing.T) {
	f := newFakeProvider(t)
	e := remoteSetup(t, f, nil)
	if got := jsonOf(t, e.do(t, "GET", "/audio/format", "", appH)); got["sample_rate"] != 24000.0 || got["provider"] != "remote" {
		t.Fatalf("format %v", got)
	}
	rec := e.do(t, "POST", "/speak/stream", `{"text":"Hi."}`, appH)
	if rec.Code != 200 || rec.Header().Get("X-Audio-Sample-Rate") != "24000" || rec.Header().Get("X-Audio-Provider") != "remote" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if !bytes.Equal(rec.Body.Bytes(), pcmFor("Hi.")) {
		t.Fatalf("body %d bytes", rec.Body.Len())
	}
	if rec := e.do(t, "POST", "/speak", `{"text":"Hi."}`, appH); rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("/speak %d", rec.Code)
	}
}

func TestRemoteWithoutURLIsNotInstalled(t *testing.T) {
	f := newFakeProvider(t)
	e := remoteSetup(t, f, map[string]any{"tts.remote_url": ""})
	if _, err := e.m.Speak(context.Background(), "Hi.", SpeakOptions{}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err %v", err)
	}
	if rec := e.do(t, "POST", "/speak/stream", `{"text":"Hi."}`, appH); rec.Code != 503 {
		t.Fatalf("%d", rec.Code)
	}
}

// TestRemoteChunksSplitMidSample: a body flushed in odd-sized pieces still arrives as whole
// samples, unshifted.
func TestRemoteChunksSplitMidSample(t *testing.T) {
	f := newFakeProvider(t)
	want := make([]byte, 2000)
	for i := 0; i < len(want); i += 2 {
		binary.LittleEndian.PutUint16(want[i:], uint16(i*7))
	}
	f.reply = func(_ int, w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		rc := http.NewResponseController(w)
		for i := 0; i < len(want); {
			j := min(len(want), i+333)
			_, _ = w.Write(want[i:j])
			_ = rc.Flush()
			time.Sleep(2 * time.Millisecond)
			i = j
		}
	}
	e := remoteSetup(t, f, nil)
	st, err := e.m.Speak(context.Background(), "One sentence.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := readAll(t, st)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if len(c)%2 != 0 {
			t.Fatalf("chunk of %d bytes", len(c))
		}
	}
	if len(chunks) < 2 {
		t.Fatalf("%d chunks: the body was not streamed", len(chunks))
	}
	if got := bytes.Join(chunks, nil); !bytes.Equal(got, want) {
		t.Fatal("samples shifted or lost")
	}
}

// wavOf is a 16-bit WAV of frames frames at rate with channels, every sample v. streamSize
// writes the data size as a streaming server does (0xFFFFFFFF) instead of the real one.
func wavOf(rate, channels, frames int, v int16, streamSize bool) []byte {
	var b bytes.Buffer
	data := 2 * channels * frames
	size := uint32(data)
	if streamSize {
		size = 0xFFFFFFFF
	}
	le := func(x any) { _ = binary.Write(&b, binary.LittleEndian, x) }
	b.WriteString("RIFF")
	le(uint32(36 + data))
	b.WriteString("WAVE")
	b.WriteString("LIST")
	le(uint32(4))
	b.WriteString("INFO")
	b.WriteString("fmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(channels))
	le(uint32(rate))
	le(uint32(rate * channels * 2))
	le(uint16(channels * 2))
	le(uint16(16))
	b.WriteString("data")
	le(size)
	for i := 0; i < frames*channels; i++ {
		le(v)
	}
	return b.Bytes()
}

func TestRemoteWAVIsResampledTo24kMono(t *testing.T) {
	for _, tc := range []struct {
		name           string
		rate, channels int
		stream         bool
	}{{"22050 mono", 22050, 1, false}, {"48k stereo", 48000, 2, true}, {"24k mono", 24000, 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeProvider(t)
			body := wavOf(tc.rate, tc.channels, tc.rate/2, 1000, tc.stream) // 0.5 s
			f.reply = func(_ int, w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write(body[:7]) // the header itself split across reads
				http.NewResponseController(w).Flush()
				_, _ = w.Write(body[7:])
			}
			e := remoteSetup(t, f, nil)
			st, err := e.m.Speak(context.Background(), "Hi.", SpeakOptions{})
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := readAll(t, st)
			if err != nil {
				t.Fatal(err)
			}
			pcm := bytes.Join(chunks, nil)
			n := len(pcm) / 2
			if n < 11900 || n > 12100 {
				t.Fatalf("%d samples, want ~12000 (0.5 s at 24 kHz)", n)
			}
			// Past the filter's warm-up the level is the source's (no gain, no RIFF bytes as audio).
			if v := int16(binary.LittleEndian.Uint16(pcm[2*(n/2):])); v < 990 || v > 1010 {
				t.Fatalf("sample %d, want ~1000", v)
			}
		})
	}
}

func TestRemoteBadWAV(t *testing.T) {
	f := newFakeProvider(t)
	body := wavOf(24000, 1, 100, 1, false)
	binary.LittleEndian.PutUint16(body[12+12+8+14:], 8) // 8-bit
	f.reply = func(_ int, w http.ResponseWriter, _ *http.Request, _ map[string]any) { _, _ = w.Write(body) }
	e := remoteSetup(t, f, nil)
	st, _ := e.m.Speak(context.Background(), "Hi.", SpeakOptions{})
	if _, err := readAll(t, st); err == nil || !strings.Contains(err.Error(), "unsupported WAV") {
		t.Fatalf("err %v", err)
	}
}

// TestRemoteErrors: a refusal of the service or of us ends the stream with the error and
// sends no more sentences; a refusal of one sentence's text skips just that sentence.
func TestRemoteErrors(t *testing.T) {
	for _, tc := range []struct {
		code     int
		requests int
		audio    bool
	}{{401, 1, false}, {403, 1, false}, {429, 1, false}, {500, 1, false}, {503, 1, false}, {400, 2, true}, {422, 2, true}} {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			f := newFakeProvider(t)
			f.reply = func(i int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
				if i == 0 {
					http.Error(w, `{"error":{"message":"nope"}}`, tc.code)
					return
				}
				_, _ = w.Write(pcmFor(body["input"].(string)))
			}
			e := remoteSetup(t, f, nil)
			st, err := e.m.Speak(context.Background(), "First. Second.", SpeakOptions{})
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := readAll(t, st)
			if n := len(f.requests()); n != tc.requests {
				t.Fatalf("%d requests, want %d", n, tc.requests)
			}
			if tc.audio {
				if err != nil || !bytes.Equal(bytes.Join(chunks, nil), pcmFor("Second.")) {
					t.Fatalf("err %v, %d chunks", err, len(chunks))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(tc.code)) || !strings.Contains(err.Error(), "nope") {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestRemoteTimeout(t *testing.T) {
	f := newFakeProvider(t)
	release := make(chan struct{})
	defer close(release)
	f.reply = func(_ int, w http.ResponseWriter, r *http.Request, _ map[string]any) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	e := remoteSetup(t, f, map[string]any{"tts.remote_timeout_ms": 100})
	start := time.Now()
	st, err := e.m.Speak(context.Background(), "First. Second.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = readAll(t, st)
	if err == nil || !strings.Contains(err.Error(), "within 100ms") {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
	if n := len(f.requests()); n != 1 {
		t.Fatalf("%d requests after a timeout, want 1", n)
	}
}

// TestRemoteTimeoutIsIdleNotTotal: a body that trickles for longer than the timeout, with no
// gap as long, is not cut; nor is one whose reader is slower than the timeout.
func TestRemoteTimeoutIsIdleNotTotal(t *testing.T) {
	f := newFakeProvider(t)
	f.reply = func(_ int, w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		rc := http.NewResponseController(w)
		for i := 0; i < 6; i++ {
			_, _ = w.Write(make([]byte, 4800))
			_ = rc.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}
	e := remoteSetup(t, f, map[string]any{"tts.remote_timeout_ms": 150})
	st, err := e.m.Speak(context.Background(), "One.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for {
		b, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		total += len(b)
		time.Sleep(200 * time.Millisecond) // a reader slower than the timeout
	}
	if total != 6*4800 {
		t.Fatalf("%d bytes, want %d", total, 6*4800)
	}
}

// TestRemoteStreamsBeforeTheSentenceEnds: the first chunk reaches the caller while the first
// sentence's body is still open, and the second sentence is requested only after it.
func TestRemoteStreamsBeforeTheSentenceEnds(t *testing.T) {
	f := newFakeProvider(t)
	gate := make(chan struct{})
	f.reply = func(i int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
		_, _ = w.Write(pcmFor(body["input"].(string)))
		_ = http.NewResponseController(w).Flush()
		if i == 0 {
			<-gate
		}
	}
	e := remoteSetup(t, f, nil)
	st, err := e.m.Speak(context.Background(), "First. Second.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := st.Next()
	if err != nil || !bytes.Equal(first, pcmFor("First.")) {
		t.Fatalf("first chunk %d bytes, %v", len(first), err)
	}
	if n := len(f.requests()); n != 1 {
		t.Fatalf("%d requests before the first sentence ended", n)
	}
	close(gate)
	rest, err := readAll(t, st)
	if err != nil || !bytes.Equal(bytes.Join(rest, nil), pcmFor("Second.")) {
		t.Fatalf("rest %v", err)
	}
}

// TestRemoteCloseStopsRequests: closing mid-reply cancels the open request and sends no more.
func TestRemoteCloseStopsRequests(t *testing.T) {
	f := newFakeProvider(t)
	gone := make(chan struct{})
	f.reply = func(i int, w http.ResponseWriter, r *http.Request, body map[string]any) {
		_, _ = w.Write(pcmFor(body["input"].(string)))
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
		close(gone)
	}
	e := remoteSetup(t, f, nil)
	st, err := e.m.Speak(context.Background(), "First. Second. Third.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Next(); err != nil {
		t.Fatal(err)
	}
	st.Close()
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("the open request was not cancelled")
	}
	// The slot comes back and nothing else is requested.
	for i := 0; i < cap(e.m.sem); i++ {
		select {
		case e.m.sem <- struct{}{}:
		case <-time.After(2 * time.Second):
			t.Fatal("synthesis slot not released")
		}
	}
	if n := len(f.requests()); n != 1 {
		t.Fatalf("%d requests after Close", n)
	}
}

func TestVoiceChoices(t *testing.T) {
	ctx := context.Background()
	t.Run("kokoro", func(t *testing.T) {
		e := setup(t, &Module{})
		_ = e.m.settings.Set(ctx, "tts.kokoro_voice", "af_heart", settings.Scope{})
		v := e.m.VoiceChoices(ctx)
		if v.Provider != "kokoro" || len(v.Voices) != len(Voices()) || v.Current != "af_heart" || v.Default != DefaultVoice ||
			v.Setting != "tts/tts.kokoro_voice" || v.FreeText {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("openai", func(t *testing.T) {
		f := newFakeProvider(t)
		e := remoteSetup(t, f, map[string]any{"tts.remote_url": "https://api.openai.com/v1", "tts.remote_voice": ""})
		v := e.m.VoiceChoices(ctx)
		if v.Provider != "remote" || len(v.Voices) != len(openAIVoices) || v.Current != "marin" || v.Default != "marin" ||
			v.Setting != "tts/tts.remote_voice" || v.FreeText {
			t.Fatalf("%+v", v)
		}
	})
	for name, tc := range map[string]struct {
		code int
		body string
		want []string
	}{
		"kokoro-fastapi": {200, `{"voices":["af_heart","bm_george"]}`, []string{"af_heart", "bm_george"}},
		"objects":        {200, `{"data":[{"id":"a"},{"voice_id":"b","name":"Bee"},{"name":"c"},{"id":"a"}]}`, []string{"a", "b", "c"}},
		"list":           {200, `["x","y"]`, []string{"x", "y"}},
		"none":           {404, `{"detail":"Not Found"}`, nil},
		"junk":           {200, `<html>`, nil},
	} {
		t.Run("other "+name, func(t *testing.T) {
			f := newFakeProvider(t)
			f.reply = func(_ int, w http.ResponseWriter, r *http.Request, _ map[string]any) {
				if r.Method != "GET" || r.URL.Path != "/v1/audio/voices" || r.Header.Get("Authorization") != "Bearer sk-test" {
					t.Errorf("%s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}
			e := remoteSetup(t, f, nil)
			v := e.m.VoiceChoices(ctx)
			if v.Current != "marin" || v.Default != "" || v.FreeText != (tc.want == nil) || v.Voices == nil {
				t.Fatalf("%+v", v)
			}
			if tc.want != nil && strings.Join(v.Voices, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("voices %v", v.Voices)
			}
		})
	}
}
