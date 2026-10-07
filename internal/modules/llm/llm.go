// Package llm is the jarvisd llm module: the port of jarvis-llm-proxy-api's request path
// (docs/llm/00-05), on the llm listener (legacy port 7704).
//
// In-process callers (CC, OCR, phone) use Service: Chat, Stream (+ Cancel), Embed, Enqueue
// (+ OnComplete). The HTTP API keeps the frozen legacy contract (contract/llm_test.go):
//   - POST /v1/chat/completions (non-stream, and stream:true with Jarvis frames {"delta"},
//     {"done":true,...}, {"error"}, {"cancelled":true}; no [DONE]), POST
//     /v1/chat/completions/cancel/{request_id}, POST /v1/embeddings: app credentials
//   - GET /v1/models, /v1/engine, /v1/adapters/date-keys, /health, /v1/health: no auth
//   - POST /internal/queue/enqueue: app credentials; jobs run on the durable queue and call back
//   - /settings: the platform settings router
//
// Engines (llama-server supervision, remotes, GPUs, the model manager) are behind Resolver;
// this package only speaks OpenAI-compatible HTTP to whatever a label resolves to.
//
// Deliberate differences from legacy (D8, docs/llm/00 §8): temperature 0 is honoured; the
// stream applies thinking control, tools, the JSON instruction and the image check like
// non-stream does, answers a real 503/400 before it opens, and always reports usage; native
// tool history is forwarded; the engine's finish_reason passes through; date_keys is [] (not
// null) for an empty user text; an expired queue job calls back; queued jobs and callbacks are
// retried; message content is never logged.
package llm

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/models"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
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
const ServiceName = "jarvis-llm-proxy-api"

// Definitions are the settings the request path reads (05 §5.2). Engine settings (model,
// context, GPUs, remote URLs, …) belong to the engine manager and join through
// Module.ExtraDefinitions, so the module keeps one settings table.
var Definitions = []settings.Definition{
	{Key: "llm.live.reasoning_budget", Category: "llm.live", Type: settings.String, Default: "0",
		Description: "Default thinking budget for the live label: 0 off, -1 unrestricted, N on; \"server\" leaves the engine's launch flags in charge"},
	{Key: "llm.background.reasoning_budget", Category: "llm.background", Type: settings.String, Default: "-1",
		Description: "Default thinking budget for the background label (LD8: the user's choice): 0 off, -1 unrestricted, N on; \"server\" leaves the engine's launch flags in charge"},
	{Key: "llm.request_timeout_seconds", Category: "llm", Type: settings.Int, Default: int64(240),
		Description: "Upper bound for one model call (thinking background jobs need minutes)"},
}

// Module is the llm module.
type Module struct {
	// Auth validates app-to-app callers.
	Auth authn.Authority
	// SettingsRead/SettingsWrite guard /settings (app credentials or a superuser may read).
	SettingsRead, SettingsWrite settings.Guard
	// Version is what /health reports.
	Version string
	// Resolver maps labels to engines.
	Resolver Resolver
	// AppID/AppKey are jarvisd's own app credentials, sent on queue callbacks.
	AppID, AppKey string
	// BackgroundParallel caps concurrent background calls and queued jobs (default 1).
	BackgroundParallel int
	// CallbackAttempts bounds callback retries (default 12, exponential backoff up to 5 min).
	CallbackAttempts int
	// HTTPClient is used for engine calls and callbacks; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// ExtraDefinitions are more llm_* settings (the engine manager's); a key already in
	// Definitions is skipped.
	ExtraDefinitions []settings.Definition
	// ManagerGuard protects the model-manager API (/v1/hardware, /v1/models/*): superuser.
	// Used only with the built-in engine stack (Resolver left nil).
	ManagerGuard settings.Guard

	stack *models.Stack
	mux   *http.ServeMux

	deps     module.Deps
	settings *settings.Service
	// chatBackoff overrides the llm.chat retry delay (tests).
	chatBackoff func(int) time.Duration
	svcOnce     sync.Once
	svc         *Service

	hmu           sync.Mutex
	notReadySince time.Time
}

