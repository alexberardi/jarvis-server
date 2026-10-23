package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Server is a fake speech provider, OpenAI-compatible and ElevenLabs: what jarvisd's remote
// stt (internal/modules/stt remote_openai.go, remote_elevenlabs.go) and remote tts
// (internal/modules/tts remote.go) send, answered with canned text and a tone instead of real
// speech, and a control API for tests (docs/llm/07-remote-speech.md §5).
//
//	POST /v1/audio/transcriptions  multipart file (a WAV), model, response_format json|text,
//	                               language?, prompt? → {"text"}
//	POST /v1/audio/speech          {model, input, voice, response_format, speed} → a 440 Hz tone,
//	                               60 ms per character of input, streamed in 20 ms pieces: s16le
//	                               mono at 24 kHz for "pcm", a WAV for "wav" (or always WAV, see
//	                               /control/wav)
//	GET  /v1/audio/voices          {"voices": Voices}
//	POST /v1/speech-to-text        ElevenLabs: multipart file (raw s16le), model_id,
//	                               file_format=pcm_s16le_16, language_code? → {"text", "words"}
//	                               (each word 0.3 s, then a 0.05 s spacing)
//	POST /v1/text-to-speech/{voice}/stream?output_format=pcm_N
//	                               ElevenLabs: {text, model_id} → the tone at N Hz; a voice not in
//	                               ElevenVoices is a 404
//	GET  /v2/voices                ElevenLabs: ElevenVoices, two per page whatever page_size
//	                               says (next_page_token, has_more)
//	GET  /control/requests         {"requests": [...]} oldest first, both kinds (see Path)
//	DELETE /control/requests       forget them
//	POST /control/text             {"text": "..."}: what every transcription answers from now on
//	POST /control/fail             {"status": 503, "count": 1, "delay_ms": 0}: the next count
//	                               transcriptions or speech requests answer that status (with
//	                               delay_ms, answer that late: a timeout; status 0 and no
//	                               delay_ms: never answer)
//	POST /control/wav              {"rate": 22050, "channels": 2}: speech answers WAV at that rate
//	                               whatever is asked (a LocalAI-like server); {"rate": 0} back
//
// With Key set, an audio request without "Authorization: Bearer <Key>" (OpenAI routes) or
// "xi-api-key: <Key>" (ElevenLabs routes) is a 401.
type Server struct {
	// Key, when set, is the only API key accepted (401 otherwise).
	Key string
	Log io.Writer // every recorded request, one JSON object per line; nil = none

	mu       sync.Mutex
	text     string
	reqs     []Request
	seq      int
	failN    int
	failAs   int
	delayMs  int
	wavRate  int
	wavChans int
}

// DefaultText is what a transcription answers until /control/text changes it.
const DefaultText = "turn off the lights"

// Voices is what GET /v1/audio/voices lists.
var Voices = []string{"fake_alto", "fake_tenor"}

// ElevenVoice is one voice of GET /v2/voices: ElevenLabs' requests name the id, people the name.
type ElevenVoice struct {
	ID   string `json:"voice_id"`
	Name string `json:"name"`
}

// ElevenVoices is what GET /v2/voices lists, over two pages.
var ElevenVoices = []ElevenVoice{{"fakeVoiceAlto01", "Fake Alto"}, {"fakeVoiceTenor2", "Fake Tenor"}, {"fakeVoiceBass03", "Fake Bass"}}

// Request is one transcription or speech request the fake received. The key itself is never
// recorded.
type Request struct {
	Seq      int       `json:"seq"`
	Time     time.Time `json:"time"`
	Path     string    `json:"path"`
	Query    string    `json:"query,omitempty"`
	Model    string    `json:"model"`
	Language string    `json:"language,omitempty"`
	Prompt   string    `json:"prompt,omitempty"`
	// ResponseFormat is also ElevenLabs' file_format (stt) or output_format (tts).
	ResponseFormat string `json:"response_format"`
	// Fields lists every form field of a transcription other than the file, sorted (a stray
	// temperature shows).
	Fields []string `json:"fields,omitempty"`
	// FileBytes and SampleRate describe a transcription's uploaded WAV (0 = not a WAV), or
	// ElevenLabs' raw PCM at the rate its file_format names.
	FileBytes  int `json:"file_bytes,omitempty"`
	SampleRate int `json:"sample_rate,omitempty"`
	// Input, Voice and Speed are a speech request's.
	Input      string  `json:"input,omitempty"`
	Voice      string  `json:"voice,omitempty"`
	Speed      float64 `json:"speed,omitempty"`
	Authorized bool    `json:"authorized"`
	Status     int     `json:"status"`
	Answer     string  `json:"answer,omitempty"`
}

