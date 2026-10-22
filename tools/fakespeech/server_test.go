package main

import (
	"bytes"
	"encoding/json"
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
