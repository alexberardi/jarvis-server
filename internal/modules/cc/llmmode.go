package cc

import (
	"context"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The LLM mode (docs/cc/prompt-contract.md): llm.tool_calling and llm.thinking_switch, each
// resolved in order from the superuser's install-wide setting, the prompt's own value, the
// live model's chat template, else a default.

// The settings' values besides prompts.ToolsNative / prompts.ToolsText.
const (
	modeAuto       = "auto"
	thinkingSuffix = "suffix"
	thinkingNone   = "none"
)

// Mode sources (ModeSetting.Source).
const (
	ModeSourceSetting  = "setting"
	ModeSourcePrompt   = "prompt"
	ModeSourceTemplate = "template"
	ModeSourceRemote   = "remote"
	ModeSourceDefault  = "default"
)

// ModeSetting is what the admin shows for one LLM mode setting.
type ModeSetting struct {
	// Value is the setting as set ("auto" unless the superuser chose one).
	Value string `json:"value"`
	// Detected is the live model's own value ("" when unknown).
	Detected string `json:"detected"`
	// Effective is what a turn uses.
	Effective string `json:"effective"`
	// Source says where Effective comes from: setting, prompt, template, remote or default.
	Source string `json:"source"`
}

// llmMode resolves the mode a turn talks to the live model in. prefs are the prompt's values.
func (m *Module) llmMode(ctx context.Context, prefs prompts.ModePrefs) (prompts.Mode, ModeSetting, ModeSetting) {
	var traits llm.ModelTraits
	if m.LiveModelTraits != nil {
		traits = m.LiveModelTraits(ctx)
	}
	tc := ModeSetting{Value: m.modeValue(ctx, settingToolCalling), Detected: traits.ToolCalling}
	switch {
	case tc.Value != modeAuto:
		tc.Effective, tc.Source = tc.Value, ModeSourceSetting
	case prefs.ToolCalling != "":
		tc.Effective, tc.Source = prefs.ToolCalling, ModeSourcePrompt
	case traits.ToolCalling != "":
		tc.Effective, tc.Source = traits.ToolCalling, traits.ToolCallingFrom
	default:
		tc.Effective, tc.Source = string(prompts.ToolsText), ModeSourceDefault
	}
	th := ModeSetting{Value: m.modeValue(ctx, settingThinkingSwitch)}
	switch {
	case traits.Thinking:
		th.Detected = thinkingSuffix
	case traits.ToolCalling != "": // a known model (remote, or a template) without the switch
		th.Detected = thinkingNone
	}
	switch {
	case th.Value != modeAuto:
		th.Effective, th.Source = th.Value, ModeSourceSetting
	case prefs.ThinkingSwitch != "":
		th.Effective, th.Source = prefs.ThinkingSwitch, ModeSourcePrompt
	case traits.Thinking:
		th.Effective, th.Source = thinkingSuffix, ModeSourceTemplate
	default:
		th.Effective, th.Source = thinkingNone, ModeSourceDefault
	}
	mode := prompts.Mode{
		ToolCalling:    prompts.ToolCalling(tc.Effective),
		ThinkingSuffix: th.Effective == thinkingSuffix,
		ForceTools:     traits.Local || m.LiveModelTraits == nil,
	}
	return mode, tc, th
}

// modeValue reads an LLM mode setting; unset or blank is auto.
func (m *Module) modeValue(ctx context.Context, key string) string {
	if m.settings == nil {
		return modeAuto
	}
	if v := strings.TrimSpace(m.settings.String(ctx, key, settings.Scope{})); v != "" {
		return v
	}
	return modeAuto
}

// oneOf validates a String setting against its options.
func oneOf(options ...string) func(any) error {
	return func(v any) error {
		s, _ := v.(string)
		for _, o := range options {
			if s == o {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(options, ", "))
	}
}
