package stt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOpenAI is an OpenAI-compatible /audio/transcriptions double: it records each request
// as received and plays a canned answer.
type fakeOpenAI struct {
	srv *httptest.Server

	mu       sync.Mutex
	reqs     []openAIReq
	status   int    // 0 = 200
	response string // "" = {"text":"turn off the lights"}
	delay    time.Duration
}

type openAIReq struct {
	method, path, query, auth, xiKey, contentType string
	fields                                        map[string]string
	names                                         []string // form part names, in order
	file                                          []byte
	filename                                      string
	raw                                           []byte
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	q := openAIReq{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"),
		xiKey:       r.Header.Get("xi-api-key"),
		contentType: r.Header.Get("Content-Type"), fields: map[string]string{}, raw: raw}
	if _, params, err := mime.ParseMediaType(q.contentType); err == nil {
		mr := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			q.names = append(q.names, p.FormName())
			if p.FileName() != "" {
				q.file, q.filename = b, p.FileName()
			} else {
				q.fields[p.FormName()] = string(b)
			}
		}
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, q)
	status, body, delay := f.status, f.response, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status == 0 {
		status = 200
	}
	if body == "" {
		body = `{"text":" turn off the lights\n"}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func (f *fakeOpenAI) last(t *testing.T) openAIReq {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no request reached the provider")
	}
	return f.reqs[len(f.reqs)-1]
}

const testKey = "sk-test-0123456789abcdef"

// remote points the module's stt label at the fake, as engine=remote does.
func (f *fakeOpenAI) remote(key string, timeout time.Duration) STTEngine {
	return ResolveFunc(func(context.Context, string) (Endpoint, error) {
		return Endpoint{BaseURL: f.srv.URL + "/v1", Remote: true, API: APIOpenAI, APIKey: key, Model: "gpt-transcribe", Timeout: timeout}, nil
	})
}

func TestRemoteOpenAIRequest(t *testing.T) {
	f := newFakeOpenAI(t)
	e := setup(t, func(m *Module) { m.Engine = f.remote(testKey, 0) })
	e.set(t, "whisper.language", "en", "")
	in := wavOf(sine(48000, 1.5, 330), 48000)
	samples, _ := DecodeWAV(in)
	res, err := e.m.Transcribe(e.ctx, in, TranscribeOptions{Prompt: "Jarvis", BeamSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last(t)
	if q.method != http.MethodPost || q.path != "/v1/audio/transcriptions" || q.auth != "Bearer "+testKey ||
		!strings.HasPrefix(q.contentType, "multipart/form-data; boundary=") {
		t.Fatalf("request %s %s auth=%q ct=%q", q.method, q.path, q.auth, q.contentType)
	}
	want := map[string]string{"model": "gpt-transcribe", "response_format": "json", "language": "en", "prompt": "Jarvis"}
	if len(q.fields) != len(want) {
		t.Errorf("fields %v, want %v", q.fields, want)
	}
	for k, v := range want {
		if q.fields[k] != v {
			t.Errorf("%s = %q, want %q", k, q.fields[k], v)
		}
	}
	if !slices.Equal(q.names, []string{"file", "model", "response_format", "language", "prompt"}) {
		t.Errorf("parts %v", q.names)
	}
	// The file is the 16 kHz WAV whisper-server would get, byte for byte.
	if q.filename != "audio.wav" || !bytes.Equal(q.file, EncodeWAV(samples)) || len(samples) != 24000 {
		t.Errorf("file %q: %d bytes", q.filename, len(q.file))
	}
	for _, s := range []string{"temperature", "beam_size", "verbose_json"} {
		if bytes.Contains(q.raw, []byte(s)) {
			t.Errorf("request carries %s", s)
		}
	}
	// json has no timings: one segment spanning the clip.
	if res.Text != "turn off the lights" || len(res.Segments) != 1 || res.Segments[0] != (Segment{T0Ms: 0, T1Ms: len(samples) * 1000 / SampleRate, Text: "turn off the lights"}) {
		t.Fatalf("result %+v", res)
	}
}

// No key (a LAN server) sends no Authorization header; no prompt sends no prompt part; silence
// is no segments.
func TestRemoteOpenAIMinimal(t *testing.T) {
	f := newFakeOpenAI(t)
	f.response = `{"text":""}`
	e := setup(t, func(m *Module) { m.Engine = f.remote("", 0) })
	res, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last(t)
	if q.auth != "" || q.fields["prompt"] != "" || slices.Contains(q.names, "prompt") {
		t.Errorf("auth %q fields %v", q.auth, q.fields)
	}
	if res.Text != "" || res.Segments == nil || len(res.Segments) != 0 {
		t.Errorf("silence %+v", res)
	}
}

// Every failure is an EngineError (the legacy 500), and none carries the key, even when the
// provider echoes it.
func TestRemoteOpenAIErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		delay  time.Duration
		want   string
	}{
		{"401", 401, `{"error":{"message":"Incorrect API key provided: ` + testKey + `"}}`, 0, "refused the API key"},
		{"403", 403, `{"error":"forbidden ` + testKey + `"}`, 0, "refused the API key"},
		{"429", 429, `{"error":{"message":"Rate limit reached"}}`, 0, "429"},
		{"500 echoing the key", 500, `upstream failed for key ` + testKey, 0, "500"},
		{"400", 400, `{"error":{"message":"Invalid file format."}}`, 0, "Invalid file format"},
		{"not json", 200, `<html>proxy</html>`, 0, "unreadable"},
		{"timeout", 200, "", 2 * time.Second, "no answer within 50ms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeOpenAI(t)
			f.status, f.response, f.delay = c.status, c.body, c.delay
			e := setup(t, func(m *Module) { m.Engine = f.remote(testKey, 50*time.Millisecond) })
			start := time.Now()
			_, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
			var ee *EngineError
			if !errors.As(err, &ee) {
				t.Fatalf("%T %v", err, err)
			}
			if strings.Contains(ee.Msg, testKey) || strings.Contains(ee.Stderr, testKey) || strings.Contains(err.Error(), testKey) {
				t.Fatalf("key in error: %q / %q", ee.Msg, ee.Stderr)
			}
			if !strings.Contains(ee.Msg, c.want) || !strings.HasPrefix(ee.Msg, "Remote transcription failed: ") {
				t.Fatalf("message %q, want %q", ee.Msg, c.want)
			}
			if c.delay > 0 && time.Since(start) > time.Second {
				t.Fatalf("timeout not applied: %s", time.Since(start))
			}
		})
	}
	// Over HTTP it is the legacy 500 {"error", "stderr"}.
	f := newFakeOpenAI(t)
	f.status, f.response = 503, `overloaded`
	e := setup(t, func(m *Module) { m.Engine = f.remote(testKey, 0) })
	if c, body := e.post(t, "/transcribe", nil, file("file", sineWAV(16000, 1, 440))); c != 500 || !strings.Contains(body["error"].(string), "503: overloaded") {
		t.Errorf("%d %v", c, body)
	}
}

// Speaker recognition stays local whatever the engine: a remote transcription still names the
// enrolled speaker, and the provider never sees the speaker clip.
func TestRemoteOpenAIKeepsLocalSpeakerRecognition(t *testing.T) {
	f := newFakeOpenAI(t)
	e := setup(t, func(m *Module) { m.Engine = f.remote(testKey, 0) })
	e.enroll(t, "hh", 7, sineWAV(16000, 4, 330))
	e.set(t, "voice.recognition_enabled", true, "hh")
	res, err := e.m.Transcribe(e.ctx, sineWAV(16000, 2, 440), TranscribeOptions{
		Speaker: &SpeakerScope{HouseholdID: "hh", AllMembers: true}, SpeakerAudio: sineWAV(16000, 2, 330),
	})
	if err != nil || res.Text != "turn off the lights" || res.Speaker.UserID == nil || *res.Speaker.UserID != 7 || res.Speaker.Outcome != OutcomeMatched {
		t.Fatalf("%+v %v", res, err)
	}
	f.mu.Lock()
	n := len(f.reqs)
	f.mu.Unlock()
	if q := f.last(t); n != 1 || !bytes.Equal(q.file, EncodeWAV(sine(16000, 2, 440))) {
		t.Errorf("provider got %d requests; file is the transcribed clip: %v", n, bytes.Equal(q.file, EncodeWAV(sine(16000, 2, 440))))
	}
	if _, _, n := e.w.last(); n != 0 {
		t.Error("the local whisper-server was called too")
	}
}
