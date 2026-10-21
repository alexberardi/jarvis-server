package models

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
)

// newTestServer is httptest.NewServer, or, where the test can't listen on a port (an OS
// sandbox), a server whose URL only the returned client reaches: requests go straight to the
// handler. nil client = the real server (use http.DefaultClient).
func newTestServer(h http.Handler) (*httptest.Server, *http.Client) {
	if l, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		l.Close()
		return httptest.NewServer(h), nil
	}
	return &httptest.Server{URL: "http://hub.invalid"}, &http.Client{Transport: inproc{h}}
}

// inproc is a RoundTripper that serves requests with a handler in this process, streaming
// the body through a pipe. A handler panic (http.ErrAbortHandler: a cut connection) or the
// request's cancellation ends the body with an error.
type inproc struct{ h http.Handler }

func (t inproc) RoundTrip(r *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	rw := &pipeWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer func() {
			p := recover()
			rw.WriteHeader(http.StatusOK)
			if p != nil {
				pw.CloseWithError(io.ErrUnexpectedEOF)
				return
			}
			// A cancelled request ends its body with the context's error, as a real
			// connection does, not with a clean (short) EOF.
			if err := r.Context().Err(); err != nil {
				pw.CloseWithError(err)
				return
			}
			pw.Close()
		}()
		t.h.ServeHTTP(rw, r)
	}()
	select {
	case <-rw.ready:
	case <-r.Context().Done():
		pr.Close()
		return nil, r.Context().Err()
	}
	return &http.Response{StatusCode: rw.code, Status: http.StatusText(rw.code), Header: rw.header, Body: pr,
		ContentLength: -1, Request: r, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}, nil
}

type pipeWriter struct {
	header http.Header
	pw     *io.PipeWriter
	once   sync.Once
	code   int
	ready  chan struct{}
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.code = code
		close(w.ready)
	})
}

func (w *pipeWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(b)
}

func (w *pipeWriter) Flush() {}
