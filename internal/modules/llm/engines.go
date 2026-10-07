package llm

import (
	"context"
	"errors"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/models"
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
