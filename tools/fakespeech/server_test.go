package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
