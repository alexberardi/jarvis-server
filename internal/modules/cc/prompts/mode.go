package prompts

import (
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// The chain-of-thought markers streaming code strips (every model the built-in prompts serve).
const (
	ThinkOpen  = thinkOpen
	ThinkClose = thinkClose
)

// Mode is how a turn talks to the live model, resolved from the llm.tool_calling and
// llm.thinking_switch settings, the prompt's values and the model's chat template.
type Mode struct {
	// ToolCalling sends the tools natively (the API's tools payload) or as text in the prompt.
	ToolCalling ToolCalling
	// ThinkingSuffix appends /think or /no_think to every user message.
	ThinkingSuffix bool
	// ForceTools arms the engine's force-tools guard (local models).
	ForceTools bool
}

// ModePrefs are a prompt's own values: ToolCalling "", "native" or "text"; ThinkingSwitch
// "", "suffix" or "none". "" means no preference (the model's template decides).
type ModePrefs struct {
	ToolCalling, ThinkingSwitch string
}

// Native says the tools go through the API's tools payload.
func (m Mode) Native() bool { return m.ToolCalling == ToolsNative }

// UserMessageSuffix is appended to every user message after a single "\n" ("" = none).
func (m Mode) UserMessageSuffix(includeThinking bool) string {
	if !m.ThinkingSuffix {
		return ""
	}
	if includeThinking {
		return suffixThink
	}
	return suffixNo
}

// ResponseFormat is the response_format to send: {"type":"text"} in text mode, nil (Python's
// None) when native.
func (m Mode) ResponseFormat() *pyjson.Object {
	if m.Native() {
		return nil
	}
	o := pyjson.NewObject()
	o.Set("type", "text")
	return o
}

// ParseResponse turns raw model output into Jarvis JSON for ParseToolCalls; ok=false is
// Python's None (feed the raw content through unchanged).
func ParseResponse(raw string) (string, bool) { return parse.ParseQwen3(raw, true) }

// SanitizeText cleans any user-facing text before TTS.
func SanitizeText(text string) string { return parse.SanitizeQwen3(text, true) }

// NativeTools is the native path's tools payload. Warmup and inference must both send exactly
// this for the prefix cache to hit (03 §7.3).
func NativeTools(tools []Tool) []Tool { return buildTools(StripJarvisExtensions(tools), nativeBuild) }
