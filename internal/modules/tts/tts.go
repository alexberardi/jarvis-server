// Package tts is the jarvisd tts module (jarvis-tts, legacy port 7707): Kokoro text-to-speech
// in the binary through sherpa-onnx (PLAN §3.3: kokoro-multi-lang-v1_0, bm_george = sid 26,
// speed 1.25, CPU fp32).
//
// HTTP, same paths and shapes as the legacy service (contract/tts_test.go):
//   - GET /ping, GET /health (shallow, with version), /settings (combined read, superuser write).
//   - GET /audio/format: {sample_rate, channels, sample_width, provider} without synthesizing.
//   - POST /speak {text}: one audio/wav.
//   - POST /speak/stream {text}: chunked audio/raw s16le PCM, one flush per sentence, with
//     X-Audio-Sample-Rate/Channels/Sample-Width/Provider.
//
// App-to-app auth on /audio/format and both /speak routes. Cut: /generate-wake-response (CC
// owns it) and Piper. tts.provider is kokoro (in the binary) or remote (remote.go: an
// OpenAI-compatible service, delivered in Kokoro's format).
//
// In process, command-center calls Speak, which returns the same per-sentence PCM stream.
//
// The engine loads lazily from the model manager's "tts" model (ModelPaths) and reloads when
// that path, or the voice's language, changes. sherpa-onnx serializes generation per engine;
// MaxConcurrent caps the streams in flight.
package tts

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the module's goose migrations, rooted at the migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at build time
	}
	return sub
}

// ServiceName is the legacy service name.
const ServiceName = "jarvis-tts"

// Definitions are the settings the module reads (docs/schema/tts.md). Cut: tts.default_voice
// (Piper), tts.kokoro_device (sherpa runs on CPU), tts.wake_system_prompt and
// tts.llm_proxy_version (the cut wake route), server.* and auth.* (jarvisd's own). All are read
// per request, so no reload is needed. The model itself is the llm module's tts.model.
var Definitions = []settings.Definition{
	{Key: "tts.provider", Category: "tts", Type: settings.String, Default: ProviderName,
		Description: "Active TTS provider: kokoro (local, default) or remote (an OpenAI-compatible service or ElevenLabs at tts.remote_url; each sentence's text is sent there). Piper is cut.",
		Options:     []any{ProviderName, RemoteProvider}},
	{Key: "tts.remote_url", Category: "tts", Type: settings.String, Default: "",
		Description: "Remote TTS base URL: for openai up to and including the version (e.g. https://api.openai.com/v1; speech is requested at {url}/audio/speech), for elevenlabs the API's root (https://api.elevenlabs.io)"},
	{Key: "tts.remote_api", Category: "tts", Type: settings.String, Default: APIOpenAI,
		Description: "Remote TTS API dialect: openai (POST /audio/speech) or elevenlabs (POST /v1/text-to-speech/{voice}/stream, key in xi-api-key)",
		Options:     []any{APIOpenAI, APIElevenLabs}},
	{Key: "tts.remote_model", Category: "tts", Type: settings.String, Default: "",
		Description: "Remote TTS model (e.g. gpt-4o-mini-tts for OpenAI, eleven_flash_v2_5 for ElevenLabs)"},
	{Key: "tts.remote_api_key", Category: "tts", Type: settings.String, Default: "", IsSecret: true,
		Description: "Remote TTS API key, sent as a bearer token (openai) or in xi-api-key (elevenlabs) (empty: none)"},
	{Key: "tts.remote_voice", Category: "tts", Type: settings.String, Default: "",
		Description: "Remote TTS voice, used for everything including phone calls (e.g. marin for OpenAI; a voice id for ElevenLabs, which needs one)"},
	{Key: "tts.remote_timeout_ms", Category: "tts", Type: settings.Int, Default: 8000,
		Description: "How long to wait for the remote TTS to answer a sentence, or to send more of it, before giving up (ms)"},
	{Key: "tts.fallback", Category: "tts", Type: settings.String, Default: fallback.ModeOff,
		Description: "When the remote TTS fails (down, timeout, rate limit, refused key): off (the reply stops) or cpu (the rest of the reply is spoken by local Kokoro in tts.kokoro_voice; install Kokoro from the Models page, nothing is downloaded for you)",
		Options:     []any{fallback.ModeOff, fallback.ModeCPU}},
	{Key: "tts.kokoro_voice", Category: "tts", Type: settings.String, Default: DefaultVoice,
		Description: "Kokoro voice ID (e.g., bm_george, bm_fable, af_heart)"},
	{Key: "tts.kokoro_speed", Category: "tts", Type: settings.Float, Default: 1.25,
		Description: "Kokoro speech speed multiplier"},
	{Key: "tts.kokoro_gain", Category: "tts", Type: settings.Float, Default: 2.0,
		Description: "Output amplitude multiplier for Kokoro (its raw output peaks ~0.3-0.5). 1.0 = unchanged, 2.0 ≈ +6 dB (default), 3.0 ≈ +9.5 dB (mild saturation on loud chunks)."},
}

