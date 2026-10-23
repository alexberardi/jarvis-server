// Package stt is the jarvisd stt module (jarvis-whisper-api, legacy listener "whisper", port
// 7706): speech-to-text and the voice-profile store.
//
//   - Speech-to-text runs on a supervised whisper.cpp whisper-server (D7). The llm module's
//     engine stack owns the process; this module only asks the resolver for the "stt" label's
//     root URL and posts each clip to /inference with the legacy decode parameters. With
//     stt.engine=remote the clip goes to an OpenAI-compatible provider instead
//     (remote_openai.go; docs/llm/07-remote-speech.md).
//   - Speaker identification runs in-binary with sherpa-onnx (ERes2Net; the model manager
//     gives the .onnx path). One threshold plus a margin gate (D33), off by default per
//     household (D35), voiceprints only (D34), per (household, user) (D36), an enrollment
//     quality gate (D37), no affect pass (D38).
//
// Two ways in: HTTP with the legacy paths and shapes (app-to-app auth, household context in
// the X-Context-* headers, as command-center's media proxy sends them), and in process for
// command-center: Transcribe, Identify, Enroll, Verify, the profile calls and PurgeUser.
package stt

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

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
const ServiceName = "jarvis-whisper-api"

// Endpoint is where a label's transcription goes: a local whisper-server's root (POST
// /inference), or a remote provider (docs/llm/07-remote-speech.md §2).
type Endpoint struct {
	BaseURL string
	// Remote: BaseURL is a provider's OpenAI-compatible base (POST {BaseURL}/audio/transcriptions).
	Remote bool
	// API is a remote endpoint's dialect (APIOpenAI).
	API string
	// APIKey goes in "Authorization: Bearer"; never logged or put in an error.
	APIKey string
	// Model is the remote request's "model".
	Model string
	// Timeout bounds one remote request; 0 = the HTTP client's.
	Timeout time.Duration
	// Fallback is a remote label's fallback setting: fallback.ModeCPU sends the clip to
	// Module.FallbackEngine when the provider fails; "" or off fails as is.
	Fallback string
}

// Remote dialects.
const APIOpenAI = "openai"

// STTEngine resolves an engine label to its endpoint. The llm module's engine.Resolver
// satisfies it through a small adapter.
type STTEngine interface {
	Resolve(ctx context.Context, label string) (Endpoint, error)
}

// ModelPaths resolves a voice model kind ("speaker") to a file. The llm module's model
// manager satisfies it through a small adapter.
type ModelPaths interface {
	ModelPath(ctx context.Context, kind string) (string, bool)
}

// ResolveFunc adapts a function to STTEngine.
type ResolveFunc func(ctx context.Context, label string) (Endpoint, error)

func (f ResolveFunc) Resolve(ctx context.Context, label string) (Endpoint, error) {
	return f(ctx, label)
}

// LocalURL is an STTEngine that always answers a local whisper-server at url (tests, tools).
func LocalURL(url string) STTEngine {
	return ResolveFunc(func(context.Context, string) (Endpoint, error) { return Endpoint{BaseURL: url}, nil })
}

// ModelPathFunc adapts a function to ModelPaths.
type ModelPathFunc func(ctx context.Context, kind string) (string, bool)

func (f ModelPathFunc) ModelPath(ctx context.Context, kind string) (string, bool) {
	return f(ctx, kind)
}

// Definitions are the settings the module reads (docs/schema/stt.md). Dropped: the engine
// keys (whisper.use_gpu, model_path, allow_model_autodownload: the llm module's stt.* labels
// own whisper-server now), voice.encoder (the model is whatever the model manager installs,
// tagged per voiceprint, D34), the short/long thresholds and cutoffs (D33), the embed
// preprocessing knobs (ECAPA only), voice.emotion_* (D38), server.* and
// auth.cache_ttl_seconds. No env fallbacks (M3).
var Definitions = []settings.Definition{
	{Key: "whisper.default_temperature", Category: "whisper", Type: settings.Float, Default: 0.0,
		Description: "Default initial temperature for sampling (0.0-1.0), used when the caller doesn't pass temperature"},
	{Key: "whisper.default_temperature_inc", Category: "whisper", Type: settings.Float, Default: 0.2,
		Description: "Default temperature increment on decode failure (0.0-1.0)"},
	{Key: "whisper.default_beam_size", Category: "whisper", Type: settings.Int, Default: int64(2),
		Description: "Default beam size for beam search (1-16), used when the caller doesn't pass beam_size. Decode time scales with beam size and STT sits on the voice hot path, so this defaults low."},
	{Key: "whisper.language", Category: "whisper", Type: settings.String, Default: "en",
		Description: "Language for transcription. The request's language field is accepted and ignored (M13)."},
	{Key: "voice.recognition_enabled", Category: "voice", Type: settings.Bool, Default: false,
		Description: "Enable speaker identification. Per household; off by default, and enrolling does not turn it on (D35)."},
	{Key: "voice.similarity_threshold", Category: "voice", Type: settings.Float, Default: 0.43,
		Description: "Cosine similarity a speaker must exceed to be identified, for every clip length and for verify (D33). 0.43 is the ERes2Net calibration on real node-mic recordings."},
	{Key: "voice.min_speaker_margin", Category: "voice", Type: settings.Float, Default: 0.05,
		Description: "Open-set/ambiguity gate. With 2+ enrolled voices, a recognition is rejected (unknown speaker) when the winner beats the runner-up by less than this margin. 0 disables the gate."},
	{Key: "voice.enroll_min_speech_seconds", Category: "voice", Type: settings.Float, Default: 3.0,
		Description: "Enrollment quality gate (D37): a take with less speech than this (energy VAD) is rejected as low_quality and not stored. Only voiced 20 ms frames count, not the pauses between words, so 3.0 s is about 4 s of talking."},
	{Key: "voice.enroll_min_consistency", Category: "voice", Type: settings.Float, Default: 0.3,
		Description: "Enrollment quality gate (D37): a take whose cosine to the user's other takes is below this is rejected as low_quality (someone else, or noise). 0 disables it."},
}

