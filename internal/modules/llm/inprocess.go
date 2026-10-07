package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
)

// In-process HTTP: other jarvisd modules that already speak the OpenAI-compatible API (OCR's
// LLM vision and validation) get an *http.Client whose requests are served by this module's
// handlers in memory: no socket, no app credentials. The request context carries a marker the
// app-auth guard trusts; it can't be set from the network.

type inProcessKey struct{}

func inProcess(r *http.Request) bool {
	v, _ := r.Context().Value(inProcessKey{}).(bool)
	return v
}

type inProcessTransport struct{ m *Module }

func (t inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.m.mux == nil {
		return nil, http.ErrServerClosed
	}
	r := req.Clone(context.WithValue(req.Context(), inProcessKey{}, true))
	r.RequestURI = r.URL.RequestURI()
	rec := httptest.NewRecorder()
	t.m.mux.ServeHTTP(rec, r)
	res := rec.Result()
	res.Request = req
	return res, nil
}

// InProcessBaseURL is the base URL to pair with InProcessClient (any host works; the
// transport ignores it).
const InProcessBaseURL = "http://jarvisd-llm"

// InProcessClient returns a client served by this module's handlers in memory. Streaming
// responses are buffered whole, so use it for non-stream calls (in-process code that needs
// streaming calls Service().Stream directly).
func (m *Module) InProcessClient() *http.Client {
	return &http.Client{Transport: inProcessTransport{m}}
}
