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
	"time"

	"github.com/gorilla/websocket"
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

func TestMountPrefix(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := MountPrefix(r); got != "" {
		t.Fatalf("unset: %q", got)
	}
	if got := MountPrefix(WithMountPrefix(r, "/cc")); got != "/cc" {
		t.Fatalf("set: %q", got)
	}
}

func TestMountRewritesRelativeRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /media/", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /abs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://x/y", http.StatusFound)
	})
	mux.HandleFunc("GET /scheme", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "jarvis://cb")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("GET /proto", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "//other/z")
		w.WriteHeader(http.StatusFound)
	})
	h := Mount("/recipes", mux)
	for _, c := range []struct {
		path string
		code int
		loc  string
	}{
		// ServeMux's trailing-slash redirect (307 for a method pattern).
		{"/recipes/media", http.StatusTemporaryRedirect, "/recipes/media/"},
		{"/recipes/abs", http.StatusFound, "https://x/y"},
		{"/recipes/scheme", http.StatusFound, "jarvis://cb"},
		{"/recipes/proto", http.StatusFound, "//other/z"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.code || rec.Header().Get("Location") != c.loc {
			t.Errorf("%s: code=%d Location=%q, want %d %q", c.path, rec.Code, rec.Header().Get("Location"), c.code, c.loc)
		}
	}
}

func TestMiddlewareHijacks(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(Middleware(log, "test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "not a Hijacker", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\nhijack")
		buf.Flush()
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "hijack" {
		t.Fatalf("code=%d body=%q", resp.StatusCode, b)
	}

	// A gorilla upgrade through a mount, as cc's Twilio media stream is served on the config port.
	var up websocket.Upgrader
	ws := http.NewServeMux()
	ws.HandleFunc("GET /media", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	front := http.NewServeMux()
	front.Handle("/cc/", Mount("/cc", Middleware(log, "cc", ws)))
	srv2 := httptest.NewServer(front)
	defer srv2.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv2.URL, "http")+"/cc/media", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	pong := make(chan string, 1)
	c.SetPongHandler(func(d string) error { pong <- d; return nil })
	if err := c.WriteControl(websocket.PingMessage, []byte("hi"), time.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	go c.ReadMessage() // runs the pong handler
	select {
	case d := <-pong:
		if d != "hi" {
			t.Fatalf("pong %q", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no pong")
	}
}