// ModelPaths resolves an in-binary model kind ("tts") to its installed path. The llm module's
// model manager backs it (see ModelPathFunc for adapting Manager.ModelPath).
type ModelPaths interface {
	ModelPath(ctx context.Context, kind string) (string, bool)
}

// ModelPathFunc adapts a function to ModelPaths.
type ModelPathFunc func(ctx context.Context, kind string) (string, bool)

func (f ModelPathFunc) ModelPath(ctx context.Context, kind string) (string, bool) {
	return f(ctx, kind)
}

// Module is the tts module.
type Module struct {
	// Auth validates app-to-app callers (ValidateApp).
	Auth authn.Authority
	// SettingsRead (app credentials or superuser JWT) and SettingsWrite (superuser) guard
	// /settings. Either nil: /settings is not mounted.
	SettingsRead, SettingsWrite settings.Guard
	// Version is what /health reports.
	Version string

	// Models resolves the Kokoro directory ("tts"). ModelDir, when set, overrides it (tests,
	// operators).
	Models   ModelPaths
	ModelDir string
	// LibDir is where the embedded sherpa-onnx libraries are extracted. Default <home>/lib.
	LibDir string
	// NumThreads per generation (default min(4, NumCPU)).
	NumThreads int
	// MaxConcurrent caps streams in flight; more wait for a slot (default 4). Generation itself
	// is serialized per engine, so this bounds queueing and memory, not CPU.
	MaxConcurrent int
	// NoWarm skips loading the engine in the background at Start.
	NoWarm bool
	// HTTPClient calls a remote provider (default http.DefaultClient; per-request timeouts
	// come from tts.remote_timeout_ms).
	HTTPClient *http.Client

	// newSynth replaces the sherpa loader (tests).
	newSynth func(engineKey) (synth, error)

	deps     module.Deps
	settings *settings.Service
	once     sync.Once
	eng      *engines
	sem      chan struct{}

	// fallback is the remote provider's fallback state (fallback.go).
	fallback fallback.Gate
}

func (m *Module) Name() string      { return "tts" }
func (m *Module) Listener() string  { return pconfig.ListenerTTS }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Settings is the module's settings service (valid after Register), for the admin aggregator.
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) log() *slog.Logger {
	if m.deps.Log != nil {
		return m.deps.Log
	}
	return slog.Default()
}

// init builds the engine holder (idempotent; Register calls it, Speak too).
func (m *Module) init() {
	m.once.Do(func() {
		load := m.newSynth
		if load == nil {
			libDir := m.LibDir
			if libDir == "" {
				libDir = filepath.Join(m.deps.Config.Home, "lib")
			}
			threads := m.NumThreads
			if threads <= 0 {
				threads = min(4, runtime.NumCPU())
			}
			load = kokoroLoader(libDir, threads)
		}
		m.eng = &engines{load: load, log: m.log(), retryAfter: 30 * time.Second, now: time.Now}
		n := m.MaxConcurrent
		if n <= 0 {
			n = 4
		}
		m.sem = make(chan struct{}, n)
	})
}

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.Version == "" {
		m.Version = "dev"
	}
	svc, err := settings.New(deps.DB, "tts", Definitions, deps.Log)
	if err != nil {
		panic(err) // static definitions
	}
	m.settings = svc
	m.init()
	if m.SettingsRead != nil && m.SettingsWrite != nil {
		svc.Mount(mux, m.SettingsRead, m.SettingsWrite)
	}

	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "pong"})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "healthy", "version": m.Version})
	})
	mux.HandleFunc("GET /audio/format", m.app(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, m.AudioFormat())
	}))
	mux.HandleFunc("POST /speak", m.app(m.handleSpeak))
	mux.HandleFunc("POST /speak/stream", m.app(m.handleSpeakStream))
}

// Start migrates the settings table, warms the engine in the background (as the legacy
// service pre-warmed its provider) and releases it at shutdown.
func (m *Module) Start(ctx context.Context) error {
	if err := m.settings.Migrate(ctx); err != nil {
		return err
	}
	go func() {
		if !m.NoWarm {
			m.warm(ctx)
		}
		<-ctx.Done()
		m.eng.shutdown()
	}()
	return nil
}

