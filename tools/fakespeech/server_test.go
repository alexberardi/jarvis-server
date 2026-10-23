package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// transcription posts what jarvisd's remote stt sends: a 16 kHz WAV and the openai fields.
func transcription(t *testing.T, url, key string, fields map[string]string) (int, map[string]any) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "audio.wav")
	fw.Write(stt.EncodeWAV(make([]float32, 16000)))
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/audio/transcriptions", &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func control(t *testing.T, method, url, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s: %d", method, url, resp.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func requests(t *testing.T, url string) []Request {
	t.Helper()
	resp, err := http.Get(url + "/control/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Requests []Request }
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Requests
}

func TestTranscriptions(t *testing.T) {
	var log bytes.Buffer
	srv := httptest.NewServer(&Server{Key: "sk-fake", Log: &log})
	defer srv.Close()
	ok := map[string]string{"model": "fake-transcribe", "response_format": "json", "language": "en"}

	if c, out := transcription(t, srv.URL, "sk-fake", ok); c != 200 || out["text"] != DefaultText {
		t.Fatalf("%d %v", c, out)
	}
	control(t, http.MethodPost, srv.URL+"/control/text", `{"text": "what time is it"}`)
	if c, out := transcription(t, srv.URL, "sk-fake", ok); c != 200 || out["text"] != "what time is it" {
		t.Fatalf("%d %v", c, out)
	}
	if c, _ := transcription(t, srv.URL, "sk-wrong", ok); c != 401 {
		t.Fatalf("wrong key: %d", c)
	}
	if c, _ := transcription(t, srv.URL, "sk-fake", map[string]string{"response_format": "json"}); c != 400 {
		t.Fatalf("no model: %d", c)
	}
	if c, _ := transcription(t, srv.URL, "sk-fake", map[string]string{"model": "m", "response_format": "verbose_json"}); c != 400 {
		t.Fatalf("verbose_json: %d", c)
	}

	reqs := requests(t, srv.URL)
	if len(reqs) != 5 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.Seq != 1 || r.Model != "fake-transcribe" || r.Language != "en" || r.ResponseFormat != "json" || r.SampleRate != 16000 ||
		!r.Authorized || r.Status != 200 || r.Answer != DefaultText || !slices.Equal(r.Fields, []string{"language", "model", "response_format"}) {
		t.Errorf("recorded %+v", r)
	}
	if reqs[2].Authorized || reqs[2].Status != 401 {
		t.Errorf("wrong key recorded %+v", reqs[2])
	}
	if strings.Contains(log.String(), "sk-") || strings.Count(log.String(), "\n") != 5 {
		t.Errorf("log %s", log.String())
	}
	control(t, http.MethodDelete, srv.URL+"/control/requests", "")
	if n := len(requests(t, srv.URL)); n != 0 {
		t.Errorf("%d after delete", n)
	}
}

func TestFail(t *testing.T) {
	srv := httptest.NewServer(&Server{})
	defer srv.Close()
	ok := map[string]string{"model": "m"}
	control(t, http.MethodPost, srv.URL+"/control/fail", `{"status": 503, "count": 1}`)
	if c, _ := transcription(t, srv.URL, "", ok); c != 503 {
		t.Fatalf("fail: %d", c)
	}
	if c, _ := transcription(t, srv.URL, "", ok); c != 200 {
		t.Fatalf("after: %d", c)
	}
	control(t, http.MethodPost, srv.URL+"/control/fail", `{"delay_ms": 300, "count": 1}`)
	start := time.Now()
	if c, _ := transcription(t, srv.URL, "", ok); c != 200 || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("delay: %d after %s", c, time.Since(start))
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/control/fail", strings.NewReader(`{"status": 200, "count": 1}`))
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 400 {
		t.Fatalf("status 200 accepted: %d", resp.StatusCode)
	}
}

func post(t *testing.T, url, body, key string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestSpeech(t *testing.T) {
	s := &Server{Key: "k"}
	srv := httptest.NewServer(s)
	defer srv.Close()
	speak := `{"model":"m","input":"Hello.","voice":"fake_alto","response_format":"pcm","speed":1}`

	resp := post(t, srv.URL+"/v1/audio/speech", speak, "k")
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || len(b) != 2*6*24000*60/1000 {
		t.Fatalf("pcm: %d, %d bytes", resp.StatusCode, len(b))
	}
	if resp := post(t, srv.URL+"/v1/audio/speech", speak, "wrong"); resp.StatusCode != 401 {
		t.Fatalf("bad key: %d", resp.StatusCode)
	}
	post(t, srv.URL+"/control/fail", `{"status":503,"count":1}`, "")
	if resp := post(t, srv.URL+"/v1/audio/speech", speak, "k"); resp.StatusCode != 503 {
		t.Fatalf("fail: %d", resp.StatusCode)
	}
	if resp := post(t, srv.URL+"/v1/audio/speech", speak, "k"); resp.StatusCode != 200 {
		t.Fatalf("after fail: %d", resp.StatusCode)
	}
	post(t, srv.URL+"/control/wav", `{"rate":22050,"channels":2}`, "")
	resp = post(t, srv.URL+"/v1/audio/speech", speak, "k")
	b, _ = io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(b), "RIFF") || len(b) != 44+4*6*22050*60/1000 {
		t.Fatalf("wav: %d bytes %q", len(b), b[:4])
	}

	r, _ := http.Get(srv.URL + "/control/requests")
	var got struct{ Requests []Request }
	_ = json.NewDecoder(r.Body).Decode(&got)
	r.Body.Close()
	if len(got.Requests) != 5 || got.Requests[0].Input != "Hello." || got.Requests[0].Voice != "fake_alto" ||
		got.Requests[1].Status != 401 || got.Requests[2].Status != 503 {
		t.Fatalf("requests %+v", got.Requests)
	}
}

// elevenTranscription posts what jarvisd's elevenlabs stt sends: raw 16 kHz PCM and its fields.
func elevenTranscription(t *testing.T, url, key string, pcmBytes int, fields map[string]string) (int, map[string]any) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "audio.pcm")
	fw.Write(make([]byte, pcmBytes))
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/speech-to-text?enable_logging=false", &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if key != "" {
		req.Header.Set("xi-api-key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestElevenLabsSpeechToText(t *testing.T) {
	srv := httptest.NewServer(&Server{Key: "xi-fake"})
	defer srv.Close()
	ok := map[string]string{"model_id": "scribe_v2", "file_format": "pcm_s16le_16", "language_code": "en"}

	c, out := elevenTranscription(t, srv.URL, "xi-fake", 32000, ok)
	if c != 200 || out["text"] != DefaultText {
		t.Fatalf("%d %v", c, out)
	}
	// Words and spacings, in seconds, as Scribe answers.
	words, _ := out["words"].([]any)
	if len(words) != 7 {
		t.Fatalf("words %v", words)
	}
	first, last := words[0].(map[string]any), words[6].(map[string]any)
	if first["text"] != "turn" || first["type"] != "word" || first["start"] != 0.0 || last["text"] != "lights" ||
		words[1].(map[string]any)["type"] != "spacing" {
		t.Fatalf("words %v", words)
	}
	// A bearer token is not ElevenLabs' auth.
	if c, _ := transcriptionBearerAt(t, srv.URL+"/v1/speech-to-text", "xi-fake"); c != 401 {
		t.Fatalf("bearer: %d", c)
	}
	if c, _ := elevenTranscription(t, srv.URL, "xi-fake", 32000, map[string]string{"model_id": "scribe_v2"}); c != 422 {
		t.Fatalf("no file_format (a WAV would be decoded, PCM must be named): %d", c)
	}
	if c, _ := elevenTranscription(t, srv.URL, "xi-fake", 31, ok); c != 422 {
		t.Fatalf("odd PCM: %d", c)
	}
	post(t, srv.URL+"/control/fail", `{"status":503,"count":1}`, "")
	if c, _ := elevenTranscription(t, srv.URL, "xi-fake", 32000, ok); c != 503 {
		t.Fatalf("fail: %d", c)
	}

	reqs := requests(t, srv.URL)
	r := reqs[0]
	if r.Path != "/v1/speech-to-text" || r.Query != "enable_logging=false" || r.Model != "scribe_v2" || r.Language != "en" ||
		r.ResponseFormat != "pcm_s16le_16" || r.FileBytes != 32000 || r.SampleRate != 16000 || !r.Authorized ||
		!slices.Equal(r.Fields, []string{"file_format", "language_code", "model_id"}) {
		t.Errorf("recorded %+v", r)
	}
}

