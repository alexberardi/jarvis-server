// Package servertools is command-center's server-tool registry (docs/cc/02 §2, §11): tools the
// LLM calls that jarvisd runs in process, as opposed to client tools the node runs.
//
// Tools are registered explicitly (no discovery). A call reaches a tool only when its name is
// in the turn's offered server set (02 §11 "explicit plane routing"), so a tool that was gated
// off is never run. Every call carries the speaker identity, or its explicit absence (D21):
// each tool decides what to refuse, the framework imposes no policy.
package servertools

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Speaker is the conversation's speaker identity as known to this turn (D3: per conversation,
// from turns jarvisd identified itself). UserID 0 means unknown, which includes ambiguous
// matches (D21).
type Speaker struct {
	UserID int64
	Name   string
	// RecognitionOff: the household has voice.recognition_enabled off (D35), so every speaker
	// is unknown; refusals say "speaker recognition is off" (M14).
	RecognitionOff bool
}

// Known reports whether the speaker was confidently identified.
func (s Speaker) Known() bool { return s.UserID != 0 }

// Refusal is the D21 / M14 wording a per-user tool speaks when the speaker is unknown.
func (s Speaker) Refusal() string {
	if s.RecognitionOff {
		return "Speaker recognition is off, so I can't tell who's speaking."
	}
	return "I'm not sure who's speaking."
}

// Turn is the context a server tool runs in: the conversation, its node and household, the
// speaker, and the turn's raw utterance (legacy user_utterance kwarg).
type Turn struct {
	ConversationID string
	HouseholdID    string
	NodeID         string
	// Timezone is the node's IANA zone ("" = unknown).
	Timezone  string
	Utterance string
	Speaker   Speaker
	// Agents is the node's read-only agent payload from /conversation/start (may be nil).
	Agents *pyjson.Object
	// MemberIDs are the household's validated member ids.
	MemberIDs []int64
	// Images resolves the chat photos the model currently sees, by number (images.go). Nil
	// outside mobile chat.
	Images TurnImages
}

// Call is one tool call addressed to a server tool.
type Call struct {
	ID   string
	Name string
	// Args are the parsed arguments: an empty object when the JSON was invalid or not an
	// object (legacy json.loads failure → {}).
	Args *pyjson.Object
}

// Str returns a string argument ("" when absent or not a string).
func (c Call) Str(name string) string {
	if c.Args == nil {
		return ""
	}
	v, _ := c.Args.Get(name)
	s, _ := v.(string)
	return s
}

// Arg returns a raw argument.
func (c Call) Arg(name string) (any, bool) {
	if c.Args == nil {
		return nil, false
	}
	return c.Args.Get(name)
}

// ServerTool is one in-process tool.
type ServerTool interface {
	Name() string
	// Definition is the OpenAI tool dict exactly as the legacy to_openai_format() built it
	// (key order is prompt bytes): {"type":"function","function":{name,description,parameters}}
	// plus a top-level "included_system_prompt_text" when the tool has guidance.
	Definition() *pyjson.Object
	// Execute runs the tool. The result is JSON-encodable (an *pyjson.Object keeps key order;
	// it becomes the role=tool message content via Python's json.dumps). An error is reported
	// to the model as {"error":"execution_error","message":"Tool execution failed: <err>"},
	// as the legacy registry did.
	Execute(ctx context.Context, call Call, turn Turn) (any, error)
}

// Registry holds the registered server tools. It is safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]ServerTool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]ServerTool{}} }

// Register adds a tool; a duplicate name replaces the earlier one.
func (r *Registry) Register(t ServerTool) {
	r.mu.Lock()
	r.tools[t.Name()] = t
	r.mu.Unlock()
}

// Get returns a registered tool.
func (r *Registry) Get(name string) (ServerTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names lists registered tools in the legacy registry order: the discovery walk visited the
// tool modules alphabetically, and every module is named after its tool.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Definitions returns the definitions of the named tools, in the order given, skipping names
// that are not registered (legacy get_tool → None was skipped).
func (r *Registry) Definitions(names []string) []*pyjson.Object {
	var out []*pyjson.Object
	for _, n := range names {
		if t, ok := r.Get(n); ok {
			out = append(out, t.Definition())
		}
	}
	return out
}

// Execute runs one call, converting a panic or error into the legacy execution_error dict.
func (r *Registry) Execute(ctx context.Context, call Call, turn Turn) (result any) {
	t, ok := r.Get(call.Name)
	if !ok {
		o := pyjson.NewObject()
		o.Set("error", "tool_not_found")
		o.Set("message", "Tool '"+call.Name+"' is not registered")
		return o
	}
	defer func() {
		if p := recover(); p != nil {
			result = execError(fmt.Errorf("%v", p))
		}
	}()
	res, err := t.Execute(ctx, call, turn)
	if err != nil {
		return execError(err)
	}
	return res
}

func execError(err error) *pyjson.Object {
	o := pyjson.NewObject()
	o.Set("error", "execution_error")
	o.Set("message", "Tool execution failed: "+err.Error())
	return o
}

// LegacyDefinition returns a copy of the legacy definition dict for a known tool name (the
// generated table), so ports register byte-exact schemas. It panics on an unknown name: the
// table is static.
func LegacyDefinition(name string) *pyjson.Object {
	raw, ok := legacyDefinitions[name]
	if !ok {
		panic("servertools: no legacy definition for " + name)
	}
	v, err := pyjson.Loads(raw)
	if err != nil {
		panic(err)
	}
	return v.(*pyjson.Object)
}

// Obj builds an ordered object from alternating keys and values.
func Obj(kv ...any) *pyjson.Object {
	o := pyjson.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
