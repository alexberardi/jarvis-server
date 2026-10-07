package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// fakeReply scripts one upstream answer.
type fakeReply struct {
	Status    int    // non-200: answer Body with this status
	Body      string // raw body for errors
	Content   string
	ToolCalls []map[string]any
	Finish    string
}

// fakeEngine is an OpenAI-compatible server (llama-server's /v1/chat/completions and
// /v1/embeddings): it records request bodies, answers scripted replies (default: echo "OK"),
// and streams content word by word.
type fakeEngine struct {
	srv *httptest.Server

	mu      sync.Mutex
	bodies  []map[string]any
	raw     [][]byte
	replies []fakeReply
	keys    []string
	// streamDelay paces stream chunks (cancel tests).
	streamDelay time.Duration
	streamText  string

	active, peak atomic.Int32
	gate         chan struct{} // when set, chat requests wait for a value
}

func newFakeEngine() *fakeEngine {
	f := &fakeEngine{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeEngine) close() { f.srv.Close() }

func (f *fakeEngine) script(r ...fakeReply) {
	f.mu.Lock()
	f.replies = append(f.replies, r...)
	f.mu.Unlock()
}

func (f *fakeEngine) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any{}, f.bodies...)
}

func (f *fakeEngine) last() map[string]any {
	r := f.requests()
	if len(r) == 0 {
		return nil
	}
	return r[len(r)-1]
}

func (f *fakeEngine) next() fakeReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) == 0 {
		return fakeReply{Content: "OK"}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

func (f *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.raw = append(f.raw, raw)
	f.keys = append(f.keys, r.Header.Get("Authorization"))
	f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/embeddings":
		f.serveEmbeddings(w, body)
		return
	case "/v1/chat/completions":
	default:
		http.NotFound(w, r)
		return
	}
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-r.Context().Done():
			return
		}
	}
	rep := f.next()
	if rep.Status != 0 && rep.Status != 200 {
		w.WriteHeader(rep.Status)
		io.WriteString(w, rep.Body)
		return
	}
	if stream, _ := body["stream"].(bool); stream {
		f.serveStream(w, r, rep)
		return
	}
	msg := map[string]any{"role": "assistant", "content": rep.Content}
	finish := rep.Finish
	if finish == "" {
		finish = "stop"
	}
	if len(rep.ToolCalls) > 0 {
		msg["content"] = nil
		msg["tool_calls"] = rep.ToolCalls
		if rep.Finish == "" {
			finish = "tool_calls"
		}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
	})
}

func (f *fakeEngine) serveStream(w http.ResponseWriter, r *http.Request, rep fakeReply) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	text := rep.Content
	if f.streamText != "" {
		text = f.streamText
	}
	words := strings.SplitAfter(text, " ")
	for _, wd := range words {
		if wd == "" {
			continue
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": wd}}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		if f.streamDelay > 0 {
			select {
			case <-time.After(f.streamDelay):
			case <-r.Context().Done():
				return
			}
		}
	}
	for i, tc := range rep.ToolCalls {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": i, "id": tc["id"], "type": "function", "function": tc["function"]}}}}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	finish := rep.Finish
	if finish == "" {
		finish = "stop"
		if len(rep.ToolCalls) > 0 {
			finish = "tool_calls"
		}
	}
	fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", finish)
	fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":5,\"total_tokens\":14}}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

func (f *fakeEngine) serveEmbeddings(w http.ResponseWriter, body map[string]any) {
	in, _ := body["input"].([]any)
	data := make([]any, len(in))
	for i, t := range in {
		h := fnv.New64a()
		io.WriteString(h, fmt.Sprint(t))
		seed := h.Sum64()
		vec := make([]float64, 8)
		for k := range vec {
			seed = seed*6364136223846793005 + 1442695040888963407
			vec[k] = float64(seed>>40)/float64(1<<24) - 0.5
		}
		data[len(in)-1-i] = map[string]any{"object": "embedding", "embedding": vec, "index": i} // out of order on purpose
	}
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": body["model"]})
}

// fakeResolver resolves labels to the fake engine (or errors).
type fakeResolver struct {
	mu  sync.Mutex
	eps map[string]Endpoint
	err map[string]error
}

func (r *fakeResolver) Resolve(_ context.Context, label string) (Endpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep := r.eps[label]
	if err := r.err[label]; err != nil {
		return ep, err
	}
	if ep.BaseURL == "" {
		return ep, &NotReadyError{State: StateNotConfigured}
	}
	return ep, nil
}

func (r *fakeResolver) set(label string, ep Endpoint, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eps[label] = ep
	r.err[label] = err
}

type fakeAuth struct{ fail bool }

func (f fakeAuth) ValidateApp(_ context.Context, id, key string) (authn.App, bool, error) {
	if f.fail {
		return authn.App{}, false, errors.New("auth down")
	}
	return authn.App{ID: id}, id == "app" && key == "secret", nil
}
func (fakeAuth) ValidateNode(context.Context, string, string, string) (authn.NodeValidation, error) {
	return authn.NodeValidation{}, nil
}
func (fakeAuth) HouseholdRole(context.Context, int64, string) (authn.Role, bool, error) {
	return "", false, nil
}
