// Package phone is command-center's phone subsystem (docs/cc/11-phone.md), with the
// jarvis-phone-gateway absorbed into jarvisd (D16): AI outbound calls to businesses, the
// household phonebook, per-user call context, the web number lookup, the make_phone_call
// server tool, the confirm/cancel/escalation card callbacks, the reaper, and the live call
// loop (telephony provider + Media Streams WebSocket + in-process STT, LLM and TTS).
//
// Everything is gated behind phone_calls.enabled (household, default off, fail closed). The
// telephony vendor sits behind Provider so tests run against a fake; nothing here talks to a
// real provider unless main wires the Twilio client with real credentials.
//
// The /internal/phone/* routes are not ported (D48): nothing outside jarvisd called them, and
// the call loop now talks to session state in process.
package phone

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- dependencies (all in process; nil disables what needs them) ---

// LLM is the llm module (the brief draft, the live call model, the guard classifier and the
// wrap-up assessment).
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
	Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Frame, error)
}

// STT transcribes caller audio (no speaker pass).
type STT interface {
	Transcribe(ctx context.Context, wav []byte, opts stt.TranscribeOptions) (stt.Result, error)
}

// TTS synthesizes one text to PCM16 mono at SampleRate.
type TTS interface {
	SynthesizePCM(ctx context.Context, text string) (pcm []int16, sampleRate int, err error)
}

// Notifier is the notifications module (D31): inbox cards and pushes.
type Notifier interface {
	CreateInboxItem(ctx context.Context, tx *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error)
	Notify(ctx context.Context, tx *sql.Tx, source string, n notifications.Notification) (notifications.Delivery, error)
}

// NameResolver resolves a user's display name (the disclosure's "on behalf of {name}").
type NameResolver interface {
	UserNames(ctx context.Context, ids []int64) (map[int64]string, error)
}

// Roles answers household membership (verify_household_role).
type Roles interface {
	HouseholdRole(ctx context.Context, userID int64, householdID string) (authn.Role, bool, error)
}

// Availability is the node calendar context provider (doc 05 context_provider_client,
// query_context(hh, "availability", {start, end}, user_id)). Nil until that client exists:
// a scheduling brief then keeps its fill-in placeholder.
type Availability interface {
	Availability(ctx context.Context, householdID string, userID *int64, start, end time.Time) (AvailabilityAnswer, error)
}

// AvailabilityAnswer is a context-provider answer: Ok with Free/Busy windows, or an error.
type AvailabilityAnswer struct {
	OK    bool
	Error string
	Free  []string
	Busy  []string
}

// ErrandHook is the 09 workflow engine's resume entry point (deliver_signal(run, step,
// "phone_call", session)). Nil until errands land; their 20 s sweep remains the safety net.
type ErrandHook interface {
	CallTerminal(ctx context.Context, snap CallSnapshot)
}

// CallSnapshot is what an errand resume reads off a terminal session.
type CallSnapshot struct {
	SessionID    string
	ErrandID     string
	ErrandStep   *int64
	State        string
	ContactName  string
	ErrorMessage string
	HouseholdID  string
	UserID       *int64
	OutcomeJSON  string
	// ConfirmedAt is when the user confirmed the call card (zero while it is a draft); the
	// errand's phone deadline runs from it (D40 09.Q6).
	ConfirmedAt time.Time
}

// Options are the bootstrap/secret values (env, never the settings DB: the telephony
// credentials stay out of the settings table, as they stayed in the gateway's env).
type Options struct {
	// PublicURL is the public https base a tunnel maps to the CC listener (Twilio reaches the
	// media WebSocket through it). PublicWSSURL overrides the derived wss base.
	PublicURL    string
	PublicWSSURL string
	// AuthToken is the provider's webhook signing key (Twilio auth token). Empty rejects every
	// media WebSocket: without it nothing can prove a stream came from the provider.
	AuthToken string
}

// Service is the phone subsystem.
type Service struct {
	DB       *db.DB
	Log      *slog.Logger
	Settings *settings.Service
	Roles    Roles
	LLM      LLM
	STT      STT
	TTS      TTS
	Notify   Notifier
	Names    NameResolver
	Blobs    blob.Store
	// Provider places and ends calls; nil means no telephony is configured (a confirmed call
	// fails with an honest card).
	Provider Provider
	// Search finds a business's number on the web (gated on web_search.enabled).
	Search   servertools.WebSearcher
	Fetch    *servertools.Fetcher
	Calendar Availability
	Errands  ErrandHook
	Options  Options
	Now      func() time.Time
	// Timing knobs (tests shorten them; zero = the legacy values).
	StreamStartTimeout time.Duration
	HeartbeatInterval  time.Duration
	EscalationWindow   time.Duration
	TurnTimeout        time.Duration

	base     context.Context
	mu       sync.Mutex
	runtimes map[string]*callRuntime
	tokens   *live.TokenRegistry
	wg       sync.WaitGroup
	planWG   sync.WaitGroup
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) init() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimes == nil {
		s.runtimes = map[string]*callRuntime{}
	}
	if s.tokens == nil {
		s.tokens = &live.TokenRegistry{}
	}
	if s.base == nil {
		s.base = context.Background()
	}
}

// Start binds live calls and background plans to ctx (shutdown cancels them).
func (s *Service) Start(ctx context.Context) {
	s.init()
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
}

// Wait blocks until background plans and live calls have finished (tests, shutdown).
func (s *Service) Wait() {
	s.planWG.Wait()
	s.wg.Wait()
}

func (s *Service) baseCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.base == nil {
		return context.Background()
	}
	return s.base
}

// UserHandler is a route handler for an authenticated user (cc's verify_user_jwt wrapper).
type UserHandler func(w http.ResponseWriter, r *http.Request, u authn.User)

// Mount registers the mobile routes (wrapped by user auth) and the provider's media
// WebSocket on mux.
func (s *Service) Mount(mux *http.ServeMux, user func(UserHandler) http.HandlerFunc) {
	s.init()
	const mob = "/api/v0/mobile"
	mux.HandleFunc("GET "+mob+"/household/{household_id}/phone-contacts", user(s.handleListContacts))
	mux.HandleFunc("POST "+mob+"/household/{household_id}/phone-contacts", user(s.handleCreateContact))
	mux.HandleFunc("PATCH "+mob+"/household/{household_id}/phone-contacts/{contact_id}", user(s.handleUpdateContact))
	mux.HandleFunc("DELETE "+mob+"/household/{household_id}/phone-contacts/{contact_id}", user(s.handleDeleteContact))
	mux.HandleFunc("GET "+mob+"/call-context", user(s.handleGetCallContext))
	mux.HandleFunc("PUT "+mob+"/call-context", user(s.handlePutCallContext))
	mux.HandleFunc("GET "+MediaPath+"{token}", s.handleMediaWS)
}
