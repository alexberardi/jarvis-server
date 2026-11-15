package llm

import "testing"

// A remote that takes llama-server's thinking controls (remote_thinking) gets them; any other
// remote gets none: OpenAI answers 400 to a parameter it doesn't know.
func TestApplyThinkingToARemoteThatTakesIt(t *testing.T) {
	off, mt := 0, 100
	p := chatPayload{MaxTokens: &mt}
	p.applyThinking(&off, Endpoint{Remote: true})
	if p.ChatTemplateKwargs != nil || p.ReasoningBudget != nil {
		t.Errorf("a plain remote got thinking controls: %+v", p)
	}
	p = chatPayload{MaxTokens: &mt}
	p.applyThinking(&off, Endpoint{Remote: true, ThinkingControls: true})
	if p.ChatTemplateKwargs["enable_thinking"] != false || p.ReasoningBudget == nil || *p.ReasoningBudget != 0 {
		t.Errorf("a remote that takes them got %+v", p)
	}
	n := 512
	p = chatPayload{MaxTokens: &mt}
	p.applyThinking(&n, Endpoint{Remote: true, ThinkingControls: true})
	if !p.ChatTemplateKwargs["enable_thinking"] || p.ReasoningBudgetTokens == nil || *p.ReasoningBudgetTokens != 512 {
		t.Errorf("thinking on for a remote that takes it: %+v", p)
	}
	p = chatPayload{MaxTokens: &mt}
	p.applyThinking(&off, Endpoint{Remote: true, ThinkingControls: true, API: APIAnthropic})
	if p.ChatTemplateKwargs != nil {
		t.Errorf("the Anthropic API got llama's controls: %+v", p)
	}
}
