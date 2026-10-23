package llm

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
		FoldSystemMessages: ep.FoldSystemMessages, API: ep.API, NoReasoningBudget: ep.NoReasoningBudget,
		Timeout: time.Duration(ep.TimeoutMS) * time.Millisecond, Fallback: ep.Fallback,
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
		case engine.StateGPUUnavailable:
			return &NotReadyError{State: StateGPUUnavailable, Reason: nr.Reason}
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
	st.Resolver.SpeechFallback = m.SpeechFallback
	st.Manager.SpeechFallback = m.SpeechFallback
	st.Resolver.OnReady = func(labels []string) {
		chat := func(ctx context.Context, r ChatRequest) error {
			_, err := m.Service().Chat(ctx, r)
			return err
		}
		warmEngine(context.Background(), chat, labels, m.OnEngineReady, m.log())
	}
}

// ResolveCPU resolves a remote stt label to its local CPU fallback engine
// (engine.Resolver.ResolveCPU), for the stt module's fallback=cpu.
func (m *Module) ResolveCPU(ctx context.Context, label string) (Endpoint, error) {
	if m.stack == nil || m.stack.Resolver == nil {
		return Endpoint{}, &NotReadyError{State: StateNotConfigured, Reason: "no local engine stack"}
	}
	ep, err := m.stack.Resolver.ResolveCPU(ctx, label)
	if err != nil {
		// Said as is: it ends up in the remote error's "(local CPU fallback unavailable: …)".
		return Endpoint{}, err
	}
	return Endpoint{BaseURL: ep.BaseURL, Model: ep.Model}, nil
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
	return m.LiveModelTraits(ctx).PromptProvider
}

// ModelTraits is what cc needs to know about the live model to resolve its auto settings.
type ModelTraits struct {
	// PromptProvider: see LivePromptProvider.
	PromptProvider string
	// ToolCalling is "native" (the model calls tools through the API), "text" (its template
	// renders no tools) or "" (unknown: no template found); ToolCallingFrom says how it was
	// decided ("remote", "template").
	ToolCalling, ToolCallingFrom string
	// Thinking: the model's template has the enable_thinking switch.
	Thinking bool
	// Local: the local engine serves the live label.
	Local bool
}

// templateTraits caches the traits read from a local model's chat template, per model ID and
// path: a template never changes for an installed file.
var templateTraits sync.Map // id+"\x00"+path -> ModelTraits

// LiveModelTraits reports the live label's model traits. A remote label calls tools natively
// and has no thinking switch. A local model's come from the chat template llama-server
// renders with: the catalog entry's pinned one (--chat-template-file), else the one in the
// model's files (GGUF header, chat_template.jinja, tokenizer_config.json). The zero value
// when the live label has no model.
func (m *Module) LiveModelTraits(ctx context.Context) ModelTraits {
	if m.stack == nil || m.stack.Manager == nil || m.stack.Resolver == nil || m.settings == nil {
		return ModelTraits{}
	}
	// As LabelStates reports remote: engine=remote with a URL (without one it is not_configured).
	c, err := m.stack.Resolver.Source.Label(ctx, "live")
	if err != nil {
		return ModelTraits{}
	}
	switch c.Engine {
	case engine.ModeRemote:
		if c.RemoteURL == "" {
			return ModelTraits{}
		}
		return ModelTraits{ToolCalling: "native", ToolCallingFrom: "remote"}
	case engine.ModeOff:
		return ModelTraits{}
	}
	id := strings.TrimSpace(m.settings.String(ctx, "llm.live.model", settings.Scope{}))
	if id == "" {
		return ModelTraits{}
	}
	mod, err := m.stack.Manager.Store.Get(ctx, id)
	if err != nil {
		// A model set by path: the installed model there, else the file or folder itself.
		if !filepath.IsAbs(id) {
			return ModelTraits{}
		}
		if mod, err = m.stack.Manager.Store.ByPath(ctx, id); err != nil {
			mod = models.Model{ID: id, Kind: engine.ModelLLM, Path: id}
		}
	}
	if mod.PromptProvider == "" && mod.Kind == engine.ModelLLM {
		// A model directory names its provider in its config.json (catalog GGUFs in their entry).
		mod.PromptProvider = models.InferDir(mod.Path).PromptProvider
	}
	e, inCatalog := models.CatalogEntry(mod.CatalogID)
	key := mod.ID + "\x00" + mod.Path
	var tr ModelTraits
	if v, ok := templateTraits.Load(key); ok {
		tr = v.(ModelTraits)
	} else if tr, err = readTemplateTraits(e, inCatalog, mod); err != nil {
		m.deps.Log.Debug("llm: no chat template for the live model", "model", mod.ID, "path", mod.Path, "err", err)
	} else {
		templateTraits.Store(key, tr)
	}
	tr.PromptProvider = mod.PromptProvider
	if tr.PromptProvider == "" && inCatalog {
		tr.PromptProvider = e.PromptProvider
	}
	return tr
}

// readTemplateTraits reads a local model's tool calling and thinking from its chat template
// (on an error, the traits of a model without one).
func readTemplateTraits(e models.Entry, inCatalog bool, mod models.Model) (ModelTraits, error) {
	tr := ModelTraits{Local: true}
	var tpl string
	var err error
	if inCatalog && e.ChatTemplate != "" {
		tpl, err = e.Template()
	} else {
		tpl, err = models.ModelChatTemplate(mod.Path)
	}
	if err != nil || tpl == "" {
		return tr, err
	}
	tr.ToolCalling, tr.ToolCallingFrom = "text", "template"
	if models.TemplateSupportsTools(tpl) {
		tr.ToolCalling = "native"
	}
	tr.Thinking = models.TemplateHasThinking(tpl)
	return tr, nil
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

// SetupInstall is one model install as the admin setup summary reads it (AD3b).
type SetupInstall = models.Install

// RecentInstalls lists the latest model installs, newest first, for the admin's per-job setup
// summary: a label whose model is still downloading has no model yet, so its install is
// what says the job is under way. Nil without the local engine stack or on a read error.
func (m *Module) RecentInstalls(ctx context.Context) []SetupInstall {
	if m.stack == nil || m.stack.Manager == nil || m.stack.Manager.Store == nil {
		return nil
	}
	out, err := m.stack.Manager.Store.ListInstalls(ctx, 50)
	if err != nil {
		return nil
	}
	return out
}
