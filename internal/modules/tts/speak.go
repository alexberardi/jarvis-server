package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// ErrNoText is returned by Speak for empty text.
var ErrNoText = errors.New("tts: no text provided")

// kokoroSampleRate is what Kokoro renders (and what legacy advertised for it). /audio/format
// reports it before an engine is loaded.
const kokoroSampleRate = 24000

// ProviderName is the X-Audio-Provider / /audio/format provider.
const ProviderName = "kokoro"

// Format describes the PCM a stream carries: always mono signed 16-bit little-endian.
type Format struct {
	SampleRate  int    `json:"sample_rate"`
	Channels    int    `json:"channels"`
	SampleWidth int    `json:"sample_width"`
	Provider    string `json:"provider"`
}

// SpeakOptions override the settings for one call. Zero values use the settings
// (tts.kokoro_voice, tts.kokoro_speed, tts.kokoro_gain).
type SpeakOptions struct {
	Voice string
	Speed float64
	Gain  float64
}

// Stream is synthesized speech, rendered sentence by sentence in the background: the next
// sentence renders while the caller consumes the current one. Read it as an io.Reader of raw
// PCM, or chunk by chunk with Next (one chunk per sentence). Close it when done (it stops the
// rendering if the caller gives up early).
type Stream struct {
	Format Format

	ch     <-chan []byte
	cancel context.CancelFunc
	buf    []byte
	err    error // set by the producer before ch closes
	got    bool
}

// Next returns the next sentence's PCM, or io.EOF at the end. If every sentence failed to
// render, the end is the last render error instead of io.EOF.
func (s *Stream) Next() ([]byte, error) {
	b, ok := <-s.ch
	if !ok {
		if !s.got && s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
	s.got = true
	return b, nil
}

// Read implements io.Reader over the concatenated PCM.
func (s *Stream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		b, err := s.Next()
		if err != nil {
			return 0, err
		}
		s.buf = b
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

// Close stops rendering. It is safe to call more than once.
func (s *Stream) Close() error {
	s.cancel()
	return nil
}

// AudioFormat is the format Speak produces, without synthesizing (GET /audio/format).
func (m *Module) AudioFormat() Format {
	rate := kokoroSampleRate
	if m.eng != nil {
		if r := m.eng.sampleRate(); r > 0 {
			rate = r
		}
	}
	return Format{SampleRate: rate, Channels: 1, SampleWidth: 2, Provider: ProviderName}
}

type speakConfig struct {
	voice voice
	speed float32
	gain  float64
}

func (m *Module) resolve(ctx context.Context, opts SpeakOptions) speakConfig {
	name, speed, gain := opts.Voice, opts.Speed, opts.Gain
	if m.settings != nil {
		if name == "" {
			name = m.settings.String(ctx, "tts.kokoro_voice", settings.Scope{})
		}
		if speed <= 0 {
			speed = m.settings.Float(ctx, "tts.kokoro_speed", settings.Scope{})
		}
		if gain <= 0 {
			gain = m.settings.Float(ctx, "tts.kokoro_gain", settings.Scope{})
		}
	}
	if speed <= 0 {
		speed = 1.25
	}
	if gain <= 0 {
		gain = 2.0
	}
	v, ok := voiceByName(name)
	if !ok {
		if name != "" {
			m.log().Warn("tts: unknown Kokoro voice; using the default", "voice", name, "default", DefaultVoice)
		}
		v, _ = voiceByName(DefaultVoice)
	}
	return speakConfig{voice: v, speed: float32(speed), gain: gain}
}

// Speak renders text as streamed PCM (the in-process form of POST /speak/stream, for
// command-center). The engine loads on first use from the model manager's "tts" model. It
// returns ErrNoText for empty text, ErrNotInstalled when no model is available, or ctx's error
// while waiting for a synthesis slot. Rendering stops when ctx ends or the stream is closed.
func (m *Module) Speak(ctx context.Context, text string, opts SpeakOptions) (*Stream, error) {
	if text == "" {
		return nil, ErrNoText
	}
	m.init()
	sentences := splitSentences(text)
	if len(sentences) == 0 {
		sentences = []string{text}
	}
	cfg := m.resolve(ctx, opts)

	select { // cap concurrent streams
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, installed := m.modelDir(ctx)
	var key engineKey
	if installed {
		key = keyFor(dir, cfg.voice)
	}
	ref, err := m.eng.acquire(key, installed)
	if err != nil {
		<-m.sem
		return nil, err
	}
	sid := cfg.voice.SID
	if sid >= ref.s.NumSpeakers() {
		m.log().Warn("tts: voice not in this model's pack; using speaker 0", "voice", cfg.voice.Name, "speakers", ref.s.NumSpeakers())
		sid = 0
	}

	sctx, cancel := context.WithCancel(ctx)
	ch := make(chan []byte, 1) // one sentence of look-ahead
	st := &Stream{
		Format: Format{SampleRate: ref.s.SampleRate(), Channels: 1, SampleWidth: 2, Provider: ProviderName},
		ch:     ch, cancel: cancel,
	}
	go func() {
		defer func() {
			close(ch)
			m.eng.release(ref)
			<-m.sem
		}()
		for i, sentence := range sentences {
			if sctx.Err() != nil {
				return
			}
			samples, err := ref.s.Generate(sentence, sid, cfg.speed)
			if err != nil {
				// Per-sentence failures (e.g. punctuation-only text renders nothing) are skipped,
				// as command-center did per sentence.
				m.log().Warn("tts: sentence failed", "index", i, "chars", len(sentence), "err", err)
				st.err = err
				continue
			}
			select {
			case ch <- pcm16(samples, cfg.gain):
			case <-sctx.Done():
				return
			}
		}
	}()
	return st, nil
}

// pcm16 converts [-1, 1] samples to little-endian s16 with the legacy gain (multiply, clip).
func pcm16(samples []float32, gain float64) []byte {
	out := make([]byte, 2*len(samples))
	g := float32(gain)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(audio.FloatToPCM16(s*g)))
	}
	return out
}

// Sample renders text in one voice as a WAV file: the admin's voice preview (AD3b). An unknown
// voice falls back to the default as Speak does; callers validate the name against Voices.
func (m *Module) Sample(ctx context.Context, voice, text string) ([]byte, error) {
	st, err := m.Speak(ctx, text, SpeakOptions{Voice: voice})
	if err != nil {
		return nil, err
	}
	defer st.Close()
	pcm, err := io.ReadAll(st)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := audio.WritePCM16WAV(&out, pcm, st.Format.Channels, st.Format.SampleRate); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
