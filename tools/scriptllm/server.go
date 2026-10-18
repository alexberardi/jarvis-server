package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Model is the one model the server lists and answers as.
const Model = "scripted"

// Server is the scripted model: an OpenAI-compatible chat API that answers from the active
// script, and a control API for tests.
//
//	GET  /v1/models            one model, "scripted"
//	POST /v1/chat/completions  stream or not; tool calls in the OpenAI wire shape
//	GET  /control/script       the active script's name and rule count
//	POST /control/script       switch: {"path": "e2e/llm/x.json"} (relative to Dir) or
//	                           {"script": {...}} inline; an empty body reloads the current file
//	GET  /control/unmatched    the turns no rule matched since the last DELETE
//	DELETE /control/unmatched  forget them
type Server struct {
	Dir     string    // where relative script paths are resolved
	Log     io.Writer // unmatched turns, one JSON object per line; nil = none
	Context int       // context_length reported by /v1/models

	mu        sync.Mutex
	script    *Script
	path      string // the active script's file; "" when inline
	unmatched []Unmatched
	seq       int
}

// Unmatched is a turn no rule matched, answered with the fallback.
type Unmatched struct {
	Time   time.Time `json:"time"`
	Script string    `json:"script"`
	Role   string    `json:"role"`
	Tool   string    `json:"tool,omitempty"`
	Text   string    `json:"text"`
}

// Use makes s the active script.
func (s *Server) Use(sc *Script, path string) {
	s.mu.Lock()
	s.script, s.path = sc, path
	s.mu.Unlock()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]any{
			{"id": Model, "object": "model", "owned_by": "scriptllm", "context_length": s.Context}}})
	case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
		s.chat(w, r)
	case r.URL.Path == "/control/script":
		s.control(w, r)
	case r.URL.Path == "/control/unmatched":
		s.mu.Lock()
		if r.Method == http.MethodDelete {
			s.unmatched = nil
		}
		out := append([]Unmatched{}, s.unmatched...)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	}
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body struct {
			Path   string          `json:"path"`
			Script json.RawMessage `json:"script"`
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err == nil && len(strings.TrimSpace(string(b))) > 0 {
			err = json.Unmarshal(b, &body)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var sc *Script
		path := body.Path
		switch {
		case len(body.Script) > 0:
			sc, err = ParseScript("inline", body.Script)
			path = ""
		default:
			if path == "" {
				s.mu.Lock()
				path = s.path
				s.mu.Unlock()
				if path == "" {
					writeError(w, http.StatusBadRequest, "no path, no script, and the active script is inline")
					return
				}
			} else if !filepath.IsAbs(path) {
				path = filepath.Join(s.Dir, path)
			}
			sc, err = LoadScript(path)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.Use(sc, path)
	} else if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET or POST")
		return
	}
	s.mu.Lock()
	name, n := s.script.Name, len(s.script.Rules)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"script": name, "rules": n})
}

// answer is what the script says to one request.
type answer struct {
	Content   string
	ToolCalls []wireToolCall
	Finish    string
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// respond finds the rule for the request's last turn; no rule is the fallback, recorded. A
// request with no turn (a warm-up) gets an empty reply.
func (s *Server) respond(msgs []wireMessage) answer {
	t, ok := lastTurn(msgs)
	if !ok {
		return answer{Finish: "stop"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rule := s.script.Find(t)
	if rule == nil {
		u := Unmatched{Time: time.Now().UTC(), Script: s.script.Name, Role: t.Role, Tool: t.Tool, Text: t.Text}
		s.unmatched = append(s.unmatched, u)
		if s.Log != nil {
			b, _ := json.Marshal(u)
			s.Log.Write(append(b, '\n'))
		}
		return answer{Content: s.script.Fallback, Finish: "stop"}
	}
	a := answer{Content: rule.Reply, Finish: "stop"}
	for _, c := range rule.ToolCalls {
		s.seq++
		var tc wireToolCall
		tc.ID = fmt.Sprintf("call_scripted_%d", s.seq)
		tc.Type = "function"
		tc.Function.Name = c.Name
		tc.Function.Arguments = "{}"
		if len(c.Arguments) > 0 {
			tc.Function.Arguments = string(c.Arguments)
		}
		a.ToolCalls = append(a.ToolCalls, tc)
		a.Finish = "tool_calls"
	}
	return a
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages      []wireMessage `json:"messages"`
		Stream        bool          `json:"stream"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	a := s.respond(req.Messages)
	prompt := 0
	for _, m := range req.Messages {
		prompt += len(m.text())/4 + 1
	}
	usage := map[string]int{"prompt_tokens": prompt, "completion_tokens": len(a.Content)/4 + len(a.ToolCalls)*8}
	usage["total_tokens"] = usage["prompt_tokens"] + usage["completion_tokens"]
	id := fmt.Sprintf("chatcmpl-scripted-%d", time.Now().UnixNano())
	created := time.Now().Unix()

	if !req.Stream {
		msg := map[string]any{"role": "assistant", "content": a.Content}
		if len(a.ToolCalls) > 0 {
			msg["tool_calls"] = a.ToolCalls
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "chat.completion", "created": created, "model": Model,
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": a.Finish}}, "usage": usage})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	send := func(choices []any, extra map[string]any) {
		ck := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": Model, "choices": choices}
		for k, v := range extra {
			ck[k] = v
		}
		b, _ := json.Marshal(ck)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	delta := func(d map[string]any, finish any) []any {
		return []any{map[string]any{"index": 0, "delta": d, "finish_reason": finish}}
	}
	send(delta(map[string]any{"role": "assistant", "content": ""}, nil), nil)
	// Content word by word, as an engine streams it.
	for _, piece := range splitWords(a.Content) {
		send(delta(map[string]any{"content": piece}, nil), nil)
	}
	// Each tool call in two fragments (id and name, then the arguments), so the client's
	// merge per index is exercised.
	for i, tc := range a.ToolCalls {
		send(delta(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": tc.ID, "type": "function",
			"function": map[string]any{"name": tc.Function.Name, "arguments": ""}}}}, nil), nil)
		send(delta(map[string]any{"tool_calls": []any{map[string]any{"index": i,
			"function": map[string]any{"arguments": tc.Function.Arguments}}}}, nil), nil)
	}
	send(delta(map[string]any{}, a.Finish), nil)
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		send([]any{}, map[string]any{"usage": usage})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

// splitWords cuts s into pieces that join back to s, each a word and the space after it.
func splitWords(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "scriptllm_error"}})
}

// openLog opens the unmatched log for appending.
func openLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
