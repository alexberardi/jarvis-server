package llm

import (
	"context"
	"errors"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/models"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The local engine stack (internal/modules/llm/engine + models: GPU detection, llama-server and
// whisper-server supervision, the model manager) is the default Resolver. Tests and the parity
// harness set Module.Resolver to a fake instead and get no stack.

// stackResolver adapts the engine resolver to this package's Resolver.
type stackResolver struct{ r *engine.Resolver }

func (s stackResolver) Resolve(ctx context.Context, label string) (Endpoint, error) {
	ep, err := s.r.Resolve(ctx, label)
	if err != nil {
		return Endpoint{}, notReady(err)
	}
	per := ep.ContextLength
	if ep.Parallel > 1 {
		per = ep.ContextLength / ep.Parallel
	}
	return Endpoint{
		BaseURL: ep.BaseURL, APIKey: ep.APIKey, Model: ep.Model,
		Vision: ep.Vision, Embeddings: ep.Embeddings, ContextLength: per, Remote: ep.Remote,
		FoldSystemMessages: ep.FoldSystemMessages,
	}, nil
}

// notReady maps engine errors onto the 503 model_not_loaded states clients see.
func notReady(err error) error {
	if errors.Is(err, engine.ErrNotConfigured) {
		return &NotReadyError{State: StateNotConfigured, Reason: err.Error()}
	}
	var nr *engine.NotReadyError
	if errors.As(err, &nr) {
		switch nr.State {
		case "starting", "restarting", "draining", "fetching_engine":
			return &NotReadyError{State: StateLoading, Reason: nr.Error()}
		default: // failed, stopped, no_engine_build, misconfigured
			return &NotReadyError{State: StateFailed, Reason: nr.Error()}
		}
	}
	return err
}

// useStack reports whether Register builds the local engine stack (no Resolver injected).
func (m *Module) useStack() bool { return m.Resolver == nil || m.stack != nil }

// attachStack builds the engine stack over the module's settings, mounts the model-manager
// API, and makes its resolver the module's Resolver.
func (m *Module) attachStack() {
	st := models.NewStack(m.deps, m.settings)
	if m.ManagerGuard != nil {
		st.Mount(m.mux, m.ManagerGuard)
	}
	m.stack = st
	m.Resolver = stackResolver{st.Resolver}
}

// ModelPath reports where the model assigned to a voice label ("tts", "speaker") is
// installed, for the in-binary voice modules. ok is false until the stack is up and a model
// is installed and assigned.
func (m *Module) ModelPath(ctx context.Context, kind string) (string, bool) {
	if m.stack == nil || m.stack.Manager == nil {
		return "", false
	}
	vm, ok := m.stack.Manager.ModelPath(ctx, kind)
	return vm.Path, ok
}

// LivePromptProvider is the prompt provider of the model on the live label: the installed
// model's own (set from its catalog entry at install), else its catalog entry's. "" when the
// live label has no installed model or the model names none (a hand-registered file); CC
// then needs llm.prompt_provider set (D11). This is how a fresh install talks without anyone
// choosing a provider.
func (m *Module) LivePromptProvider(ctx context.Context) string {
	if m.stack == nil || m.stack.Manager == nil || m.settings == nil {
		return ""
	}
	id := strings.TrimSpace(m.settings.String(ctx, "llm.live.model", settings.Scope{}))
	if id == "" {
		return ""
	}
	mod, err := m.stack.Manager.Store.Get(ctx, id)
	if err != nil {
		return ""
	}
	if mod.PromptProvider != "" {
		return mod.PromptProvider
	}
	if e, ok := models.CatalogEntry(mod.CatalogID); ok {
		return e.PromptProvider
	}
	return ""
}

// LabelStates reports every label's state for the admin's setup state: the engine labels
// (live, background, embeddings, stt) as GET /v1/models/labels reports them (ready, degraded,
// remote, not_configured, a loading state, ...), and the in-binary voice labels (tts,
// speaker) as ready or not_configured. Nil without the local engine stack.
func (m *Module) LabelStates(ctx context.Context) map[string]string {
	if m.stack == nil || m.stack.Resolver == nil || m.stack.Manager == nil {
		return nil
	}
	out := map[string]string{}
	for _, ls := range m.stack.Resolver.Status(ctx) {
		out[ls.Label] = ls.State
	}
	for _, v := range m.stack.Manager.VoiceStatus(ctx) {
		switch v.Problem {
		case "":
			out[v.Label] = "ready"
		case "not configured":
			out[v.Label] = StateNotConfigured
		default:
			out[v.Label] = "error"
		}
	}
	return out
}

// SetupHardware is the summary the admin setup wizard's Hardware step starts from (AD3). The
// full view, with installed engine builds and running engines, is GET /v1/hardware.
type SetupHardware struct {
	Hardware engine.Hardware             `json:"hardware"`
	Proposal map[string]engine.Placement `json:"proposal"`
	// Flavours lists, per engine kind ("llama-server", "whisper-server"), the flavours this
	// platform has builds for: the only choices the step offers.
	Flavours map[string][]string `json:"flavours"`
}

// HardwareSummary returns the (cached) hardware detection with its proposal; ok is false
// without the local engine stack.
func (m *Module) HardwareSummary(ctx context.Context) (SetupHardware, bool) {
	if m.stack == nil || m.stack.Manager == nil {
		return SetupHardware{}, false
	}
	hw := engine.Hardware{Flavour: engine.FlavourCPU}
	if m.stack.Detector != nil {
		hw = m.stack.Detector.Hardware(ctx, false)
	}
	fl := map[string][]string{}
	for _, k := range engine.Kinds {
		f := []string{}
		if m.stack.Binaries != nil {
			f = append(f, engine.FlavoursFor(k, m.stack.Binaries.Platform)...)
		}
		fl[string(k)] = f
	}
	return SetupHardware{Hardware: hw, Proposal: engine.Propose(hw), Flavours: fl}, true
}