func transcriptionBearerAt(t *testing.T, url, key string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestElevenLabsTextToSpeech(t *testing.T) {
	srv := httptest.NewServer(&Server{Key: "xi-fake"})
	defer srv.Close()
	speak := func(path, body, key string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("xi-api-key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	const path = "/v1/text-to-speech/fakeVoiceAlto01/stream?output_format=pcm_24000&enable_logging=false"
	body := `{"text":"Hello.","model_id":"eleven_flash_v2_5"}`
	resp := speak(path, body, "xi-fake")
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || len(b) != 2*6*24000*60/1000 {
		t.Fatalf("pcm: %d, %d bytes", resp.StatusCode, len(b))
	}
	if resp := speak(path, body, "wrong"); resp.StatusCode != 401 {
		t.Fatalf("bad key: %d", resp.StatusCode)
	}
	if resp := speak("/v1/text-to-speech/fakeVoiceAlto01/stream?output_format=mp3_44100_128", body, "xi-fake"); resp.StatusCode != 422 {
		t.Fatalf("mp3: %d", resp.StatusCode)
	}
	if resp := speak("/v1/text-to-speech/nobody/stream?output_format=pcm_24000", body, "xi-fake"); resp.StatusCode != 404 {
		t.Fatalf("unknown voice: %d", resp.StatusCode)
	}
	post(t, srv.URL+"/control/fail", `{"status":429,"count":1}`, "")
	if resp := speak(path, body, "xi-fake"); resp.StatusCode != 429 {
		t.Fatalf("fail: %d", resp.StatusCode)
	}
	reqs := requests(t, srv.URL)
	r := reqs[0]
	if r.Path != "/v1/text-to-speech/fakeVoiceAlto01/stream" || r.Query != "output_format=pcm_24000&enable_logging=false" ||
		r.Voice != "fakeVoiceAlto01" || r.Input != "Hello." || r.Model != "eleven_flash_v2_5" || r.ResponseFormat != "pcm_24000" || !r.Authorized {
		t.Errorf("recorded %+v", r)
	}
}

func TestElevenLabsVoices(t *testing.T) {
	srv := httptest.NewServer(&Server{Key: "xi-fake"})
	defer srv.Close()
	page := func(q string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+"/v2/voices?"+q, nil)
		req.Header.Set("xi-api-key", "xi-fake")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	// The fake pages by 2 at most whatever is asked, so a client must follow next_page_token.
	c, out := page("page_size=100")
	if c != 200 || out["has_more"] != true || out["next_page_token"] != "2" || len(out["voices"].([]any)) != 2 {
		t.Fatalf("page 1: %d %v", c, out)
	}
	if v := out["voices"].([]any)[0].(map[string]any); v["voice_id"] != ElevenVoices[0].ID || v["name"] != ElevenVoices[0].Name {
		t.Fatalf("voice %v", v)
	}
	c, out = page("page_size=100&next_page_token=2")
	if c != 200 || out["has_more"] != false || out["next_page_token"] != nil || len(out["voices"].([]any)) != len(ElevenVoices)-2 {
		t.Fatalf("page 2: %d %v", c, out)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v2/voices", nil)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Fatalf("no key: %d", resp.StatusCode)
	}
}
