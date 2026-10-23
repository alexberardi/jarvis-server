package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server is the fake speech provider (the openai dialect) and a control API for tests.
//
//	POST /v1/audio/speech   {model, input, voice, response_format, speed} → a 440 Hz tone, 60 ms
//	                        per character of input, streamed in 20 ms pieces: s16le mono at
//	                        24 kHz for "pcm", a WAV for "wav" (or always WAV, see /control/wav)
//	GET  /v1/audio/voices   {"voices": Voices}
//	GET  /control/requests  {"requests": [...]} oldest first
//	DELETE /control/requests forget them
//	POST /control/fail      {"status": 503, "count": 1}: the next count speech requests answer that
//	                        status ({"error": {"message"}}); status 0 stalls them instead
//	POST /control/wav       {"rate": 22050, "channels": 2}: answer WAV at that rate whatever is
//	                        asked (a LocalAI-like server); {"rate": 0} back to normal
//
// With Key set, a speech or voices request without "Authorization: Bearer <Key>" is a 401.
type Server struct {
	Key string
	Log io.Writer // every speech request, one JSON object per line; nil = none

	mu       sync.Mutex
	reqs     []Request
	failN    int
	failAs   int
	wavRate  int
	wavChans int
}

// Voices is what GET /v1/audio/voices lists.
var Voices = []string{"fake_alto", "fake_tenor"}

// Request is one POST /v1/audio/speech.
type Request struct {
	Time           time.Time `json:"time"`
	Auth           bool      `json:"auth"` // a bearer token was sent
	Model          string    `json:"model"`
	Input          string    `json:"input"`
	Voice          string    `json:"voice"`
	ResponseFormat string    `json:"response_format"`
	Speed          float64   `json:"speed"`
	Status         int       `json:"status"`
}

const rate = 24000

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/audio/speech":
		s.speech(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/audio/voices":
		if !s.authorized(r) {
			apiError(w, http.StatusUnauthorized, "Incorrect API key provided")
			return
		}
		writeJSON(w, map[string]any{"voices": Voices})
	case r.URL.Path == "/control/requests" && r.Method == http.MethodGet:
		s.mu.Lock()
		out := append([]Request{}, s.reqs...)
		s.mu.Unlock()
		writeJSON(w, map[string]any{"requests": out})
	case r.URL.Path == "/control/requests" && r.Method == http.MethodDelete:
		s.mu.Lock()
		s.reqs = nil
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/control/fail" && r.Method == http.MethodPost:
		var b struct{ Status, Count int }
		if json.NewDecoder(r.Body).Decode(&b) != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.failAs, s.failN = b.Status, max(b.Count, 1)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/control/wav" && r.Method == http.MethodPost:
		var b struct{ Rate, Channels int }
		if json.NewDecoder(r.Body).Decode(&b) != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.wavRate, s.wavChans = b.Rate, max(b.Channels, 1)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	return s.Key == "" || r.Header.Get("Authorization") == "Bearer "+s.Key
}

func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	req := Request{Time: time.Now().UTC(), Auth: strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		req.Status = http.StatusUnprocessableEntity
		s.record(req)
		apiError(w, req.Status, "invalid JSON body")
		return
	}
	s.mu.Lock()
	fail, failAs := s.failN > 0, s.failAs
	if fail {
		s.failN--
	}
	wavRate, wavChans := s.wavRate, s.wavChans
	s.mu.Unlock()
	switch {
	case !s.authorized(r):
		req.Status = http.StatusUnauthorized
	case strings.TrimSpace(req.Input) == "":
		req.Status = http.StatusBadRequest
	case fail && failAs == 0:
		req.Status = 0 // stalled: never answers
	case fail:
		req.Status = failAs
	default:
		req.Status = http.StatusOK
	}
	s.record(req)
	switch req.Status {
	case http.StatusOK:
	case 0:
		<-r.Context().Done()
		return
	default:
		apiError(w, req.Status, "fakespeech: told to answer "+http.StatusText(req.Status))
		return
	}
	outRate, chans, wav := rate, 1, req.ResponseFormat == "wav"
	if wavRate > 0 {
		outRate, chans, wav = wavRate, wavChans, true
	}
	frames := len([]rune(req.Input)) * outRate * 60 / 1000
	if wav {
		w.Header().Set("Content-Type", "audio/wav")
	} else {
		w.Header().Set("Content-Type", "audio/pcm")
	}
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if wav {
		_, _ = w.Write(wavHeader(outRate, chans))
	}
	piece := outRate / 50 // 20 ms
	buf := make([]byte, 0, 2*chans*piece)
	for i := 0; i < frames; i++ {
		v := uint16(int16(8000 * math.Sin(2*math.Pi*440*float64(i)/float64(outRate))))
		for c := 0; c < chans; c++ {
			buf = binary.LittleEndian.AppendUint16(buf, v)
		}
		if len(buf) == cap(buf) || i == frames-1 {
			if _, err := w.Write(buf); err != nil {
				return
			}
			_ = rc.Flush()
			buf = buf[:0]
		}
	}
}

func (s *Server) record(req Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	if s.Log != nil {
		b, _ := json.Marshal(req)
		_, _ = s.Log.Write(append(b, '\n'))
	}
}

// wavHeader is a streaming WAV header: 16-bit PCM, sizes unknown (0xFFFFFFFF), as servers that
// stream WAV write it.
func wavHeader(rate, chans int) []byte {
	b := []byte("RIFF\xff\xff\xff\xffWAVEfmt ")
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, uint16(chans))
	b = binary.LittleEndian.AppendUint32(b, uint32(rate))
	b = binary.LittleEndian.AppendUint32(b, uint32(rate*chans*2))
	b = binary.LittleEndian.AppendUint16(b, uint16(chans*2))
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data\xff\xff\xff\xff"...)
	return b
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
