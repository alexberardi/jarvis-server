// Package httpx holds the HTTP helpers every module shares. Response shapes follow the
// FastAPI services they replace, so clients see the same bodies: errors are {"detail": ...}.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// MaxBody is the default request-body cap for DecodeJSON.
const MaxBody = 1 << 20

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("httpx: encode response", "err", err)
	}
}

// Error writes a FastAPI-shaped error: {"detail": detail}. detail is usually a string, but
// FastAPI validation errors use a list, so any JSON value is allowed.
func Error(w http.ResponseWriter, status int, detail any) {
	WriteJSON(w, status, map[string]any{"detail": detail})
}

// DecodeJSON reads a JSON body of at most MaxBody bytes into v. Unknown fields are ignored,
// matching how the frozen clients are tolerated (e.g. D47). On failure it writes a 422 or 413
// and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body := http.MaxBytesReader(w, r.Body, MaxBody)
	if err := json.NewDecoder(body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			Error(w, http.StatusRequestEntityTooLarge, "Request body too large")
		case errors.Is(err, io.EOF):
			Error(w, http.StatusUnprocessableEntity, "Request body is empty")
		default:
			Error(w, http.StatusUnprocessableEntity, fmt.Sprintf("Invalid JSON body: %v", err))
		}
		return false
	}
	return true
}

// statusRecorder captures the status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps SSE and chunked streaming working through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// ServerName is the Server header every jarvisd listener sends.
const ServerName = "jarvisd"

// Middleware wraps a handler with panic recovery and access logging for one listener.
func Middleware(log *slog.Logger, listener string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// `jarvisd doctor` tells jarvisd's listeners from another program on the same port
		// (the legacy stack) by this header.
		w.Header().Set("Server", ServerName)
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.Error("panic in handler", "listener", listener, "method", r.Method, "path", r.URL.Path, "panic", p)
				if rec.status == 0 {
					Error(rec, http.StatusInternalServerError, "Internal Server Error")
				}
			}
			log.Debug("request", "listener", listener, "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "dur", time.Since(start))
		}()
		next.ServeHTTP(rec, r)
	})
}

// FieldError is one entry of a FastAPI/pydantic validation error.
type FieldError struct {
	Type  string `json:"type"`
	Loc   []any  `json:"loc"`
	Msg   string `json:"msg"`
	Input any    `json:"input"`
}

// ValidationError writes FastAPI's 422 body: {"detail": [{"type", "loc", "msg", "input"}, ...]}.
func ValidationError(w http.ResponseWriter, errs ...FieldError) {
	WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": errs})
}
