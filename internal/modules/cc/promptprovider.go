package cc

import (
	"context"
	"errors"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The effective prompt provider (admin AD4, derive + override): llm.prompt_provider when set
// ("set by you"), else the live model's own (DefaultPromptProvider, "from model").

// Prompt-provider sources.
const (
	PromptSourceSetting = "setting"
	PromptSourceModel   = "model"
)

// ErrUnknownPromptProvider rejects an override that names no registered provider.
var ErrUnknownPromptProvider = errors.New("unknown prompt provider")

// PromptProviderStatus is what the admin shows for the prompt provider.
type PromptProviderStatus struct {
	// Value is llm.prompt_provider as set; "" means derive it from the live model.
	Value string `json:"value"`
	// Derived is the live model's provider ("" when there is no live model or it names none).
	Derived string `json:"derived"`
	// Effective is what a voice turn uses: Value when set, else Derived.
	Effective string `json:"effective"`
	// Source is "setting", "model", or "" when nothing names one.
	Source string `json:"source"`
	// Valid says Effective is a registered provider; a voice turn fails otherwise (D11).
	Valid bool `json:"valid"`
	// Options are the providers an override may name. The admin offers the pick-list when
	// Derived is "" (a hand-registered model that declares none).
	Options []string `json:"options"`
}

// PromptProvider reports the effective prompt provider and where it comes from.
func (m *Module) PromptProvider(ctx context.Context) PromptProviderStatus {
	st := PromptProviderStatus{Options: prompts.Names()}
	if m.settings != nil {
		st.Value = strings.TrimSpace(m.settings.String(ctx, settingPromptProvider, settings.Scope{}))
	}
	if m.DefaultPromptProvider != nil {
		st.Derived = m.DefaultPromptProvider(ctx)
	}
	switch {
	case st.Value != "":
		st.Effective, st.Source = st.Value, PromptSourceSetting
	case st.Derived != "":
		st.Effective, st.Source = st.Derived, PromptSourceModel
	}
	if st.Effective != "" {
		_, err := prompts.Lookup(st.Effective)
		st.Valid = err == nil
	}
	return st
}

// SetPromptProvider overrides the prompt provider; "" clears the override so the live
// model's provider applies again. An unknown name is ErrUnknownPromptProvider.
func (m *Module) SetPromptProvider(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name != "" {
		if _, err := prompts.Lookup(name); err != nil {
			return ErrUnknownPromptProvider
		}
	}
	if m.settings == nil {
		return errors.New("cc: settings not ready")
	}
	return m.settings.Set(ctx, settingPromptProvider, name, settings.Scope{})
}
