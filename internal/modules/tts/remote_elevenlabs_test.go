package tts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

const elevenKey = "xi-test-0123456789abcdef"

// elevenSetup is a module with tts.provider=remote speaking the elevenlabs dialect at f, as the
// ElevenLabs preset sets it (a base with no version).
func elevenSetup(t *testing.T, f *fakeProvider, extra map[string]any) *env {
	vals := map[string]any{
		"tts.remote_url": f.srv.URL, "tts.remote_api": "elevenlabs", "tts.remote_model": "eleven_flash_v2_5",
		"tts.remote_voice": "JBFqnCBsd6RMkjVDRZzb", "tts.remote_api_key": elevenKey,
	}
	for k, v := range extra {
		vals[k] = v
	}
	return remoteSetup(t, f, vals)
}

// elevenReply plays pcmFor(text) for a speech request, split mid-sample.
func elevenReply(_ int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
	pcm := pcmFor(body["text"].(string))
	_, _ = w.Write(pcm[:31])
	w.(http.Flusher).Flush()
	_, _ = w.Write(pcm[31:])
}

func TestRemoteElevenLabsRequest(t *testing.T) {
	f := newFakeProvider(t)
	f.reply = elevenReply
	e := elevenSetup(t, f, nil)
	st, err := e.m.Speak(context.Background(), "Hello there. How are you?", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Format != remoteFormat() {
		t.Fatalf("format %+v", st.Format)
	}
	chunks, err := readAll(t, st)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bytes.Join(chunks, nil), append(pcmFor("Hello there."), pcmFor("How are you?")...); !bytes.Equal(got, want) {
		t.Fatalf("audio %d bytes, want %d", len(got), len(want))
	}
	for _, c := range chunks {
		if len(c)%2 != 0 {
			t.Fatalf("chunk of %d bytes splits a sample", len(c))
		}
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for i, text := range []string{"Hello there.", "How are you?"} {
		q := reqs[i]
		if q.method != "POST" || q.path != "/v1/text-to-speech/JBFqnCBsd6RMkjVDRZzb/stream" ||
			q.query != "output_format=pcm_24000&enable_logging=false" || q.ctype != "application/json" {
			t.Fatalf("request %s %s?%s %s", q.method, q.path, q.query, q.ctype)
		}
		// The key goes in xi-api-key only.
		if q.xiKey != elevenKey || q.auth != "" {
			t.Fatalf("xi-api-key %q, Authorization %q", q.xiKey, q.auth)
		}
		if want := fmt.Sprintf(`{"model_id":"eleven_flash_v2_5","text":%q}`, text); q.raw != want {
			t.Fatalf("body %s, want %s", q.raw, want)
		}
	}
}

// A base pasted with its /v1 is the same API; a voice id is escaped into the path; no key sends
// no key header.
func TestRemoteElevenLabsURLAndNoKey(t *testing.T) {
	f := newFakeProvider(t)
	f.reply = elevenReply
	e := elevenSetup(t, f, map[string]any{"tts.remote_url": f.srv.URL + "/v1/", "tts.remote_voice": "a b", "tts.remote_api_key": ""})
	st, err := e.m.Speak(context.Background(), "Hi.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, st); err != nil {
		t.Fatal(err)
	}
	q := f.requests()[0]
	if q.path != "/v1/text-to-speech/a b/stream" || q.xiKey != "" || q.auth != "" {
		t.Fatalf("path %q xi %q auth %q", q.path, q.xiKey, q.auth)
	}
}

// ElevenLabs needs a voice in the path: none set is not installed, not a request.
func TestRemoteElevenLabsWithoutVoice(t *testing.T) {
	f := newFakeProvider(t)
	e := elevenSetup(t, f, map[string]any{"tts.remote_voice": ""})
	if _, err := e.m.Speak(context.Background(), "Hi.", SpeakOptions{}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err %v", err)
	}
	if len(f.requests()) != 0 {
		t.Fatal("a request without a voice")
	}
}

// A 422 skips that sentence; a 401 ends the reply (and its error never carries the key, even
// when the provider echoes it).
func TestRemoteElevenLabsErrors(t *testing.T) {
	refused := `{"detail":{"status":"invalid_api_key","message":"key ` + elevenKey + ` is invalid"}}`
	f := newFakeProvider(t)
	f.reply = func(i int, w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch i {
		case 0:
			http.Error(w, `{"detail":{"status":"invalid_text"}}`, http.StatusUnprocessableEntity)
		case 1:
			elevenReply(i, w, r, body)
		default:
			http.Error(w, refused, http.StatusUnauthorized)
		}
	}
	e := elevenSetup(t, f, nil)
	st, err := e.m.Speak(context.Background(), "One. Two. Three. Four.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	chunks, _ := readAll(t, st)
	if !bytes.Equal(bytes.Join(chunks, nil), pcmFor("Two.")) {
		t.Fatalf("audio %d bytes", len(bytes.Join(chunks, nil)))
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("%d requests, want 3 (the 401 ends it)", n)
	}

	// Refused before any audio: the reply is the error.
	f2 := newFakeProvider(t)
	f2.reply = func(_ int, w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		http.Error(w, refused, http.StatusUnauthorized)
	}
	e = elevenSetup(t, f2, nil)
	if st, err = e.m.Speak(context.Background(), "One. Two.", SpeakOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = readAll(t, st); err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), elevenKey) {
		t.Fatalf("err %v", err)
	}
	if n := len(f2.requests()); n != 1 {
		t.Fatalf("%d requests after a 401", n)
	}
}

// ElevenLabs' voices are GET /v2/voices, paged: every page is read, the ids are the voices
// and their names come along for display.
func TestVoiceChoicesElevenLabs(t *testing.T) {
	f := newFakeProvider(t)
	var queries []string
	f.reply = func(_ int, w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != "GET" || r.URL.Path != "/v2/voices" || r.Header.Get("xi-api-key") != elevenKey || r.Header.Get("Authorization") != "" {
			t.Errorf("%s %s xi=%q auth=%q", r.Method, r.URL.Path, r.Header.Get("xi-api-key"), r.Header.Get("Authorization"))
		}
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("next_page_token") == "" {
			_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v1","name":"George"},{"voice_id":"v2","name":"Aria"}],"has_more":true,"next_page_token":"p2"}`)
			return
		}
		_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v3","name":""}],"has_more":false,"next_page_token":null}`)
	}
	e := elevenSetup(t, f, map[string]any{"tts.remote_voice": "v2"})
	v := e.m.VoiceChoices(context.Background())
	if v.Provider != "remote" || strings.Join(v.Voices, ",") != "v1,v2,v3" || v.Current != "v2" || v.FreeText ||
		v.Setting != "tts/tts.remote_voice" || v.Names["v1"] != "George" || v.Names["v2"] != "Aria" || v.Names["v3"] != "" {
		t.Fatalf("%+v", v)
	}
	if strings.Join(queries, " | ") != "page_size=100 | next_page_token=p2&page_size=100" {
		t.Fatalf("queries %v", queries)
	}

	// A refused key lists nothing: the voice is free text.
	f.reply = func(_ int, w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		http.Error(w, `{"detail":"invalid_api_key"}`, http.StatusUnauthorized)
	}
	if v := e.m.VoiceChoices(context.Background()); !v.FreeText || len(v.Voices) != 0 || v.Current != "v2" {
		t.Fatalf("refused %+v", v)
	}
}

// tts.remote_api offers both dialects.
func TestRemoteAPIOptions(t *testing.T) {
	for _, d := range Definitions {
		if d.Key == "tts.remote_api" {
			if fmt.Sprint(d.Options) != "[openai elevenlabs]" || d.Default != "openai" {
				t.Fatalf("%+v", d)
			}
			e := setup(t, &Module{})
			if err := e.m.settings.Set(context.Background(), "tts.remote_api", "elevenlabs", settings.Scope{}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("no tts.remote_api")
}
