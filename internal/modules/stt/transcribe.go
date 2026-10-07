package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Segment is one whisper segment, in milliseconds (legacy run_whisper: whisper.cpp's
// centisecond t0/t1 × 10).
type Segment struct {
	T0Ms int    `json:"t0_ms"`
	T1Ms int    `json:"t1_ms"`
	Text string `json:"text"`
}

// Result is POST /transcribe's body. Affect is always null (D38).
type Result struct {
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
	Speaker  Speaker   `json:"speaker"`
	Affect   any       `json:"affect"`
}

// TranscribeOptions are the knobs of one transcription. Zero values take the settings.
type TranscribeOptions struct {
	// SpeakerAudio, when set, is the clip the speaker pass embeds instead of the transcribed
	// audio (the node's wake + command concatenation, invariant 4). A WAV, like the audio.
	SpeakerAudio []byte
	// Speaker, when set, runs the speaker pass in that scope (if the household has
	// voice.recognition_enabled). Nil skips it (legacy ?speaker_recognition=false).
	Speaker *SpeakerScope
	// Prompt is whisper's initial prompt.
	Prompt string
	// Temperature and TemperatureInc override whisper.default_temperature(_inc) when non-nil.
	Temperature, TemperatureInc *float64
	// BeamSize overrides whisper.default_beam_size when > 0 (1-16).
	BeamSize int
	// Preprocess applies the legacy normalise + trim pipeline before whisper.
	Preprocess bool
}

// EngineError is whisper-server failing a request (legacy 500 {"error", "stderr"}).
type EngineError struct {
	Msg    string
	Stderr string
}

func (e *EngineError) Error() string { return e.Msg }

// ErrEngineUnavailable wraps a resolver failure: no stt model, engine starting or failed.
var ErrEngineUnavailable = errors.New("stt: speech-to-text engine unavailable")

// Transcribe is the in-process POST /transcribe: audio is a WAV at any rate, resampled to
// 16 kHz mono for whisper-server and for the speaker pass, which run concurrently.
func (m *Module) Transcribe(ctx context.Context, wav []byte, opts TranscribeOptions) (Result, error) {
	samples, err := DecodeWAV(wav)
	if err != nil {
		return Result{}, err
	}
	var spk []float32
	if opts.Speaker != nil && len(opts.SpeakerAudio) > 0 {
		if spk, err = DecodeWAV(opts.SpeakerAudio); err != nil {
			return Result{}, err
		}
	}
	return m.transcribe(ctx, samples, spk, opts)
}

func (m *Module) transcribe(ctx context.Context, samples, speakerSamples []float32, opts TranscribeOptions) (Result, error) {
	start := m.now()
	whisperIn := samples
	if opts.Preprocess {
		whisperIn = Preprocess(samples)
	}
	sys := settings.Scope{}
	p := inferenceParams{
		Language:       m.settings.String(ctx, "whisper.language", sys),
		Prompt:         opts.Prompt,
		Temperature:    m.settings.Float(ctx, "whisper.default_temperature", sys),
		TemperatureInc: m.settings.Float(ctx, "whisper.default_temperature_inc", sys),
		BeamSize:       int(m.settings.Int(ctx, "whisper.default_beam_size", sys)),
	}
	if opts.Temperature != nil {
		p.Temperature = *opts.Temperature
	}
	if opts.TemperatureInc != nil {
		p.TemperatureInc = *opts.TemperatureInc
	}
	if opts.BeamSize > 0 {
		p.BeamSize = opts.BeamSize
	}

	// The speaker pass reads the raw audio (never the preprocessed copy) and runs beside
	// whisper: they use different engines, and a failure there never fails the transcript.
	speaker := Speaker{Outcome: OutcomeSkipped}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if opts.Speaker == nil {
			return
		}
		in := samples
		if len(speakerSamples) > 0 {
			in = speakerSamples
		}
		sp, err := m.Identify(ctx, *opts.Speaker, in)
		if errors.Is(err, ErrRecognitionOff) {
			m.logRecognitionOff(opts.Speaker.HouseholdID)
		}
		speaker = sp
	}()
	text, segs, err := m.infer(ctx, whisperIn, p)
	<-done
	if err != nil {
		return Result{}, err
	}
	if speaker.Outcome != OutcomeMatched {
		speaker.UserID = nil
	}
	m.deps.Log.Info("stt: transcribed", "chars", len(text), "segments", len(segs),
		"speaker_outcome", speaker.Outcome, "ms", m.now().Sub(start).Milliseconds())
	return Result{Text: text, Segments: segs, Speaker: speaker, Affect: nil}, nil
}

