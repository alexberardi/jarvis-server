package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Node is the fake node: it speaks command-center's node API (docs/cc/01) with a real node's
// credentials, as a Pi or the desktop agent would after STT.
type Node struct {
	BaseURL string // command-center, e.g. http://127.0.0.1:17703
	ID, Key string
	HTTP    *http.Client
}

// ToolCall is an OpenAI tool call, as command-center returns it to the node.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Args decodes the call's JSON arguments; nil when they are not an object.
func (c ToolCall) Args() map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(c.Function.Arguments), &m) != nil {
		return nil
	}
	return m
}

// TurnResponse is VoiceCommandResponse, the fields the runner needs.
type TurnResponse struct {
	StopReason        string     `json:"stop_reason"`
	ToolCalls         []ToolCall `json:"tool_calls"`
	AssistantMessage  *string    `json:"assistant_message"`
	ValidationRequest *struct {
		Question string `json:"question"`
	} `json:"validation_request"`
	Commands      json.RawMessage `json:"commands"`
	EndOfExchange *bool           `json:"end_of_exchange"`
}

// ToolResult is what the node sends back for one call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Output     any    `json:"output"`
}

func (n *Node) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", n.ID+":"+n.Key)
	client := n.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: %d %s", path, resp.StatusCode, truncate(string(rb), 500))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(rb, out); err != nil {
		return fmt.Errorf("POST %s: decode: %w", path, err)
	}
	return nil
}

// Start opens a conversation with the node's tool set.
func (n *Node) Start(ctx context.Context, cid, timezone string, tools []json.RawMessage) error {
	if tools == nil {
		tools = []json.RawMessage{}
	}
	return n.post(ctx, "/api/v0/conversation/start", map[string]any{
		"conversation_id":    cid,
		"node_context":       map[string]any{"timezone": timezone},
		"available_commands": []any{},
		"client_tools":       tools,
	}, nil)
}

// Command sends one utterance (what STT would have produced).
func (n *Node) Command(ctx context.Context, cid, text string) (TurnResponse, error) {
	var r TurnResponse
	err := n.post(ctx, "/api/v0/voice/command", map[string]any{"voice_command": text, "conversation_id": cid}, &r)
	return r, err
}

// Continue returns the node's tool results.
func (n *Node) Continue(ctx context.Context, cid string, results []ToolResult) (TurnResponse, error) {
	var r TurnResponse
	err := n.post(ctx, "/api/v0/voice/command/continue", map[string]any{"conversation_id": cid, "tool_results": results}, &r)
	return r, err
}

// End closes the conversation.
func (n *Node) End(ctx context.Context, cid string) error {
	return n.post(ctx, "/api/v0/conversation/end", map[string]any{"conversation_id": cid}, nil)
}