// Module is the stt module.
type Module struct {
	// Auth validates app-to-app callers (ValidateApp).
	Auth authn.Authority
	// SettingsRead/SettingsWrite guard /settings (combined read, superuser write).
	SettingsRead, SettingsWrite settings.Guard
	// Version is what /health reports.
	Version string

	// Engine resolves the "stt" label to whisper-server or a remote provider.
	Engine STTEngine
	// FallbackEngine resolves the "stt" label to a local whisper-server forced to CPU,
	// whatever its engine setting, for a remote label with fallback=cpu. Nil: no fallback.
	FallbackEngine STTEngine
	// Models resolves the "speaker" model path.
	Models ModelPaths
	// Speaker, when set, replaces the model loaded through Models (tests).
	Speaker SpeakerModel
	// LibDir is where the sherpa-onnx libraries are extracted. Default <home>/lib.
	LibDir string
	// MaxUploadBytes caps each uploaded file (legacy WHISPER_MAX_UPLOAD_BYTES, 25 MB).
	MaxUploadBytes int64
	// HTTPClient talks to whisper-server. Default: a client with a 5-minute timeout.
	HTTPClient *http.Client

	deps     module.Deps
	settings *settings.Service
	now      func() time.Time

	modelMu sync.Mutex
	model   *loadedModel
	retired []SpeakerModel

	offLogMu sync.Mutex
	offLogAt time.Time

	// fallback is the remote label's fallback state (fallback.go).
	fallback     fallback.Gate
	fallbackMode atomic.Value // string: the latest request's fallback setting
}

func (m *Module) Name() string      { return "stt" }
func (m *Module) Listener() string  { return pconfig.ListenerSTT }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Settings is the module's settings service (valid after Register), for the admin aggregator.
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) libDir() string {
	if m.LibDir != "" {
		return m.LibDir
	}
	return filepath.Join(m.deps.Config.Home, "lib")
}

func (m *Module) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return defaultClient
}

var defaultClient = &http.Client{Timeout: 5 * time.Minute}

func (m *Module) maxUpload() int64 {
	if m.MaxUploadBytes > 0 {
		return m.MaxUploadBytes
	}
	return 25 << 20
}

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.now == nil {
		m.now = time.Now
	}
	m.fallback.Now = func() time.Time { return m.now() }
	if m.Version == "" {
		m.Version = "dev"
	}
	svc, err := settings.New(deps.DB, "stt", Definitions, deps.Log)
	if err != nil {
		panic(err) // static definitions
	}
	m.settings = svc
	if m.SettingsRead != nil && m.SettingsWrite != nil {
		svc.Mount(mux, m.SettingsRead, m.SettingsWrite)
	}

	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "pong"})
	})
	mux.HandleFunc("GET /health", m.handleHealth)
	mux.HandleFunc("POST /transcribe", m.app(m.handleTranscribe))

	mux.HandleFunc("POST /voice-profiles/enroll", m.app(m.handleEnroll))
	mux.HandleFunc("POST /voice-profiles/verify", m.app(m.handleVerify))
	mux.HandleFunc("GET /voice-profiles/check", m.app(m.handleCheck))
	mux.HandleFunc("GET /voice-profiles", m.app(m.handleList))
	mux.HandleFunc("GET /voice-profiles/{user_id}/samples", m.app(m.handleSamples))
	mux.HandleFunc("DELETE /voice-profiles/{user_id}/samples/{sample_index}", m.app(m.handleDeleteSample))
	mux.HandleFunc("DELETE /voice-profiles/user/{user_id}", m.app(m.handlePurge))
	mux.HandleFunc("DELETE /voice-profiles/{user_id}", m.app(m.handleDeleteProfile))
}

// Start migrates the settings table. The speaker model loads on first use; the module
// releases it when ctx ends.
func (m *Module) Start(ctx context.Context) error {
	if err := m.settings.Migrate(ctx); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		m.closeModels()
	}()
	return nil
}

func (m *Module) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var encoder any
	if id := m.activeModelID(ctx); id != "" {
		encoder = id
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "healthy",
		"version": m.Version,
		"speaker": map[string]any{
			// System scope; a household may override it (D35).
			"recognition_enabled": m.settings.Bool(ctx, "voice.recognition_enabled", settings.Scope{}),
			"encoder":             encoder,
		},
	})
}

// app is the legacy jarvis_auth_client require_app_auth: both headers or 401 "Missing app
// credentials", then a check with the auth module (401 "Invalid app credentials").
func (m *Module) app(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key, ok := authn.AppCreds(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "Missing app credentials")
			return
		}
		a, valid, err := m.Auth.ValidateApp(r.Context(), id, key)
		if err != nil {
			m.deps.Log.Error("stt: app validation failed", "err", err)
			httpx.Error(w, http.StatusBadGateway, "Auth service unavailable: "+err.Error())
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