// logRecognitionOff makes a disabled flag visible (at most once a minute), so it isn't
// mistaken for a model that never matches.
func (m *Module) logRecognitionOff(householdID string) {
	m.offLogMu.Lock()
	defer m.offLogMu.Unlock()
	if now := m.now(); now.Sub(m.offLogAt) > time.Minute {
		m.offLogAt = now
		m.deps.Log.Warn("stt: speaker_recognition_disabled (voice.recognition_enabled=false)", "household", householdID)
	}
}

type inferenceParams struct {
	Language       string
	Prompt         string
	Temperature    float64
	TemperatureInc float64
	BeamSize       int
}

// verboseJSON is the part of whisper-server's response_format=verbose_json we read.
type verboseJSON struct {
	Text     string `json:"text"`
	Segments []struct {
		Text  string  `json:"text"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	} `json:"segments"`
	Error string `json:"error"`
}

// infer posts the clip to whisper-server's /inference with the legacy decode parameters.
func (m *Module) infer(ctx context.Context, samples []float32, p inferenceParams) (string, []Segment, error) {
	if m.Engine == nil {
		return "", nil, fmt.Errorf("%w: no engine resolver", ErrEngineUnavailable)
	}
	base, err := m.Engine.Resolve(ctx, "stt")
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "audio.wav")
	fw.Write(EncodeWAV(samples))
	fields := [][2]string{
		{"response_format", "verbose_json"},
		{"language", p.Language},
		{"temperature", strconv.FormatFloat(p.Temperature, 'f', -1, 64)},
		{"temperature_inc", strconv.FormatFloat(p.TemperatureInc, 'f', -1, 64)},
		{"beam_size", strconv.Itoa(p.BeamSize)},
	}
	if p.Prompt != "" {
		fields = append(fields, [2]string{"prompt", p.Prompt})
	}
	for _, f := range fields {
		mw.WriteField(f[0], f[1])
	}
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/inference", &body)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", nil, &EngineError{Msg: "Whisper transcription failed: " + err.Error(), Stderr: err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", nil, &EngineError{Msg: "Whisper transcription failed: " + err.Error(), Stderr: err.Error()}
	}
	var out verboseJSON
	jerr := json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || jerr != nil || out.Error != "" {
		detail := strings.TrimSpace(string(raw))
		if out.Error != "" {
			detail = out.Error
		}
		return "", nil, &EngineError{Msg: fmt.Sprintf("Whisper transcription failed: whisper-server %d: %s", resp.StatusCode, detail), Stderr: detail}
	}
	segs := make([]Segment, 0, len(out.Segments))
	texts := make([]string, 0, len(out.Segments))
	for _, s := range out.Segments {
		segs = append(segs, Segment{T0Ms: centiMs(s.Start), T1Ms: centiMs(s.End), Text: s.Text})
		texts = append(texts, s.Text)
	}
	text := strings.TrimSpace(strings.Join(texts, " "))
	if len(out.Segments) == 0 {
		text = strings.TrimSpace(out.Text)
	}
	return text, segs, nil
}

// centiMs turns whisper-server's seconds (centisecond-exact) into legacy milliseconds.
func centiMs(sec float64) int { return int(math.Round(sec*100)) * 10 }

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
