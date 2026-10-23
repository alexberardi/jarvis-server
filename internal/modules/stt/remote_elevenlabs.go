package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// inferElevenLabs transcribes the clip on ElevenLabs' Scribe: multipart POST
// {base}/v1/speech-to-text?enable_logging=false with the 16 kHz samples as raw PCM
// (file_format=pcm_s16le_16, no WAV header to decode), model_id and language_code (omitted for
// auto). The prompt, temperature and beam size have no equivalent and are dropped. The answer's
// words carry timings in seconds; they become one segment per sentence
// (docs/decisions/2026-10-22-remote-speech-elevenlabs.md).
//
// The key goes only in the xi-api-key header, never as a bearer token: neither it nor the
// headers are logged, and an error never contains it.
func (m *Module) inferElevenLabs(ctx context.Context, ep Endpoint, samples []float32, p inferenceParams) (string, []Segment, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "audio.pcm")
	pcm := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(audio.FloatToPCM16(s)))
	}
	fw.Write(pcm)
	fields := [][2]string{{"model_id", ep.Model}, {"file_format", "pcm_s16le_16"}}
	if lang := strings.TrimSpace(p.Language); lang != "" && !strings.EqualFold(lang, "auto") {
		fields = append(fields, [2]string{"language_code", lang})
	}
	for _, f := range fields {
		mw.WriteField(f[0], f[1])
	}
	mw.Close()

	raw, err := m.postRemote(ctx, ep, ElevenLabsBase(ep.BaseURL)+"/v1/speech-to-text?enable_logging=false", mw.FormDataContentType(), &body,
		func(h http.Header) { h.Set("xi-api-key", ep.APIKey) })
	if err != nil {
		return "", nil, err
	}
	var out struct {
		Text  string           `json:"text"`
		Words []elevenLabsWord `json:"words"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, remoteError(ep, http.StatusOK, "unreadable response: "+clip(strings.TrimSpace(string(raw)), 300))
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return "", []Segment{}, nil
	}
	segs := wordSegments(out.Words)
	if len(segs) == 0 {
		segs = []Segment{{T0Ms: 0, T1Ms: len(samples) * 1000 / SampleRate, Text: text}}
	}
	return text, segs, nil
}

// ElevenLabsBase is the API's root for a configured URL: its paths carry their own version
// (/v1/speech-to-text, /v2/voices), so a pasted trailing /v1 or /v2 is dropped.
func ElevenLabsBase(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for _, v := range []string{"/v1", "/v2"} {
		u = strings.TrimSuffix(u, v)
	}
	return u
}

// elevenLabsWord is one entry of Scribe's words: a word, the spacing after it, or an audio
// event such as "(laughs)".
type elevenLabsWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Type  string  `json:"type"`
}

// wordSegments groups words into sentences (a word ending in . ? or !), whisper's segment
// size. Spacing joins words; audio events are not speech and are left out.
func wordSegments(words []elevenLabsWord) []Segment {
	segs := []Segment{}
	var cur strings.Builder
	var t0, t1 float64
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, Segment{T0Ms: ms(t0), T1Ms: ms(t1), Text: s})
		}
		cur.Reset()
	}
	for _, w := range words {
		switch w.Type {
		case "word":
			if strings.TrimSpace(cur.String()) == "" {
				cur.Reset()
				t0 = w.Start
			}
			cur.WriteString(w.Text)
			t1 = w.End
			if strings.HasSuffix(w.Text, ".") || strings.HasSuffix(w.Text, "?") || strings.HasSuffix(w.Text, "!") {
				flush()
			}
		case "spacing":
			if cur.Len() > 0 {
				cur.WriteString(w.Text)
			}
		}
	}
	flush()
	return segs
}

func ms(s float64) int { return int(s*1000 + 0.5) }
