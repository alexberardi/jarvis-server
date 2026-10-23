package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// inferOpenAI transcribes the clip on an OpenAI-compatible provider (OpenAI, Groq, speaches,
// LocalAI): multipart POST {base}/audio/transcriptions with the 16 kHz WAV, the model, the
// language and the prompt, response_format=json (the gpt-transcribe family accepts nothing
// else). Temperature and beam size have no equivalent and are dropped. json carries no
// timings, so the transcript is one segment spanning the clip (docs/llm/07-remote-speech.md §2).
//
// The key goes only in the Authorization header: neither it nor the headers are logged, and an
// error never contains it.
func (m *Module) inferOpenAI(ctx context.Context, ep Endpoint, samples []float32, p inferenceParams) (string, []Segment, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "audio.wav")
	fw.Write(EncodeWAV(samples))
	fields := [][2]string{{"model", ep.Model}, {"response_format", "json"}}
	if p.Language != "" {
		fields = append(fields, [2]string{"language", p.Language})
	}
	if p.Prompt != "" {
		fields = append(fields, [2]string{"prompt", p.Prompt})
	}
	for _, f := range fields {
		mw.WriteField(f[0], f[1])
	}
	mw.Close()

	if ep.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ep.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ep.BaseURL, "/")+"/audio/transcriptions", &body)
	if err != nil {
		return "", nil, remoteError(ep, 0, "bad URL")
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", nil, remoteError(ep, 0, fmt.Sprintf("no answer within %s", ep.Timeout))
		}
		return "", nil, remoteError(ep, 0, err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", nil, remoteError(ep, 0, err.Error())
	}
	var out struct {
		Text string `json:"text"`
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// A provider may echo part of the key in its message: say only what happened.
		return "", nil, remoteError(ep, resp.StatusCode, fmt.Sprintf("%d: the provider refused the API key", resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		return "", nil, remoteError(ep, resp.StatusCode, fmt.Sprintf("%d: %s", resp.StatusCode, clip(strings.TrimSpace(string(raw)), 300)))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, remoteError(ep, resp.StatusCode, "unreadable response: "+clip(strings.TrimSpace(string(raw)), 300))
	}
	text := strings.TrimSpace(out.Text)
	segs := []Segment{}
	if text != "" {
		segs = append(segs, Segment{T0Ms: 0, T1Ms: len(samples) * 1000 / SampleRate, Text: text})
	}
	return text, segs, nil
}

// remoteError is a failed remote transcription, with the key scrubbed from the detail. status
// is the provider's HTTP status, 0 for a transport error or timeout.
func remoteError(ep Endpoint, status int, detail string) *EngineError {
	if ep.APIKey != "" {
		detail = strings.ReplaceAll(detail, ep.APIKey, "***")
	}
	return &EngineError{Msg: "Remote transcription failed: " + detail, Stderr: detail, Remote: true, Status: status}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
