package stt

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// elevenLabs points the module's stt label at the fake as the ElevenLabs preset does: the base
// has no version, the dialect's paths carry it.
func (f *fakeOpenAI) elevenLabs(base, key string, timeout time.Duration) STTEngine {
	return ResolveFunc(func(context.Context, string) (Endpoint, error) {
		return Endpoint{BaseURL: f.srv.URL + base, Remote: true, API: APIElevenLabs, APIKey: key, Model: "scribe_v2", Timeout: timeout}, nil
	})
}

// elevenLabsWords is a Scribe answer: two sentences, a spacing between words and an audio event.
const elevenLabsWords = `{"language_code":"eng","text":"Turn off the lights. Thanks!","words":[
{"text":"Turn","start":0.12,"end":0.3,"type":"word"},{"text":" ","start":0.3,"end":0.32,"type":"spacing"},
{"text":"off","start":0.32,"end":0.5,"type":"word"},{"text":" ","start":0.5,"end":0.52,"type":"spacing"},
{"text":"the","start":0.52,"end":0.6,"type":"word"},{"text":" ","start":0.6,"end":0.62,"type":"spacing"},
{"text":"lights.","start":0.62,"end":1.01,"type":"word"},{"text":" ","start":1.01,"end":1.1,"type":"spacing"},
{"text":"(laughs)","start":1.1,"end":1.3,"type":"audio_event"},{"text":" ","start":1.3,"end":1.31,"type":"spacing"},
{"text":"Thanks!","start":1.31,"end":1.48,"type":"word"}]}`

func TestRemoteElevenLabsRequest(t *testing.T) {
	f := newFakeOpenAI(t)
	f.response = elevenLabsWords
	e := setup(t, func(m *Module) { m.Engine = f.elevenLabs("", testKey, 0) })
	e.set(t, "whisper.language", "en", "")
	in := wavOf(sine(48000, 1.5, 330), 48000)
	samples, _ := DecodeWAV(in)
	res, err := e.m.Transcribe(e.ctx, in, TranscribeOptions{Prompt: "Jarvis", BeamSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last(t)
	if q.method != http.MethodPost || q.path != "/v1/speech-to-text" || q.query != "enable_logging=false" ||
		!strings.HasPrefix(q.contentType, "multipart/form-data; boundary=") {
		t.Fatalf("request %s %s?%s ct=%q", q.method, q.path, q.query, q.contentType)
	}
	// The key goes in xi-api-key only, never as a bearer token.
	if q.xiKey != testKey || q.auth != "" {
		t.Fatalf("xi-api-key %q, Authorization %q", q.xiKey, q.auth)
	}
	want := map[string]string{"model_id": "scribe_v2", "file_format": "pcm_s16le_16", "language_code": "en"}
	if len(q.fields) != len(want) {
		t.Errorf("fields %v, want %v", q.fields, want)
	}
	for k, v := range want {
		if q.fields[k] != v {
			t.Errorf("%s = %q, want %q", k, q.fields[k], v)
		}
	}
	if !slices.Equal(q.names, []string{"file", "model_id", "file_format", "language_code"}) {
		t.Errorf("parts %v", q.names)
	}
	// The file is the 16 kHz samples as raw s16le: the WAV's data without its header.
	wav := EncodeWAV(samples)
	if q.filename != "audio.pcm" || len(q.file) != 2*len(samples) || !bytes.Equal(q.file, wav[len(wav)-2*len(samples):]) {
		t.Errorf("file %q: %d bytes, want %d", q.filename, len(q.file), 2*len(samples))
	}
	for _, s := range []string{"temperature", "beam_size", "Jarvis", "response_format"} {
		if bytes.Contains(q.raw, []byte(s)) {
			t.Errorf("request carries %s", s)
		}
	}
	// Words become a segment per sentence, in ms; the audio event is not text.
	wantSegs := []Segment{{T0Ms: 120, T1Ms: 1010, Text: "Turn off the lights."}, {T0Ms: 1310, T1Ms: 1480, Text: "Thanks!"}}
	if res.Text != "Turn off the lights. Thanks!" || !slices.Equal(res.Segments, wantSegs) {
		t.Fatalf("result %+v", res)
	}
}

// A base that ends at /v1 (pasted from ElevenLabs' docs) is the same API; auto language sends
// no language_code; no words is one segment for the clip, empty text none.
func TestRemoteElevenLabsVariants(t *testing.T) {
	f := newFakeOpenAI(t)
	f.response = `{"text":" hello there "}`
	e := setup(t, func(m *Module) { m.Engine = f.elevenLabs("/v1/", "", 0) })
	e.set(t, "whisper.language", "auto", "")
	samples := sine(16000, 1, 330)
	res, err := e.m.Transcribe(e.ctx, wavOf(samples, 16000), TranscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last(t)
	if q.path != "/v1/speech-to-text" || q.xiKey != "" || q.auth != "" || slices.Contains(q.names, "language_code") {
		t.Errorf("path %q xi %q auth %q parts %v", q.path, q.xiKey, q.auth, q.names)
	}
	if res.Text != "hello there" || !slices.Equal(res.Segments, []Segment{{T0Ms: 0, T1Ms: 1000, Text: "hello there"}}) {
		t.Errorf("no words %+v", res)
	}

	f.response = `{"text":"","words":[]}`
	if res, err = e.m.Transcribe(e.ctx, wavOf(samples, 16000), TranscribeOptions{}); err != nil || res.Text != "" || len(res.Segments) != 0 {
		t.Errorf("silence %+v %v", res, err)
	}
}

// The openai dialect's failure rules hold: 401/403 say only that the key was refused, nothing
// carries the key, a timeout says so.
func TestRemoteElevenLabsErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		delay  time.Duration
		want   string
	}{
		{"401", 401, `{"detail":{"status":"invalid_api_key","message":"Invalid API key: ` + testKey + `"}}`, 0, "refused the API key"},
		{"422", 422, `{"detail":[{"msg":"bad file_format"}]}`, 0, "422: "},
		{"500 echoing the key", 500, `upstream failed for key ` + testKey, 0, "500"},
		{"not json", 200, `<html>proxy</html>`, 0, "unreadable"},
		{"timeout", 200, "", 2 * time.Second, "no answer within 50ms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeOpenAI(t)
			f.status, f.response, f.delay = c.status, c.body, c.delay
			e := setup(t, func(m *Module) { m.Engine = f.elevenLabs("", testKey, 50*time.Millisecond) })
			_, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
			var ee *EngineError
			if !errors.As(err, &ee) {
				t.Fatalf("%T %v", err, err)
			}
			if strings.Contains(ee.Msg, testKey) || strings.Contains(ee.Stderr, testKey) {
				t.Fatalf("key in error: %q", ee.Msg)
			}
			if !strings.Contains(ee.Msg, c.want) || !strings.HasPrefix(ee.Msg, "Remote transcription failed: ") {
				t.Fatalf("message %q, want %q", ee.Msg, c.want)
			}
		})
	}
}

// An unknown dialect is an error, not a request in the wrong shape.
func TestRemoteUnknownDialect(t *testing.T) {
	f := newFakeOpenAI(t)
	e := setup(t, func(m *Module) {
		m.Engine = ResolveFunc(func(context.Context, string) (Endpoint, error) {
			return Endpoint{BaseURL: f.srv.URL, Remote: true, API: "deepgram"}, nil
		})
	})
	_, err := e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
	if err == nil || !strings.Contains(err.Error(), "deepgram") {
		t.Fatalf("%v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 0 {
		t.Fatal("an unknown dialect reached the provider")
	}
}
