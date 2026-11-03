// Package prompts builds command-center's system prompts. A prompt is text with {{variables}}
// (docs/cc/prompt-contract.md): the four built-in prompts are embedded text files, Render
// fills in the variables from Inputs, and SystemPrompt adds the per-turn <person_view> tail.
// How a turn talks to the model (native or text tool calling, the thinking suffix, the
// force-tools guard) is a Mode, resolved from the LLM settings and the model, not from the
// prompt's name. The package also has the per-turn blocks, the tool transforms (ToolBuilder),
// the one output cleanup (ParseResponse, SanitizeText) and the per-household server-tool gates.
//
// The built-ins reproduce the legacy providers byte-exact (D22): fixtures/golden/prompts (G1)
// is the gate. Rule text lives in the generated rules.go, copied from the legacy Python by
// internal/genrules.
package prompts

// Built-in prompt names (the llm.prompt_provider values).
const (
	Qwen3_14B   = "Qwen3_14B_Compressed"  // prod (serves the 27B)
	Qwen3_8B    = "Qwen3_8B_Compressed"   // dev
	Qwen3_5_9B  = "Qwen3_5_9B_Compressed" // native tools
	Cloud       = "Cloud"                 // remote models (any vendor), native tools
	thinkOpen   = "<think>"
	thinkClose  = "</think>"
	suffixThink = "/think"
	suffixNo    = "/no_think"
)

const qwenToolFormat = "For each function call, return a json object with function name and arguments within <tool_call></tool_call> XML tags:\n" +
	"<tool_call>\n" +
	`{"name": "<function-name>", "arguments": {"<arg-name>": "<arg-value>"}, "failure_message": "<brief spoken response if this call fails>"}` + "\n" +
	"</tool_call>"
