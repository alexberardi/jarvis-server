package cc

import (
	"context"
	"errors"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// fakeLLMFunc answers Chat with one tool call; it records the user message and tools.
type fakeLLMFunc func(req map[string]any) (name, args string)

func (f fakeLLMFunc) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	var tools []string
	for _, t := range req.Tools {
		tools = append(tools, string(t.Raw))
	}
	user := ""
	if len(req.Messages) > 1 && req.Messages[1].Content != nil && req.Messages[1].Content.Text != nil {
		user = *req.Messages[1].Content.Text
	}
	if req.Label != "background" || req.MaxTokens == nil || *req.MaxTokens != 512 || req.ReasoningBudget == nil || *req.ReasoningBudget != 0 {
		return nil, errors.New("wrong request shape")
	}
	name, args := f(map[string]any{"user": user, "tools": strings.Join(tools, "\n")})
	return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}}}}, nil
}

func (f fakeLLMFunc) Stream(context.Context, llm.ChatRequest) (<-chan llm.Frame, error) {
	return nil, errors.New("no stream")
}

func (f fakeLLMFunc) Embed(context.Context, []string) (llm.Embeddings, error) {
	return llm.Embeddings{}, errors.New("no embeddings")
}
