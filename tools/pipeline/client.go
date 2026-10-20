package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
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
	// StoodDown: another node of the household answers this wake (not_for_me).
	StoodDown bool `json:"stood_down"`
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
	rb, err := n.send(ctx, path, "application/json", b)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(rb, out); err != nil {
		return fmt.Errorf("POST %s: decode: %w", path, err)
	}
	return nil
}

// send POSTs a body with the node's credentials and returns a 200's body.
func (n *Node) send(ctx context.Context, path, contentType string, body []byte) ([]byte, error) {
	return do(ctx, n.HTTP, n.BaseURL+path, contentType, body, map[string]string{"X-API-Key": n.ID + ":" + n.Key})
}

// postJSON POSTs JSON to a URL and decodes a 200's body into out.
func postJSON(ctx context.Context, url string, header map[string]string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	rb, err := do(ctx, nil, url, "application/json", b, header)
	if err != nil {
		return err
	}
	return json.Unmarshal(rb, out)
}

// do POSTs a body and returns a 200's body; any other status is an error with the body.
func do(ctx context.Context, client *http.Client, url, contentType string, body []byte, header map[string]string) ([]byte, error) {
	return doWant(ctx, client, url, contentType, body, header, http.StatusOK)
}

// doWant is do accepting any of the given statuses (a create's 201).
func doWant(ctx context.Context, client *http.Client, url, contentType string, body []byte, header map[string]string, ok ...int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ok, resp.StatusCode) {
		return nil, fmt.Errorf("POST %s: %d %s", req.URL.Path, resp.StatusCode, truncate(strings.TrimSpace(string(rb)), 500))
	}
	return rb, nil
}

// Start opens a conversation with the node's tool set and, when agents is not nil, its
// device payload (node_context.agents); a vocab that is not "" is node_context.stt_vocabulary.
func (n *Node) Start(ctx context.Context, cid, timezone string, tools []json.RawMessage, agents json.RawMessage, vocab string) error {
	if tools == nil {
		tools = []json.RawMessage{}
	}
	nc := map[string]any{"timezone": timezone}
	if agents != nil {
		nc["agents"] = agents
	}
	if vocab != "" {
		nc["stt_vocabulary"] = vocab
	}
	return n.post(ctx, "/api/v0/conversation/start", map[string]any{
		"conversation_id":    cid,
		"node_context":       nc,
		"available_commands": []any{},
		"client_tools":       tools,
	}, nil)
}

// Command sends one utterance (what STT would have produced). speaker, when not 0, is sent as
// speaker_user_id: a sandbox with JARVIS_DEV_SPEAKER_HINTS=1 takes it as the voice match. extra
// are more request fields (turn_source, computer_activity).
func (n *Node) Command(ctx context.Context, cid, text string, speaker int64, extra map[string]any) (TurnResponse, error) {
	var r TurnResponse
	body := map[string]any{"voice_command": text, "conversation_id": cid}
	for k, v := range extra {
		body[k] = v
	}
	if speaker != 0 {
		body["speaker_user_id"] = speaker
	}
	err := n.post(ctx, "/api/v0/voice/command", body, &r)
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
