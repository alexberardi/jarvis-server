package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stallServer writes head (when set), flushes, then sends nothing until the request ends.
// With headers false it stalls before sending any response headers.
func stallServer(t *testing.T, ctype, head string, headers bool) *httptest.Server {
	t.Helper()
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // the server notices the client leaving only after this
		if headers {
			w.Header().Set("Content-Type", ctype)
			w.WriteHeader(200)
			if head != "" {
				w.Write([]byte(head))
			}
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) }) // runs first
	return srv
}

// idleService resolves the background label to srv, with one background slot.
func idleService(srv *httptest.Server, api string) *Service {
	ep := Endpoint{BaseURL: srv.URL, Model: "m", API: api}
	s := NewService(ServiceConfig{Resolver: &fakeResolver{eps: map[string]Endpoint{LabelBackground: ep}, err: map[string]error{}}, Log: quietLog(), BackgroundParallel: 1})
	s.setAdmission(0, 0)
	return s
}

// A stalled engine ends the stream with an idle error (no done frame) and frees the slot,
// even though the caller's ctx never ends.
func TestStreamIdleTimeout(t *testing.T) {
	cases := []struct {
		name, api, ctype, head string
		headers                bool
		delta                  string
	}{
		{name: "openai mid-stream", ctype: "text/event-stream", headers: true,
			head: "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n", delta: "Hel"},
		{name: "before headers"},
		{name: "headers, no body", ctype: "text/event-stream", headers: true},
		{name: "anthropic mid-stream", api: APIAnthropic, ctype: "text/event-stream", headers: true, delta: "Hel",
			head: sse(`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := idleService(stallServer(t, c.ctype, c.head, c.headers), c.api)
			s.setStreamIdle(80*time.Millisecond, 80*time.Millisecond)
			start := time.Now()
			ch, err := s.Stream(context.Background(), ChatRequest{Label: LabelBackground, Messages: []Message{msg("user", "hi")}})
			if err != nil {
				t.Fatal(err)
			}
			frames := collect(t, ch)
			last := frames[len(frames)-1]
			if last.Done || last.Cancelled || !strings.Contains(last.Err, "engine sent nothing for") {
				t.Fatalf("last frame %+v", last)
			}
			if c.delta != "" && (len(frames) != 2 || frames[0].Delta != c.delta) {
				t.Fatalf("frames %+v", frames)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("took %s", d)
			}
			if n := len(s.bg); n != 0 {
				t.Fatalf("background slot still held (%d)", n)
			}
		})
	}
}

// Until the first byte the engine gets the longer first-byte allowance (prompt eval sends
// nothing), then the idle window applies between reads.
func TestStreamIdleFirstByteAllowance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond) // a long prompt eval, past the idle window
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range []string{"a", "b", "c"} {
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + p + "\"}}]}\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	s := idleService(srv, "")
	s.setStreamIdle(60*time.Millisecond, 2*time.Second)
	ch, err := s.Stream(context.Background(), ChatRequest{Label: LabelBackground, Messages: []Message{msg("user", "hi")}})
	if err != nil {
		t.Fatal(err)
	}
	frames := collect(t, ch)
	if last := frames[len(frames)-1]; !last.Done || last.Content != "abc" {
		t.Fatalf("frames %+v", frames)
	}
}

// A slow reader of the frames is not an idle engine: the watchdog pauses while a delta waits
// for the consumer.
func TestStreamIdleSlowConsumer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// The engine never goes quiet for 50 ms: it flushes a frame every 5 ms (about 0.5 s in all),
		// so only the consumer's pause could trip the watchdog.
		for i := 0; i < 100; i++ {
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	s := idleService(srv, "")
	s.setStreamIdle(50*time.Millisecond, 50*time.Millisecond)
	ch, err := s.Stream(context.Background(), ChatRequest{Label: LabelBackground, Messages: []Message{msg("user", "hi")}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // longer than the whole stream: the 64-frame buffer fills and the parser blocks on us
	frames := collect(t, ch)
	if last := frames[len(frames)-1]; !last.Done || len(last.Content) != 100 {
		t.Fatalf("last frame %+v", last)
	}
}

func TestStreamIdleSetting(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	if idle, first := s.streamIdle(context.Background()); idle != 60*time.Second || first != 240*time.Second {
		t.Fatalf("defaults: idle %s first %s", idle, first)
	}
}
