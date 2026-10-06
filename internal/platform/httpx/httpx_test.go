package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func body(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q", rec.Body.String())
	}
	return m
}

func TestErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusNotFound, "Not found")
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code=%d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if body(t, rec)["detail"] != "Not found" {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestDecodeJSON(t *testing.T) {
	type req struct {
		Name string `json:"name"`
	}
	cases := []struct {
		in     string
		ok     bool
		status int
	}{
		{`{"name":"x","unknown":1}`, true, 200}, // unknown fields are ignored
		{``, false, 422},
		{`{bad`, false, 422},
		{`{"name":"` + strings.Repeat("a", MaxBody) + `"}`, false, 413},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", strings.NewReader(c.in))
		var v req
		ok := DecodeJSON(rec, r, &v)
		if ok != c.ok || (!ok && rec.Code != c.status) {
			t.Errorf("%.20q: ok=%v code=%d", c.in, ok, rec.Code)
		}
		if ok && v.Name != "x" {
			t.Errorf("decoded %+v", v)
		}
	}
}

func TestMiddlewareRecoversPanic(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := Middleware(log, "test", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 500 || body(t, rec)["detail"] != "Internal Server Error" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Fatalf("panic not logged: %q", logs.String())
	}
}

func TestMiddlewareKeepsFlusher(t *testing.T) {
	h := Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Error("wrapped writer lost http.Flusher")
			}
			w.Write([]byte("data: x\n\n"))
		}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}
