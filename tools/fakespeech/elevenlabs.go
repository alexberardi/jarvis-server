package main

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The ElevenLabs routes: the shapes jarvisd's elevenlabs dialect sends and reads
// (docs/llm/07-remote-speech.md §2-3), never the real API.

func (s *Server) elevenTranscribe(w http.ResponseWriter, r *http.Request) {
	q := Request{Time: time.Now().UTC(), Path: r.URL.Path, Query: r.URL.RawQuery, Fields: []string{}, Authorized: s.authorizedXI(r)}
	status, detail := http.StatusOK, ""
	parseErr := r.ParseMultipartForm(32 << 20)
	if parseErr == nil {
		for name, vs := range r.MultipartForm.Value {
			q.Fields = append(q.Fields, name)
			switch name {
			case "model_id":
				q.Model = vs[0]
			case "language_code":
				q.Language = vs[0]
			case "file_format":
				q.ResponseFormat = vs[0]
			}
		}
		slices.Sort(q.Fields)
		if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
			if f, err := fhs[0].Open(); err == nil {
				b, _ := io.ReadAll(f)
				f.Close()
				q.FileBytes = len(b)
			}
		}
		if q.ResponseFormat == "pcm_s16le_16" {
			q.SampleRate = 16000
		}
	}
	var delay time.Duration
	var stall bool
	s.mu.Lock()
	switch {
	case !q.Authorized:
		status, detail = http.StatusUnauthorized, "invalid_api_key"
	case parseErr != nil:
		status, detail = http.StatusUnprocessableEntity, "want multipart/form-data: "+parseErr.Error()
	case q.FileBytes == 0:
		status, detail = http.StatusUnprocessableEntity, "file is required"
	case q.ResponseFormat != "pcm_s16le_16":
		// jarvisd sends raw PCM, which ElevenLabs reads only when file_format names it.
		status, detail = http.StatusUnprocessableEntity, "file_format: want pcm_s16le_16"
	case q.FileBytes%2 != 0:
		status, detail = http.StatusUnprocessableEntity, "pcm_s16le_16: an odd byte count"
	case q.Model == "":
		status, detail = http.StatusUnprocessableEntity, "model_id is required"
	default:
		var failAs int
		if failAs, delay, stall = s.failure(); failAs != 0 {
			status, detail = failAs, "failing on purpose (/control/fail)"
		}
	}
	answer := ""
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
		elevenError(w, status, detail)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"language_code": "eng", "language_probability": 1, "text": answer, "words": words(answer)})
}

// words is Scribe's words for text: each word 0.3 s long, a 0.05 s spacing between.
func words(text string) []map[string]any {
	out := []map[string]any{}
	t := 0.0
	for i, w := range strings.Fields(text) {
		if i > 0 {
			out = append(out, map[string]any{"text": " ", "start": t, "end": t + 0.05, "type": "spacing"})
			t += 0.05
		}
		out = append(out, map[string]any{"text": w, "start": t, "end": t + 0.3, "type": "word"})
		t += 0.3
	}
	return out
}

func (s *Server) elevenSpeech(w http.ResponseWriter, r *http.Request) {
	voice := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/"), "/stream")
	format := r.URL.Query().Get("output_format")
	q := Request{Time: time.Now().UTC(), Path: r.URL.Path, Query: r.URL.RawQuery, Voice: voice, ResponseFormat: format,
		Authorized: s.authorizedXI(r)}
	var in struct {
		Text    string `json:"text"`
		ModelID string `json:"model_id"`
	}
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	q.Input, q.Model = in.Text, in.ModelID
	outRate := 0
	if r, ok := strings.CutPrefix(format, "pcm_"); ok {
		outRate, _ = strconv.Atoi(r)
	}
	known := slices.ContainsFunc(ElevenVoices, func(v ElevenVoice) bool { return v.ID == voice })
	var delay time.Duration
	var stall bool
	s.mu.Lock()
	switch {
	case !q.Authorized:
		q.Status = http.StatusUnauthorized
	case err != nil || outRate < 8000 || outRate > 48000:
		// The real API defaults to MP3; jarvisd must ask for PCM.
		q.Status = http.StatusUnprocessableEntity
	case !known:
		q.Status = http.StatusNotFound
	case strings.TrimSpace(q.Input) == "":
		q.Status = http.StatusBadRequest
	default:
		q.Status, delay, stall = s.failure()
		if q.Status == 0 && !stall {
			q.Status = http.StatusOK
		}
	}
	s.record(&q)
	s.mu.Unlock()

	if !wait(r, delay, stall) {
		return
	}
	switch q.Status {
	case http.StatusOK:
		tone(w, q.Input, outRate, 1, false)
	case http.StatusUnauthorized:
		elevenError(w, q.Status, "invalid_api_key")
	case http.StatusNotFound:
		elevenError(w, q.Status, "voice_not_found")
	default:
		elevenError(w, q.Status, "fakespeech: told to answer "+http.StatusText(q.Status))
	}
}

// elevenVoices pages ElevenVoices two at a time: next_page_token is the next index.
func (s *Server) elevenVoices(w http.ResponseWriter, r *http.Request) {
	if !s.authorizedXI(r) {
		elevenError(w, http.StatusUnauthorized, "invalid_api_key")
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("next_page_token"))
	from = min(max(from, 0), len(ElevenVoices))
	to := min(from+2, len(ElevenVoices))
	out := map[string]any{"voices": ElevenVoices[from:to], "has_more": to < len(ElevenVoices), "total_count": len(ElevenVoices),
		"next_page_token": nil}
	if to < len(ElevenVoices) {
		out["next_page_token"] = strconv.Itoa(to)
	}
	writeJSON(w, http.StatusOK, out)
}

func elevenError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"detail": map[string]string{"status": msg, "message": msg}})
}