const rate = 24000

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case r.URL.Path == "/v1/audio/transcriptions" && r.Method == http.MethodPost:
		s.transcribe(w, r)
	case r.URL.Path == "/v1/audio/speech" && r.Method == http.MethodPost:
		s.speech(w, r)
	case r.URL.Path == "/v1/audio/voices" && r.Method == http.MethodGet:
		if !s.authorized(r) {
			apiError(w, http.StatusUnauthorized, "Incorrect API key provided")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"voices": Voices})
	case r.URL.Path == "/v1/speech-to-text" && r.Method == http.MethodPost:
		s.elevenTranscribe(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/") && strings.HasSuffix(r.URL.Path, "/stream") && r.Method == http.MethodPost:
		s.elevenSpeech(w, r)
	case r.URL.Path == "/v2/voices" && r.Method == http.MethodGet:
		s.elevenVoices(w, r)
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
			(in.DelayMs == 0 && in.Status != 0 && in.Status < 400) {
			writeError(w, http.StatusBadRequest, `want {"status": 4xx|5xx|0, "count": N} or {"delay_ms": N, "count": N}`)
			return
		}
		s.mu.Lock()
		s.failAs, s.failN, s.delayMs = in.Status, in.Count, in.DelayMs
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"status": in.Status, "count": in.Count, "delay_ms": in.DelayMs})
	case r.URL.Path == "/control/wav" && r.Method == http.MethodPost:
		var in struct{ Rate, Channels int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, `want {"rate": N, "channels": N}`)
			return
		}
		s.mu.Lock()
		s.wavRate, s.wavChans = in.Rate, max(in.Channels, 1)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"rate": in.Rate, "channels": max(in.Channels, 1)})
	default:
		writeError(w, http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return s.Key == "" || key == s.Key
}

func (s *Server) authorizedXI(r *http.Request) bool {
	return s.Key == "" || r.Header.Get("xi-api-key") == s.Key
}

// failure takes one of the failures /control/fail asked for, under s.mu: a status to answer, or
// a delay before answering normally; stall = never answer.
func (s *Server) failure() (status int, delay time.Duration, stall bool) {
	if s.failN == 0 {
		return 0, 0, false
	}
	s.failN--
	switch {
	case s.delayMs > 0:
		return 0, time.Duration(s.delayMs) * time.Millisecond, false
	case s.failAs == 0:
		return 0, 0, true
	}
	return s.failAs, 0, false
}

// record adds q to the requests and the log, under s.mu.
func (s *Server) record(q *Request) {
	s.seq++
	q.Seq = s.seq
	s.reqs = append(s.reqs, *q)
	if s.Log != nil {
		b, _ := json.Marshal(q)
		_, _ = s.Log.Write(append(b, '\n'))
	}
}

// wait sleeps d (a /control/fail delay) or forever when stall; false if the caller went away.
func wait(r *http.Request, d time.Duration, stall bool) bool {
	if stall {
		<-r.Context().Done()
		return false
	}
	if d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return false
		}
	}
	return true
}

func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	q := Request{Time: time.Now().UTC(), Path: r.URL.Path, Fields: []string{}, Authorized: s.authorized(r)}
	status, answer, detail := http.StatusOK, "", ""
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
	var delay time.Duration
	var stall bool
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
	default:
		var failAs int
		if failAs, delay, stall = s.failure(); failAs != 0 {
			status, detail = failAs, "failing on purpose (/control/fail)"
		}
	}
	if status == http.StatusOK {
		answer = DefaultText
		if s.text != "" {
			answer = s.text
		}
	}
	q.Status, q.Answer = status, answer
	if stall {
		q.Status, q.Answer = 0, ""
	}
	s.record(&q)
	s.mu.Unlock()

	if !wait(r, delay, stall) {
		return
	}
	if status != http.StatusOK {
		apiError(w, status, detail)
		return
	}
	if q.ResponseFormat == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, answer)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": answer})
}

func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	q := Request{Time: time.Now().UTC(), Path: r.URL.Path, Authorized: s.authorized(r)}
	var in struct {
		Model          string  `json:"model"`
		Input          string  `json:"input"`
		Voice          string  `json:"voice"`
		ResponseFormat string  `json:"response_format"`
		Speed          float64 `json:"speed"`
	}
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	q.Model, q.Input, q.Voice, q.ResponseFormat, q.Speed = in.Model, in.Input, in.Voice, in.ResponseFormat, in.Speed
	var delay time.Duration
	var stall bool
	s.mu.Lock()
	switch {
	case err != nil:
		q.Status = http.StatusUnprocessableEntity
	case !q.Authorized:
		q.Status = http.StatusUnauthorized
	case strings.TrimSpace(q.Input) == "":
		q.Status = http.StatusBadRequest
	default:
		q.Status, delay, stall = s.failure()
		if q.Status == 0 && !stall {
			q.Status = http.StatusOK
		}
	}
	wavRate, wavChans := s.wavRate, s.wavChans
	s.record(&q)
	s.mu.Unlock()

	if !wait(r, delay, stall) {
		return
	}
	switch q.Status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		apiError(w, q.Status, "Incorrect API key provided")
		return
	default:
		apiError(w, q.Status, "fakespeech: told to answer "+http.StatusText(q.Status))
		return
	}
	outRate, chans, wav := rate, 1, q.ResponseFormat == "wav"
	if wavRate > 0 {
		outRate, chans, wav = wavRate, wavChans, true
	}
	tone(w, q.Input, outRate, chans, wav)
}

// tone streams a 440 Hz tone, 60 ms per character of input, in 20 ms pieces: s16le PCM at
// outRate with chans channels, behind a streaming WAV header when wav.
func tone(w http.ResponseWriter, input string, outRate, chans int, wav bool) {
	frames := len([]rune(input)) * outRate * 60 / 1000
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
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}