func (m *Module) Name() string      { return "llm" }
func (m *Module) Listener() string  { return pconfig.ListenerLLM }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Settings is the module's settings service (valid after Register), for the admin aggregator.
func (m *Module) Settings() *settings.Service { return m.settings }

// Service is the in-process API. It exists before Register (so callers can wire OnComplete
// handlers early) and works once the module is registered.
func (m *Module) Service() *Service {
	m.svcOnce.Do(func() { m.svc = NewService(ServiceConfig{}) })
	return m.svc
}

func (m *Module) log() *slog.Logger {
	if m.deps.Log != nil {
		return m.deps.Log
	}
	return slog.Default()
}

func (m *Module) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

func (m *Module) definitions() []settings.Definition {
	defs := append([]settings.Definition{}, Definitions...)
	seen := map[string]bool{}
	for _, d := range defs {
		seen[d.Key] = true
	}
	extra := m.ExtraDefinitions
	if m.useStack() {
		extra = append(append([]settings.Definition{}, extra...), models.SettingDefinitions()...)
	}
	for _, d := range extra {
		if !seen[d.Key] {
			seen[d.Key] = true
			defs = append(defs, d)
		}
	}
	return defs
}

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.Version == "" {
		m.Version = "dev"
	}
	svc, err := settings.New(deps.DB, "llm", m.definitions(), deps.Log)
	if err != nil {
		panic(err) // static definitions
	}
	m.settings = svc
	m.mux = mux
	if m.Resolver == nil {
		m.attachStack()
	}
	if m.SettingsRead != nil && m.SettingsWrite != nil {
		svc.Mount(mux, m.SettingsRead, m.SettingsWrite)
	}
	s := m.Service()
	s.configure(ServiceConfig{Resolver: m.Resolver, HTTPClient: m.HTTPClient, Settings: svc, Log: deps.Log,
		BackgroundParallel: m.BackgroundParallel, DB: deps.DB, Queue: deps.Queue})

	mux.HandleFunc("POST /v1/chat/completions", m.app(m.handleChat))
	mux.HandleFunc("POST /v1/chat/completions/cancel/{request_id}", m.app(m.handleCancel))
	mux.HandleFunc("POST /v1/embeddings", m.app(m.handleEmbeddings))
	mux.HandleFunc("GET /v1/models", m.handleModels)
	mux.HandleFunc("GET /v1/engine", m.handleEngine)
	mux.HandleFunc("GET /v1/adapters/date-keys", m.handleDateKeys)
	mux.HandleFunc("POST /internal/queue/enqueue", m.app(m.handleEnqueue))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { m.handleHealth(w, r, true) })
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { m.handleHealth(w, r, false) })

	if deps.Queue != nil {
		conc := m.BackgroundParallel
		if conc <= 0 {
			conc = 1
		}
		backoff := m.chatBackoff
		if backoff == nil {
			backoff = func(n int) time.Duration {
				if n <= 1 {
					return 10 * time.Second
				}
				return 30 * time.Second
			}
		}
		deps.Queue.Register(jobChat, queue.Handler{Run: s.runChat, Concurrency: conc, MaxAttempts: chatAttempts,
			Lease: 30 * time.Minute, Backoff: backoff})
		attempts := m.CallbackAttempts
		if attempts <= 0 {
			attempts = 12
		}
		deps.Queue.Register(jobCallback, queue.Handler{Run: m.runCallback, Concurrency: 2, MaxAttempts: attempts, Lease: time.Minute})
		deps.Queue.Register(jobNotify, queue.Handler{Run: s.runNotify, Concurrency: 2, MaxAttempts: 5, Lease: 5 * time.Minute})
		deps.Queue.Register(jobPurge, queue.Handler{Run: s.runPurge})
	}
}

// Start migrates the settings table and schedules the hourly dedupe purge.
func (m *Module) Start(ctx context.Context) error {
	if err := m.settings.Migrate(ctx); err != nil {
		return err
	}
	if m.stack != nil {
		if err := m.stack.Start(ctx); err != nil {
			return err
		}
	}
	if m.deps.Scheduler == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: jobPurge, Kind: scheduler.KindInterval, JobType: jobPurge,
		Spec: scheduler.Spec{Every: time.Hour, StartNow: true},
	})
}
