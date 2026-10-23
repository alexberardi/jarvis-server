package tts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Remote TTS (docs/llm/07-remote-speech.md §3): tts.provider=remote sends each sentence to an
// OpenAI-compatible POST {tts.remote_url}/audio/speech and delivers what comes back as the
// same stream Kokoro produces, always 24 kHz s16le mono, so /audio/format and the X-Audio-*
// headers callers set before the first chunk stay true whatever the provider sends.

// RemoteProvider is the tts.provider value (and the Format provider) for a remote service.
const RemoteProvider = "remote"

// remoteSampleRate is the stable rate a remote stream is delivered at (Kokoro's).
const remoteSampleRate = kokoroSampleRate

// ErrRemoteNotConfigured is Speak's error for tts.provider=remote with no tts.remote_url. It
// is an ErrNotInstalled (503 on /speak, 409 on the admin sample): no voice is set up yet.
var ErrRemoteNotConfigured = fmt.Errorf("%w: tts.provider is remote but tts.remote_url is empty", ErrNotInstalled)

// remoteConfig is one call's remote settings.
type remoteConfig struct {
	url, api, model, key, voice string
	speed, gain                 float64
	timeout                     time.Duration
}

func (m *Module) provider(ctx context.Context) string {
	if m.settings == nil {
		return ProviderName
	}
	if p := m.settings.String(ctx, "tts.provider", settings.Scope{}); p == RemoteProvider {
		return p
	}
	return ProviderName
}

// remote reads the remote settings. tts.kokoro_* do not apply (docs/decisions/2026-10-22-remote-tts.md):
// speed and gain are 1 unless the call sets them.
func (m *Module) remote(ctx context.Context, opts SpeakOptions) remoteConfig {
	c := remoteConfig{voice: opts.Voice, speed: opts.Speed, gain: opts.Gain, api: "openai"}
	if s := m.settings; s != nil {
		sc := settings.Scope{}
		c.url = strings.TrimRight(strings.TrimSpace(s.String(ctx, "tts.remote_url", sc)), "/")
		if a := s.String(ctx, "tts.remote_api", sc); a != "" {
			c.api = a
		}
		c.model = s.String(ctx, "tts.remote_model", sc)
		c.key = s.String(ctx, "tts.remote_api_key", sc)
		if c.voice == "" {
			c.voice = s.String(ctx, "tts.remote_voice", sc)
		}
		c.timeout = time.Duration(s.Int(ctx, "tts.remote_timeout_ms", sc)) * time.Millisecond
	}
	if c.speed <= 0 {
		c.speed = 1
	}
	if c.gain <= 0 {
		c.gain = 1
	}
	if c.timeout <= 0 {
		c.timeout = 8 * time.Second
	}
	return c
}

// speakRemote is Speak for tts.provider=remote: one request per sentence, in order, each
// read as it arrives. The slot in m.sem is held until the stream ends.
func (m *Module) speakRemote(ctx context.Context, sentences []string, opts SpeakOptions) (*Stream, error) {
	c := m.remote(ctx, opts)
	if c.url == "" {
		return nil, ErrRemoteNotConfigured
	}
	select { // cap concurrent streams
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	sctx, cancel := context.WithCancel(ctx)
	ch := make(chan []byte, 1) // one chunk of look-ahead
	st := &Stream{Format: remoteFormat(), ch: ch, cancel: cancel}
	go func() {
		defer func() {
			<-m.sem
			close(ch)
		}()
		emit := func(b []byte) error {
			select {
			case ch <- b:
				return nil
			case <-sctx.Done():
				return sctx.Err()
			}
		}
		for i, sentence := range sentences {
			if sctx.Err() != nil {
				return
			}
			err := c.speak(sctx, m.httpClient(), sentence, emit)
			if err == nil {
				continue
			}
			if sctx.Err() != nil {
				return
			}
			st.err = err
			var se *statusError
			if errors.As(err, &se) && se.perSentence() {
				// The provider refused this sentence's text (e.g. punctuation only); the rest
				// may still render, as Kokoro skips a sentence it can't render.
				m.log().Warn("tts: remote sentence refused", "index", i, "chars", len(sentence), "err", err)
				continue
			}
			// Down, slow, or refusing us (key, quota): the next sentence would only wait for
			// the same answer.
			m.log().Error("tts: remote provider failed", "index", i, "url", c.url, "err", err)
			return
		}
	}()
	return st, nil
}

func remoteFormat() Format {
	return Format{SampleRate: remoteSampleRate, Channels: 1, SampleWidth: 2, Provider: RemoteProvider}
}

func (m *Module) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

// statusError is a non-2xx answer from the provider.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("remote tts: HTTP %d: %s", e.code, e.body)
}