// warm loads the engine for the configured voice if a model is installed. Failure is only
// logged: the first request retries.
func (m *Module) warm(ctx context.Context) {
	if m.provider(ctx) == RemoteProvider {
		return
	}
	dir, ok := m.modelDir(ctx)
	if !ok {
		m.log().Info("tts: no Kokoro model installed yet; /speak answers 503 until one is")
		return
	}
	ref, err := m.eng.acquire(keyFor(dir, m.resolve(ctx, SpeakOptions{}).voice), true)
	if err != nil {
		return
	}
	m.eng.release(ref)
}

// --- auth ---

// app is jarvis-auth-client's require_app_auth: 401 "Missing app credentials" / "Invalid app
// credentials". An auth outage is 502 here (legacy: 401 "Auth service unavailable: ...").
func (m *Module) app(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key, ok := authn.AppCreds(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "Missing app credentials")
			return
		}
		a, valid, err := m.Auth.ValidateApp(r.Context(), id, key)
		if err != nil {
			m.log().Error("tts: app validation failed", "err", err)
			httpx.Error(w, http.StatusBadGateway, "Auth service unavailable")
			return
		}
		if !valid {
			httpx.Error(w, http.StatusUnauthorized, "Invalid app credentials")
			return
		}
		if a.ID == "" {
			a.ID = id
		}
		h(w, r.WithContext(authn.WithApp(r.Context(), a)))
	}
}

// --- synthesis routes ---

// readText reads {"text": ...}. ok=false means a response was written.
func readText(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body map[string]any
	if !httpx.DecodeJSON(w, r, &body) {
		return "", false
	}
	switch t := body["text"].(type) {
	case nil:
	case string:
		if t != "" {
			return t, true
		}
	case bool:
		if t {
			httpx.Error(w, http.StatusUnprocessableEntity, "text must be a string")
			return "", false
		}
	case float64:
		if t != 0 {
			httpx.Error(w, http.StatusUnprocessableEntity, "text must be a string")
			return "", false
		}
	default:
		httpx.Error(w, http.StatusUnprocessableEntity, "text must be a string")
		return "", false
	}
	// LEGACY-BUG (contract TestTTSEmptyText): empty text is 200 with a JSON error body.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"error": "No text provided"})
	return "", false
}

func (m *Module) speakError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotInstalled):
		httpx.Error(w, http.StatusServiceUnavailable, "TTS model not installed")
	case r.Context().Err() != nil:
		// the caller left; nothing to answer
	default:
		m.log().Error("tts: synthesis failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Speech synthesis failed: "+err.Error())
	}
}

func (m *Module) logRequest(r *http.Request, route string, chars int) {
	app, _ := authn.AppFrom(r.Context())
	m.log().Debug("tts: request", "route", route, "app", app.ID, "chars", chars,
		"household_id", r.Header.Get("X-Context-Household-Id"), "node_id", r.Header.Get("X-Context-Node-Id"))
}

func (m *Module) handleSpeak(w http.ResponseWriter, r *http.Request) {
	text, ok := readText(w, r)
	if !ok {
		return
	}
	m.logRequest(r, "speak", len(text))
	st, err := m.Speak(r.Context(), text, SpeakOptions{})
	if err != nil {
		m.speakError(w, r, err)
		return
	}
	defer st.Close()
	var pcm bytes.Buffer
	if _, err := io.Copy(&pcm, st); err != nil {
		m.speakError(w, r, err)
		return
	}
	var out bytes.Buffer
	_ = audio.WritePCM16WAV(&out, pcm.Bytes(), st.Format.Channels, st.Format.SampleRate)
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.Itoa(out.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}

func (m *Module) handleSpeakStream(w http.ResponseWriter, r *http.Request) {
	text, ok := readText(w, r)
	if !ok {
		return
	}
	m.logRequest(r, "speak/stream", len(text))
	start := time.Now()
	st, err := m.Speak(r.Context(), text, SpeakOptions{})
	if err != nil {
		m.speakError(w, r, err)
		return
	}
	defer st.Close()
	h := w.Header()
	h.Set("Content-Type", "audio/raw")
	h.Set("X-Audio-Sample-Rate", strconv.Itoa(st.Format.SampleRate))
	h.Set("X-Audio-Channels", strconv.Itoa(st.Format.Channels))
	h.Set("X-Audio-Sample-Width", strconv.Itoa(st.Format.SampleWidth))
	h.Set("X-Audio-Provider", st.Format.Provider)
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	var first time.Duration
	chunks := 0
	for {
		b, err := st.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				m.log().Error("tts: stream produced no audio", "err", err)
			}
			break
		}
		if chunks == 0 {
			first = time.Since(start)
		}
		chunks++
		if _, err := w.Write(b); err != nil {
			return // client gone; Close stops rendering
		}
		_ = rc.Flush()
	}
	m.log().Info("tts: stream done", "chars", len(text), "chunks", chunks,
		"first_chunk", first.Round(time.Millisecond), "total", time.Since(start).Round(time.Millisecond))
}
