package cc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Each LLM mode setting resolves in order: the superuser's setting, the prompt's value, the
// live model's template (or a remote label), else the default.
func TestLLMMode(t *testing.T) {
	local := func(tc string, thinking bool) *llm.ModelTraits {
		return &llm.ModelTraits{ToolCalling: tc, ToolCallingFrom: "template", Thinking: thinking, Local: true}
	}
	for _, c := range []struct {
		name                       string
		toolSetting, thinkSetting  string // "" = unset (auto)
		prefs                      prompts.ModePrefs
		traits                     *llm.ModelTraits // nil = no LiveModelTraits
		tool, toolSource, detected string
		think, thinkSource         string
		force                      bool
	}{
		{name: "template native", traits: local("native", false),
			tool: "native", toolSource: "template", detected: "native", think: "none", thinkSource: "default", force: true},
		{name: "template text", traits: local("text", false),
			tool: "text", toolSource: "template", detected: "text", think: "none", thinkSource: "default", force: true},
		{name: "prompt over template", prefs: prompts.ModePrefs{ToolCalling: "text"}, traits: local("native", false),
			tool: "text", toolSource: "prompt", detected: "native", think: "none", thinkSource: "default", force: true},
		{name: "setting over prompt", toolSetting: "native", prefs: prompts.ModePrefs{ToolCalling: "text"}, traits: local("text", false),
			tool: "native", toolSource: "setting", detected: "text", think: "none", thinkSource: "default", force: true},
		{name: "explicit auto", toolSetting: "auto", thinkSetting: "auto", traits: local("native", true),
			tool: "native", toolSource: "template", detected: "native", think: "suffix", thinkSource: "template", force: true},
		{name: "remote", traits: &llm.ModelTraits{ToolCalling: "native", ToolCallingFrom: "remote"},
			tool: "native", toolSource: "remote", detected: "native", think: "none", thinkSource: "default", force: false},
		{name: "no traits", tool: "text", toolSource: "default", think: "none", thinkSource: "default", force: true},
		{name: "unknown template", traits: &llm.ModelTraits{Local: true},
			tool: "text", toolSource: "default", think: "none", thinkSource: "default", force: true},
		{name: "thinking from template", traits: local("text", true),
			tool: "text", toolSource: "template", detected: "text", think: "suffix", thinkSource: "template", force: true},
		{name: "prompt none over template", prefs: prompts.ModePrefs{ThinkingSwitch: "none"}, traits: local("text", true),
			tool: "text", toolSource: "template", detected: "text", think: "none", thinkSource: "prompt", force: true},
		{name: "prompt suffix", prefs: prompts.ModePrefs{ThinkingSwitch: "suffix"}, traits: local("text", false),
			tool: "text", toolSource: "template", detected: "text", think: "suffix", thinkSource: "prompt", force: true},
		{name: "setting suffix over prompt none", thinkSetting: "suffix", prefs: prompts.ModePrefs{ThinkingSwitch: "none"}, traits: local("text", false),
			tool: "text", toolSource: "template", detected: "text", think: "suffix", thinkSource: "setting", force: true},
		{name: "setting none over template", thinkSetting: "none", traits: local("text", true),
			tool: "text", toolSource: "template", detected: "text", think: "none", thinkSource: "setting", force: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, envOpts{configure: func(m *Module) {
				if c.traits != nil {
					tr := *c.traits
					m.LiveModelTraits = func(context.Context) llm.ModelTraits { return tr }
				}
			}})
			ctx := context.Background()
			for key, v := range map[string]string{settingToolCalling: c.toolSetting, settingThinkingSwitch: c.thinkSetting} {
				if v != "" {
					if err := e.m.settings.Set(ctx, key, v, settings.Scope{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			mode, tc, th := e.m.llmMode(ctx, c.prefs)
			if string(mode.ToolCalling) != c.tool || tc.Effective != c.tool || tc.Source != c.toolSource || tc.Detected != c.detected {
				t.Errorf("tool calling: mode %q, %+v; want %q from %q (detected %q)", mode.ToolCalling, tc, c.tool, c.toolSource, c.detected)
			}
			wantValue := c.toolSetting
			if wantValue == "" {
				wantValue = "auto"
			}
			if tc.Value != wantValue {
				t.Errorf("tool calling value %q, want %q", tc.Value, wantValue)
			}
			if mode.ThinkingSuffix != (c.think == "suffix") || th.Effective != c.think || th.Source != c.thinkSource {
				t.Errorf("thinking: mode %v, %+v; want %q from %q", mode.ThinkingSuffix, th, c.think, c.thinkSource)
			}
			if mode.ForceTools != c.force {
				t.Errorf("force tools %v, want %v", mode.ForceTools, c.force)
			}
		})
	}
}

// Thinking's Detected is the template's switch: suffix, none for a known model without one,
// "" when the model is unknown.
func TestLLMModeThinkingDetected(t *testing.T) {
	for _, c := range []struct {
		traits llm.ModelTraits
		want   string
	}{
		{llm.ModelTraits{ToolCalling: "text", ToolCallingFrom: "template", Thinking: true, Local: true}, "suffix"},
		{llm.ModelTraits{ToolCalling: "native", ToolCallingFrom: "remote"}, "none"},
		{llm.ModelTraits{Local: true}, ""},
	} {
		e := newEnv(t, envOpts{configure: func(m *Module) {
			m.LiveModelTraits = func(context.Context) llm.ModelTraits { return c.traits }
		}})
		if _, _, th := e.m.llmMode(context.Background(), prompts.ModePrefs{}); th.Detected != c.want {
			t.Errorf("%+v: detected %q, want %q", c.traits, th.Detected, c.want)
		}
	}
}

// llm.force_tools: auto keeps the force-tools guard to a local model (and to no traits at all);
// on and off choose it, e.g. the sandbox's remote tap to the installed jarvisd's local model.
func TestLLMModeForceTools(t *testing.T) {
	remote := llm.ModelTraits{ToolCalling: "native", ToolCallingFrom: "remote"}
	local := llm.ModelTraits{ToolCalling: "native", ToolCallingFrom: "template", Local: true}
	for _, c := range []struct {
		setting string // "" = unset
		traits  llm.ModelTraits
		want    bool
	}{
		{"", remote, false},
		{"", local, true},
		{"auto", remote, false},
		{"on", remote, true},
		{"off", local, false},
		{"on", local, true},
	} {
		e := newEnv(t, envOpts{configure: func(m *Module) {
			m.LiveModelTraits = func(context.Context) llm.ModelTraits { return c.traits }
		}})
		ctx := context.Background()
		if c.setting != "" {
			if err := e.m.settings.Set(ctx, settingForceTools, c.setting, settings.Scope{}); err != nil {
				t.Fatal(err)
			}
		}
		if mode, _, _ := e.m.llmMode(ctx, prompts.ModePrefs{}); mode.ForceTools != c.want {
			t.Errorf("%q, local=%v: force tools %v, want %v", c.setting, c.traits.Local, mode.ForceTools, c.want)
		}
	}
}

// A setting write outside the options is rejected; the options are accepted.
func TestLLMModeSettingsValidate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for key, ok := range map[string][]string{
		settingToolCalling:    {"auto", "native", "text"},
		settingThinkingSwitch: {"auto", "suffix", "none"},
		settingForceTools:     {"auto", "on", "off"},
	} {
		for _, v := range ok {
			if err := e.m.settings.Set(ctx, key, v, settings.Scope{}); err != nil {
				t.Errorf("%s=%s: %v", key, v, err)
			}
		}
		for _, v := range []string{"bogus", "", "Native"} {
			if err := e.m.settings.Set(ctx, key, v, settings.Scope{}); !errors.Is(err, settings.ErrInvalidValue) {
				t.Errorf("%s=%q: %v, want ErrInvalidValue", key, v, err)
			}
		}
	}
}

// The admin's prompt-provider status carries the install's LLM mode, with lower-case JSON.
func TestPromptProviderStatusLLMMode(t *testing.T) {
	e := newEnv(t, envOpts{configure: func(m *Module) {
		m.LiveModelTraits = func(context.Context) llm.ModelTraits {
			return llm.ModelTraits{ToolCalling: "native", ToolCallingFrom: "template", Thinking: true, Local: true}
		}
	}})
	ctx := context.Background()
	if err := e.m.settings.Set(ctx, settingThinkingSwitch, "none", settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(e.m.PromptProvider(ctx))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(raw, &got)
	want := map[string]map[string]any{
		"tool_calling":    {"value": "auto", "detected": "native", "effective": "native", "source": "template"},
		"thinking_switch": {"value": "none", "detected": "suffix", "effective": "none", "source": "setting"},
	}
	for k, w := range want {
		obj, _ := got[k].(map[string]any)
		if len(obj) != len(w) {
			t.Fatalf("%s: %v", k, got[k])
		}
		for f, v := range w {
			if obj[f] != v {
				t.Errorf("%s.%s = %v, want %v", k, f, obj[f], v)
			}
		}
	}
}