// perSentence is true for an answer about this sentence's text rather than about us or the
// service: 400, 413, 422.
func (e *statusError) perSentence() bool {
	return e.code == http.StatusBadRequest || e.code == http.StatusRequestEntityTooLarge ||
		e.code == http.StatusUnprocessableEntity
}

// speak renders one sentence (openai dialect) and passes its PCM to emit in whole samples,
// 24 kHz s16le mono. timeout bounds the wait for the answer and each silence in its body
// after, not the whole body: a long sentence may stream for longer than that.
func (c remoteConfig) speak(ctx context.Context, client *http.Client, text string, emit func([]byte) error) error {
	if c.api != "openai" {
		return fmt.Errorf("remote tts: unsupported tts.remote_api %q", c.api)
	}
	body, _ := json.Marshal(map[string]any{
		"model": c.model, "input": text, "voice": c.voice, "response_format": "pcm", "speed": c.speed,
	})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(c.timeout, cancel)
	defer idle.Stop()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, c.url+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return c.timedOut(ctx, rctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	idle.Reset(c.timeout)
	// The clock stops while the caller hasn't taken the last chunk yet: a slow reader is not
	// a slow provider.
	emitIdle := func(b []byte) error {
		idle.Stop()
		err := emit(b)
		idle.Reset(c.timeout)
		return err
	}
	r := bufio.NewReaderSize(&idleReader{r: resp.Body, t: idle, d: c.timeout}, 16<<10)
	if head, _ := r.Peek(4); string(head) == "RIFF" {
		err = readWAV(r, c.gain, emitIdle)
	} else {
		err = readPCM(r, remoteSampleRate, 1, c.gain, emitIdle)
	}
	if err != nil {
		return c.timedOut(ctx, rctx, err)
	}
	return nil
}

// timedOut names a timeout (the idle timer cancelled rctx, not the caller) as one.
func (c remoteConfig) timedOut(ctx, rctx context.Context, err error) error {
	if ctx.Err() == nil && rctx.Err() != nil {
		return fmt.Errorf("remote tts: no answer from %s within %s", hostOf(c.url), c.timeout)
	}
	return fmt.Errorf("remote tts: %w", err)
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// idleReader pushes the idle timer back on every read that returns data.
type idleReader struct {
	r io.Reader
	t *time.Timer
	d time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.t.Reset(r.d)
	}
	return n, err
}

// readPCM streams s16le PCM at rate with channels to emit as 24 kHz mono, in whole frames: a
// network chunk that ends mid-sample carries its odd bytes over to the next.
func readPCM(r io.Reader, rate, channels int, gain float64, emit func([]byte) error) error {
	rs := audio.NewResampler(rate, remoteSampleRate)
	frame := 2 * channels
	buf := make([]byte, 8<<10)
	have := 0
	var mono, out []int16
	for {
		n, err := r.Read(buf[have:])
		have += n
		if whole := have - have%frame; whole > 0 {
			mono = mono[:0]
			for off := 0; off < whole; off += frame {
				var sum int
				for ch := 0; ch < channels; ch++ {
					sum += int(int16(binary.LittleEndian.Uint16(buf[off+2*ch:])))
				}
				mono = append(mono, int16(sum/channels))
			}
			out = rs.Process(mono, out[:0])
			if len(out) > 0 {
				if e := emit(encode(out, gain)); e != nil {
					return e
				}
			}
			have = copy(buf, buf[whole:have])
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// readWAV parses a RIFF/WAVE header as it streams and passes its 16-bit PCM data to readPCM.
// The data chunk's size is ignored (streaming servers write 0 or 0xFFFFFFFF): data runs to
// the end of the body.
func readWAV(r io.Reader, gain float64, emit func([]byte) error) error {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil || string(riff[8:12]) != "WAVE" {
		return errors.New("remote tts: a RIFF answer that is not WAVE")
	}
	channels, rate := 0, 0
	for {
		var h [8]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return fmt.Errorf("remote tts: WAV with no data chunk: %w", err)
		}
		id, size := string(h[0:4]), int64(binary.LittleEndian.Uint32(h[4:8]))
		switch id {
		case "fmt ":
			if size < 16 || size > 1<<10 {
				return fmt.Errorf("remote tts: WAV fmt chunk of %d bytes", size)
			}
			b := make([]byte, size+size%2)
			if _, err := io.ReadFull(r, b); err != nil {
				return fmt.Errorf("remote tts: WAV fmt: %w", err)
			}
			format := binary.LittleEndian.Uint16(b[0:2])
			channels = int(binary.LittleEndian.Uint16(b[2:4]))
			rate = int(binary.LittleEndian.Uint32(b[4:8]))
			bits := binary.LittleEndian.Uint16(b[14:16])
			if (format != 1 && format != 0xFFFE) || bits != 16 || channels < 1 || channels > 8 || rate < 8000 || rate > 192000 {
				return fmt.Errorf("remote tts: unsupported WAV (format %d, %d-bit, %d channels, %d Hz; want 16-bit PCM)", format, bits, channels, rate)
			}
		case "data":
			if channels == 0 {
				return errors.New("remote tts: WAV data before its fmt chunk")
			}
			return readPCM(r, rate, channels, gain, emit)
		default:
			if _, err := io.CopyN(io.Discard, r, size+size%2); err != nil {
				return fmt.Errorf("remote tts: WAV: %w", err)
			}
		}
	}
}

// encode writes samples as s16le with gain (multiply, clip).
func encode(samples []int16, gain float64) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		v := s
		if gain != 1 {
			v = int16(max(-32768, min(32767, float64(s)*gain)))
		}
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

// openAIVoices are the voices OpenAI's /audio/speech offers (gpt-4o-mini-tts; checked
// 2026-10-21). It has no endpoint that lists them.
var openAIVoices = []string{
	"alloy", "ash", "ballad", "cedar", "coral", "echo", "fable", "marin", "nova", "onyx", "sage", "shimmer", "verse",
}

// openAIDefaultVoice is the OpenAI preset's voice (docs/llm/07-remote-speech.md §1).
const openAIDefaultVoice = "marin"

// VoiceChoices is what GET /api/tts/voices offers: the active provider's voices, the one in
// use, the default, and the setting a choice is written to ("tts/<key>"). FreeText is true
// when the provider has no list we can read (an "Other" server without GET /audio/voices):
// any name is accepted.
type VoiceChoices struct {
	Provider string   `json:"provider"`
	Voices   []string `json:"voices"`
	Current  string   `json:"current"`
	Default  string   `json:"default"`
	Setting  string   `json:"setting"`
	FreeText bool     `json:"free_text"`
}

// voicesTimeout bounds asking an "Other" server for its voices.
const voicesTimeout = 5 * time.Second

// VoiceChoices lists the voices of the active provider: Kokoro's, OpenAI's fixed list when
// tts.remote_url is OpenAI's, else what GET {url}/audio/voices answers.
func (m *Module) VoiceChoices(ctx context.Context) VoiceChoices {
	if m.provider(ctx) != RemoteProvider {
		v := VoiceChoices{Provider: ProviderName, Voices: Voices(), Current: DefaultVoice, Default: DefaultVoice,
			Setting: "tts/tts.kokoro_voice"}
		if m.settings != nil {
			if cur := m.settings.String(ctx, "tts.kokoro_voice", settings.Scope{}); cur != "" {
				v.Current = cur
			}
		}
		return v
	}
	c := m.remote(ctx, SpeakOptions{})
	v := VoiceChoices{Provider: RemoteProvider, Voices: []string{}, Current: c.voice, Setting: "tts/tts.remote_voice"}
	if u, err := url.Parse(c.url); err == nil && u.Hostname() == "api.openai.com" {
		v.Voices, v.Default = append([]string(nil), openAIVoices...), openAIDefaultVoice
	} else if c.url != "" {
		names, err := c.listVoices(ctx, m.httpClient())
		if err != nil {
			m.log().Info("tts: the remote server lists no voices; the voice is free text", "url", c.url, "err", err)
		}
		if names != nil {
			v.Voices = names
		}
	}
	v.FreeText = len(v.Voices) == 0
	if v.Current == "" {
		v.Current = v.Default
	}
	return v
}

// listVoices is GET {url}/audio/voices. Servers disagree on the shape (Kokoro-FastAPI:
// {"voices": ["af_heart", …]}; others a list or {"data": […]} of objects with an id or name),
// so any of those is read.
func (c remoteConfig) listVoices(ctx context.Context, client *http.Client) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, voicesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/audio/voices", nil)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{code: resp.StatusCode}
	}
	var raw any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("voices: %w", err)
	}
	if obj, ok := raw.(map[string]any); ok {
		raw = obj["voices"]
		if raw == nil {
			raw = obj["data"]
		}
	}
	list, _ := raw.([]any)
	names := []string{}
	seen := map[string]bool{}
	for _, it := range list {
		var name string
		switch x := it.(type) {
		case string:
			name = x
		case map[string]any:
			for _, k := range []string{"voice_id", "id", "name"} {
				if s, ok := x[k].(string); ok && s != "" {
					name = s
					break
				}
			}
		}
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}
