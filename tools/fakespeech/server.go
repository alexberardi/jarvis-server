package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Server is a fake OpenAI-compatible speech provider: what jarvisd's remote stt sends
// (internal/modules/stt remote_openai.go), answered with canned text instead of a real
// transcription, and a control API for tests (docs/llm/07-remote-speech.md §5).
//
//	POST /v1/audio/transcriptions  multipart file (a WAV), model, response_format json|text,
//	                               language?, prompt? → {"text"}; Bearer <Key> when Key is set
//	GET  /control/requests         {"requests": [...]} oldest first
//	DELETE /control/requests       forget them
//	POST /control/text             {"text": "..."}: what every transcription answers from now on
//	POST /control/fail             {"status": 503, "count": 1, "delay_ms": 0}: the next count
//	                               transcriptions answer that status (or, with delay_ms, answer
//	                               that late: a timeout)
type Server struct {
	// Key, when set, is the only API key accepted (401 otherwise).
	Key string
	Log io.Writer // every recorded request, one JSON object per line; nil = none

	mu      sync.Mutex
	text    string
	reqs    []Request
	seq     int
	failN   int
	failAs  int
	delayMs int
}

// DefaultText is what a transcription answers until /control/text changes it.
const DefaultText = "turn off the lights"

// Request is one transcription the fake received. The key itself is never recorded.
type Request struct {
	Seq            int       `json:"seq"`
	Time           time.Time `json:"time"`
	Path           string    `json:"path"`
	Model          string    `json:"model"`
	Language       string    `json:"language,omitempty"`
	Prompt         string    `json:"prompt,omitempty"`
	ResponseFormat string    `json:"response_format"`
	// Fields lists every form field other than the file, sorted (a stray temperature shows).
	Fields []string `json:"fields"`
	// FileBytes and SampleRate describe the uploaded WAV (0 = not a WAV).
	FileBytes  int    `json:"file_bytes"`
	SampleRate int    `json:"sample_rate"`
	Authorized bool   `json:"authorized"`
	Status     int    `json:"status"`
	Answer     string `json:"answer,omitempty"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case r.URL.Path == "/v1/audio/transcriptions" && r.Method == http.MethodPost:
		s.transcribe(w, r)
	case r.URL.Path == "/control/requests" && r.Method == http.MethodGet:
		s.mu.Lock()
		out := append([]Request{}, s.reqs...)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"requests": out})
	case r.URL.Path == "/control/requests" && r.Method == http.MethodDelete:
		s.mu.Lock()
		s.reqs = nil
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"requests": []Request{}})
	case r.URL.Path == "/control/text" && r.Method == http.MethodPost:
		var in struct{ Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, `want {"text": "..."}`)
			return
		}
		s.mu.Lock()
		s.text = in.Text
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"text": in.Text})
	case r.URL.Path == "/control/fail" && r.Method == http.MethodPost:
		var in struct {
			Status, Count int
			DelayMs       int `json:"delay_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Count < 0 || in.DelayMs < 0 ||
			(in.DelayMs == 0 && in.Status < 400) {
			writeError(w, http.StatusBadRequest, `want {"status": 4xx|5xx, "count": N} or {"delay_ms": N, "count": N}`)
			return
		}
		s.mu.Lock()
		s.failAs, s.failN, s.delayMs = in.Status, in.Count, in.DelayMs
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"status": in.Status, "count": in.Count, "delay_ms": in.DelayMs})
	default:
		writeError(w, http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	}
}

func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	q := Request{Time: time.Now().UTC(), Path: r.URL.Path, Fields: []string{}}
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	q.Authorized = s.Key == "" || key == s.Key
	status, answer, detail := http.StatusOK, "", ""
	var delay time.Duration
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		status, detail = http.StatusBadRequest, "want multipart/form-data: "+err.Error()
	} else {
		for name, vs := range r.MultipartForm.Value {
			q.Fields = append(q.Fields, name)
			switch name {
			case "model":
				q.Model = vs[0]
			case "language":
				q.Language = vs[0]
			case "prompt":
				q.Prompt = vs[0]
			case "response_format":
				q.ResponseFormat = vs[0]
			}
		}
		slices.Sort(q.Fields)
		if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
			if f, err := fhs[0].Open(); err == nil {
				b, _ := io.ReadAll(f)
				f.Close()
				q.FileBytes, q.SampleRate = len(b), wavRate(b)
			}
		}
	}
	s.mu.Lock()
	switch {
	case status != http.StatusOK:
	case !q.Authorized:
		status, detail = http.StatusUnauthorized, "Incorrect API key provided"
	case q.FileBytes == 0:
		status, detail = http.StatusBadRequest, "file is required"
	case q.SampleRate == 0:
		status, detail = http.StatusBadRequest, "file is not a WAV"
	case q.Model == "":
		status, detail = http.StatusBadRequest, "model is required"
	case q.ResponseFormat != "" && q.ResponseFormat != "json" && q.ResponseFormat != "text":
		status, detail = http.StatusBadRequest, "response_format: json or text"
	case s.failN > 0:
		s.failN--
		if s.delayMs > 0 {
			delay = time.Duration(s.delayMs) * time.Millisecond
		} else {
			status, detail = s.failAs, "failing on purpose (/control/fail)"
		}
	}
	if status == http.StatusOK {
		answer = DefaultText
		if s.text != "" {
			answer = s.text
		}
	}
	s.seq++
	q.Seq, q.Status, q.Answer = s.seq, status, answer
	s.reqs = append(s.reqs, q)
	if s.Log != nil {
		b, _ := json.Marshal(q)
		_, _ = s.Log.Write(append(b, '\n'))
	}
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"error": map[string]string{"message": detail}})
		return
	}
	if q.ResponseFormat == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, answer)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": answer})
}

// wavRate is a RIFF/WAVE file's sample rate, 0 if it isn't one.
func wavRate(b []byte) int {
	if len(b) < 28 || !bytes.Equal(b[0:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WAVE")) {
		return 0
	}
	// The fmt chunk is first in the WAVs jarvisd writes; look for it anyway.
	for i := 12; i+8 <= len(b); {
		id, n := string(b[i:i+4]), int(b[i+4])|int(b[i+5])<<8|int(b[i+6])<<16|int(b[i+7])<<24
		if id == "fmt " && i+16 <= len(b) {
			return int(b[i+12]) | int(b[i+13])<<8 | int(b[i+14])<<16 | int(b[i+15])<<24
		}
		i += 8 + n + n%2
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}
