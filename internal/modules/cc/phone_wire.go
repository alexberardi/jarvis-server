package cc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// Wiring for the phone subsystem (docs/cc/11-phone.md, package cc/phone), with the phone
// gateway absorbed (D16). Everything is off until a household turns phone_calls.enabled on;
// placing real calls additionally needs Twilio credentials (the household's phone.twilio_*
// settings, the system default, or TWILIO_* in the environment; AD6) and a public URL for the
// media WebSocket (a tunnel that exposes phone.MediaPath on the CC listener).

// PhoneConfig configures phone calls. The zero value serves the phonebook, call context and
// the tool's refusals, and places calls for households whose credentials are in settings.
type PhoneConfig struct {
	// Provider, when set, is the one provider every household uses, signed with
	// Options.AuthToken (a fake in tests). Nil resolves a Twilio client per call from the
	// household's credentials.
	Provider phone.Provider
	// EnvCredentials are TWILIO_ACCOUNT_SID / TWILIO_AUTH_TOKEN / TWILIO_FROM_NUMBER, the last
	// fallback after household and system settings.
	EnvCredentials phone.Credentials
	// NewProvider builds the provider from resolved credentials (nil: phone.Twilio).
	NewProvider func(phone.Credentials) phone.Provider
	// Options carry the public URL (env) and the single-provider path's signing key.
	Options phone.Options
	// Calendar is the node availability provider (doc 05); nil keeps the fill-in placeholder.
	Calendar phone.Availability
	// Errands is the 09 workflow engine's resume hook; nil until errands land.
	Errands phone.ErrandHook
}

const phoneReaperJob = "cc.phone_reaper"

// phoneService returns the (possibly not yet configured) phone service, so the server tool
// can be registered with the other server tools before registerPhone fills it in.
func (m *Module) phoneService() *phone.Service {
	if m.phone == nil {
		m.phone = &phone.Service{}
	}
	return m.phone
}

// registerPhone configures the phone service and mounts its routes. (Its make_phone_call
// tool is registered by registerServerTools, always offered per invariant 12.)
func (m *Module) registerPhone(mux *http.ServeMux) {
	search := m.WebSearch
	if search == nil {
		search = &servertools.DuckDuckGo{}
	}
	p := m.phoneService()
	p.DB, p.Log, p.Settings, p.Roles = m.deps.DB, m.deps.Log, m.settings, m.Auth
	p.LLM, p.STT, p.Notify, p.Names, p.Blobs = m.LLM, m.STT, m.Notify, m.Names, m.deps.Blobs
	p.Provider, p.Search, p.Calendar, p.Errands = m.Phone.Provider, search, m.Phone.Calendar, m.Phone.Errands
	p.EnvCredentials, p.NewProvider = m.Phone.EnvCredentials, m.Phone.NewProvider
	p.Options, p.Now = m.Phone.Options, m.now
	if m.TTS != nil {
		p.TTS = phoneTTS{m.TTS}
	}
	p.Mount(mux, func(h phone.UserHandler) http.HandlerFunc {
		return m.user(func(w http.ResponseWriter, r *http.Request, u authn.User) { h(w, r, u) })
	})
	if m.deps.Queue != nil {
		m.deps.Queue.Register(phoneReaperJob, queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) {
			p.Reap(ctx)
			return nil, nil
		}})
	}
}

// startPhone binds live calls to the module's lifetime and schedules the 30 s reaper (D27).
func (m *Module) startPhone(ctx context.Context) error {
	if m.phone == nil {
		return nil
	}
	m.phone.Start(ctx)
	if m.deps.Scheduler == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: phoneReaperJob, Kind: scheduler.KindInterval, JobType: phoneReaperJob,
		Spec: scheduler.Spec{Every: phone.ReaperInterval},
	})
}

// PhoneService is the phone subsystem (valid after Register): errands call CreatePlan, and
// doc 13's /callbacks dispatches the card taps through PhoneService().Callbacks().
func (m *Module) PhoneService() *phone.Service { return m.phone }

// phoneTTS adapts the voice pipeline's streaming TTS to whole-utterance PCM.
type phoneTTS struct{ t TTS }

func (a phoneTTS) SynthesizePCM(ctx context.Context, text string) ([]int16, int, error) {
	sp, err := a.t.Speak(ctx, text)
	if err != nil {
		return nil, 0, err
	}
	defer sp.Close()
	var buf []byte
	deadline := time.Now().Add(30 * time.Second)
	for {
		chunk, err := sp.Next()
		buf = append(buf, chunk...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, 0, errors.New("phone: TTS timed out")
		}
	}
	return live.PCMBytesToInt16(buf), a.t.AudioFormat().SampleRate, nil
}
