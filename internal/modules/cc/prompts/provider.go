// Package prompts builds command-center's system prompts and owns each model family's
// quirks (docs/cc/03): the four kept providers behind one parameterised builder, the layer-2
// handler wrapper, the per-turn blocks, the tool transforms (ToolBuilder) and the per-household
// server-tool gates.
//
// The providers are ported byte-exact (D22): fixtures/golden/prompts (G1) is the gate. Rule
// text lives in the generated rules.go, copied from the legacy Python by internal/genrules.
// Provider selection is a static registry keyed by the llm.prompt_provider setting; an
// unknown name is a hard error (D11).
package prompts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Provider names (the llm.prompt_provider values).
const (
	Qwen3_14B   = "Qwen3_14B_Compressed"  // prod (serves the 27B)
	Qwen3_8B    = "Qwen3_8B_Compressed"   // dev
	Qwen3_5_9B  = "Qwen3_5_9B_Compressed" // native tools
	ChatGPT     = "ChatGPTOpenAI"         // e2e, native tools
	thinkOpen   = "<think>"
	thinkClose  = "</think>"
	suffixThink = "/think"
	suffixNo    = "/no_think"
)

// Provider is one prompt provider: the prompt builder plus the model family's output quirks.
type Provider interface {
	// Name is the registry key (llm.prompt_provider).
	Name() string
	// BuildSystemPrompt is layer 1, the provider's own prompt. Use AssembleSystemPrompt for
	// the full messages[0] (layer 2 adds the handler wrapper).
	BuildSystemPrompt(ctx Context, tools []Tool, flags []CommandFlag) string
	// SupportsNativeTools selects the native path (tools sent via the API's "tools").
	SupportsNativeTools() bool
	// ForceToolCalls arms the engine's force-tools guard (all Qwen providers, even native 9B).
	ForceToolCalls() bool
	// ResponseFormat is the response_format to send ({"type":"text"} for Qwen); nil means
	// Python's None (ChatGPT; unused on the native path).
	ResponseFormat() *pyjson.Object
	// UserMessageSuffix is appended to every user message after a single "\n" ("" = none).
	UserMessageSuffix(includeThinking bool) string
	// ParseResponse turns raw model output into Jarvis JSON for ParseToolCalls; ok=false is
	// Python's None (feed the raw content through unchanged).
	ParseResponse(raw string) (string, bool)
	// SanitizeText cleans any user-facing text before TTS.
	SanitizeText(text string) string
	// BuildTools is the native tools payload transform (ToolBuilder.build). Pass
	// StripJarvisExtensions(tools) first, as NativeTools does.
	BuildTools(tools []Tool) []Tool
	// ThinkDelimiters are the chain-of-thought markers streaming code strips.
	ThinkDelimiters() (open, close string)
	// Capabilities is the health/admin metadata.
	Capabilities() Capabilities
}

// Capabilities is get_capabilities(), in the legacy key order. use_tool_classifier keeps the
// legacy value for the admin surface although the fastText router is cut (D9).
type Capabilities struct {
	ProviderName        string `json:"provider_name"`
	ModelFamily         string `json:"model_family"`
	SizeTier            string `json:"size_tier"`
	TrainingTier        string `json:"training_tier"`
	UseToolClassifier   bool   `json:"use_tool_classifier"`
	SupportsNativeTools bool   `json:"supports_native_tools"`
}

// family selects the prompt template.
type family int

const (
	familyQwen family = iota
	familyChatGPT
)

// spec parameterises the one builder (D22): every provider is a spec value.
type spec struct {
	name   string
	family family
	// Qwen only.
	rules             []string // rule constants, in order ({terminology} → function)
	paramDescriptions bool     // keep per-property descriptions in the <tools> block
	native            bool     // no <tools> block / <tool_call> format; tools go via the API
	// parseUnwrapMessage: parse_response unwraps <message> (14B; 8B/9B never did).
	parseUnwrapMessage bool
	// sanitizeUnwrapMessage: sanitize_text unwraps <message> (14B; 8B/9B since D22).
	sanitizeUnwrapMessage bool
	caps                  Capabilities
}

var (
	rules4 = []string{RulePopulateRequired, RuleOneAtATime, RuleBestMatchIntent, RuleSttAwareness}
	rules5 = []string{RulePopulateRequired, RuleOneAtATime, RuleBestMatchIntent, RuleExtractParams, RuleSttAwareness}
)

var registry = map[string]*spec{
	Qwen3_14B: {
		name: Qwen3_14B, family: familyQwen, rules: rules5, paramDescriptions: true,
		parseUnwrapMessage: true, sanitizeUnwrapMessage: true,
		caps: Capabilities{Qwen3_14B, "qwen", "large", "untrained", true, false},
	},
	Qwen3_8B: {
		name: Qwen3_8B, family: familyQwen, rules: rules4,
		sanitizeUnwrapMessage: true, // D22 (D8 fix)
		caps:                  Capabilities{Qwen3_8B, "qwen", "medium", "untrained", true, false},
	},
	Qwen3_5_9B: {
		name: Qwen3_5_9B, family: familyQwen, rules: rules4, native: true,
		sanitizeUnwrapMessage: true, // D22 (D8 fix)
		caps:                  Capabilities{Qwen3_5_9B, "qwen3.5", "medium", "untrained", true, true},
	},
	ChatGPT: {
		name: ChatGPT, family: familyChatGPT,
		caps: Capabilities{ChatGPT, "openai", "large", "untrained", false, true},
	},
}

// Lookup returns the provider for an llm.prompt_provider value (exact name). There is no
// default and no remap of dropped legacy names (D11): anything else is an error.
func Lookup(name string) (Provider, error) {
	if s, ok := registry[name]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("unknown prompt provider %q (want one of %s)", name, strings.Join(Names(), ", "))
}

// Names lists the registered providers, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *spec) Name() string                      { return s.name }
func (s *spec) SupportsNativeTools() bool         { return s.family == familyChatGPT || s.native }
func (s *spec) ForceToolCalls() bool              { return s.family == familyQwen }
func (s *spec) ThinkDelimiters() (string, string) { return thinkOpen, thinkClose }
func (s *spec) Capabilities() Capabilities        { return s.caps }

func (s *spec) ResponseFormat() *pyjson.Object {
	if s.family != familyQwen {
		return nil
	}
	o := pyjson.NewObject()
	o.Set("type", "text")
	return o
}

func (s *spec) UserMessageSuffix(includeThinking bool) string {
	if s.family != familyQwen {
		return ""
	}
	if includeThinking {
		return suffixThink
	}
	return suffixNo
}

func (s *spec) ParseResponse(raw string) (string, bool) {
	if s.family != familyQwen {
		return "", false
	}
	return parse.ParseQwen3(raw, s.parseUnwrapMessage)
}

func (s *spec) SanitizeText(text string) string {
	if s.family != familyQwen {
		return text
	}
	return parse.SanitizeQwen3(text, s.sanitizeUnwrapMessage)
}

func (s *spec) BuildTools(tools []Tool) []Tool { return buildTools(tools, nativeBuild) }

// NativeTools is the native path's tools payload: p.BuildTools(StripJarvisExtensions(tools)).
// Warmup and inference must both send exactly this for the prefix cache to hit (03 §7.3).
func NativeTools(p Provider, tools []Tool) []Tool {
	return p.BuildTools(StripJarvisExtensions(tools))
}

func sub(rule string) string { return strings.ReplaceAll(rule, "{terminology}", "function") }

func rulesBlock(rules []string) string {
	lines := []string{"Rules:"}
	for _, r := range rules {
		lines = append(lines, "- "+sub(r))
	}
	return strings.Join(lines, "\n")
}

// chatGPTRules is core_rules.build_rules_block() with its defaults (7 rules).
var chatGPTRules = []string{
	RulePopulateRequired, RuleOneAtATime, RuleUseActualParamNames, RuleBestMatchIntent,
	RuleDateParams, RuleExtractParams, RuleSttAwareness,
}

const qwenToolFormat = "For each function call, return a json object with function name and arguments within <tool_call></tool_call> XML tags:\n" +
	"<tool_call>\n" +
	`{"name": "<function-name>", "arguments": {"<arg-name>": "<arg-value>"}, "failure_message": "<brief spoken response if this call fails>"}` + "\n" +
	"</tool_call>"

const chatGPTPreamble = "You are a function-calling voice assistant. Use the provided functions to act on the user's request: " +
	"call one or more functions when the request maps to them, and always include every required parameter — " +
	"use sensible defaults from context when the user does not state them explicitly. Never make the user supply " +
	"an OPTIONAL parameter you can default — call the function and let the system fill it in. For example, answer " +
	"\"what time is it\" by calling get_current_time with no arguments (the system uses the local timezone); do " +
	"NOT reply by asking which location. "

// BuildSystemPrompt is the parameterised builder behind all four providers.
func (s *spec) BuildSystemPrompt(ctx Context, tools []Tool, flags []CommandFlag) string {
	identity := identityHeader(ctx)
	direct := directAnswerSection(flags)
	agent := agentContextSummary(ctx)

	if s.family == familyChatGPT {
		return identity + "\n\n" + chatGPTPreamble + AntiHallucinationMandate + "\n\n" +
			rulesBlock(chatGPTRules) + "\n" + agent + "\n" +
			strings.ReplaceAll(FallbackBriefReply, "{terminology}", "tool") + "\n" + direct
	}

	functions := "functions"
	if s.native {
		functions = "of the available functions"
	}
	var b strings.Builder
	b.WriteString(identity)
	b.WriteString("\n\nYou are a function calling AI model. You may call one or more " + functions +
		" to assist with the user query. Always include all required parameters — use sensible defaults from context when the user does not state them explicitly. ")
	b.WriteString(AntiHallucinationMandate)
	b.WriteString("\n\n")
	if !s.native {
		b.WriteString("You are provided with function signatures within <tools></tools> XML tags:\n")
		b.WriteString(toolsBlock(tools, buildOptions{paramDescriptions: s.paramDescriptions, excludeRefinable: true}))
		b.WriteString("\n\n" + qwenToolFormat + "\n\n")
	}
	b.WriteString(rulesBlock(s.rules))
	b.WriteString("\n")
	b.WriteString(dtKeysLine(ctx.DateKeys))
	b.WriteString(toolGuidanceSection(tools))
	b.WriteString(direct)
	b.WriteString("\n")
	b.WriteString(agent)
	b.WriteString("\n")
	return b.String()
}
