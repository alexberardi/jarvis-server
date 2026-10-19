package nodesettings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// Plan-time context queries (N:services/context_handler.py): a server-side planner asks the
// node's commands for typed, read-only context before it acts, on
// jarvis/nodes/{id}/context/{op}, and waits for the answer on …/response/{correlation_id}.
// The ops are "operations" (every context operation the commands declare) and "query" (run
// one: {command?, operation, params, user_id}). The commands are the sidecar's; the
// operation runs there, with the requesting user set as on a voice turn.

// TopicContext is the subscription filter for the context requests.
const TopicContext = "context/+"

// MaxContextResponse caps an answer (N:'s MAX_RESPONSE_BYTES): a runaway provider must not
// wedge the broker or blow the planner's timeout.
const MaxContextResponse = 128 << 10

// ContextCommand is one command with the context operations it declares, each the
// ContextOperation dict as the command made it ({name, description, params_schema}).
type ContextCommand struct {
	Name       string            `json:"command_name"`
	Operations []json.RawMessage `json:"operations"`
}

// ContextProviders are the commands that can answer context queries (the sidecar's).
type ContextProviders interface {
	// ContextCommands lists every command, with its declared operations (none for most).
	ContextCommands() []ContextCommand
	// ContextQuery runs one operation and returns its ContextResult dict ({ok, data, error}).
	// An error is the operation raising.
	ContextQuery(ctx context.Context, command, operation string, params json.RawMessage, userID *int64) (Object, error)
}

// Context answers the context requests for one node.
type Context struct {
	Store     *Store           // the household's on/off switches; nil: every command is on
	Providers ContextProviders // nil: no commands
	Log       *slog.Logger
}

func (c *Context) commands() []ContextCommand {
	if c.Providers == nil {
		return nil
	}
	var out []ContextCommand
	for _, cmd := range c.Providers.ContextCommands() {
		// A command the household turned off is not installed as far as the planner knows.
		if c.Store == nil || c.Store.Enabled(cmd.Name) {
			out = append(out, cmd)
		}
	}
	return out
}

// Handle answers one request. topic is the full request topic (…/context/{op}); it returns the
// response topic and body, or "" when there is nothing to answer to (an unknown op, a payload
// that isn't a JSON object or has no correlation id, as on the Pi). Every other failure is an
// answer: a silent drop would read as "node offline" on the planner's side.
func (c *Context) Handle(ctx context.Context, topic string, payload []byte) (string, []byte) {
	op := topic[strings.LastIndex(topic, "/")+1:]
	handler, ok := map[string]func(context.Context, cdPayload) map[string]any{
		"operations": c.operations, "query": c.query,
	}[op]
	if !ok {
		c.Log.Warn("context: unknown op", "op", op)
		return "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var p cdPayload
	if err := dec.Decode(&p.m); err != nil || p.m == nil || json.Unmarshal(payload, &p.raw) != nil {
		c.Log.Warn("context: payload is not a JSON object", "op", op)
		return "", nil
	}
	cid := p.str("correlation_id")
	if cid == "" || strings.ContainsAny(cid, "/+#") {
		return "", nil
	}
	resp := c.safely(ctx, op, handler, p)
	body, err := json.Marshal(resp)
	switch {
	case err != nil:
		c.Log.Error("context: response serialisation failed", "op", op, "err", err)
		body = []byte(`{"ok":false,"error":"context result was not serialisable"}`)
	case len(body) > MaxContextResponse:
		c.Log.Warn("context: response too large", "op", op, "size", len(body))
		body = []byte(`{"ok":false,"error":"context result too large"}`)
	}
	return topic + "/response/" + cid, body
}

func (c *Context) safely(ctx context.Context, op string, h func(context.Context, cdPayload) map[string]any, p cdPayload) (resp map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			c.Log.Error("context: handler crashed", "op", op, "err", r)
			resp = ctxFail(fmt.Sprintf("context handler failed: %v", r))
		}
	}()
	return h(ctx, p)
}

func ctxFail(msg string) map[string]any { return map[string]any{"ok": false, "error": msg} }

func (c *Context) operations(context.Context, cdPayload) map[string]any {
	providers := []map[string]any{}
	for _, cmd := range c.commands() {
		if len(cmd.Operations) > 0 {
			providers = append(providers, map[string]any{"command_name": cmd.Name, "operations": cmd.Operations})
		}
	}
	return map[string]any{"ok": true, "providers": providers}
}

// contextOp is the part of a declared operation the node reads.
type contextOp struct {
	Name         string `json:"name"`
	ParamsSchema Object `json:"params_schema"`
}

func findOp(cmd ContextCommand, name string) (contextOp, bool) {
	for _, raw := range cmd.Operations {
		var op contextOp
		if json.Unmarshal(raw, &op) == nil && op.Name == name {
			return op, true
		}
	}
	return contextOp{}, false
}

// missing is ContextOperation.missing_required: the required params absent, in schema order.
func (op contextOp) missing(params Object) []string {
	var out []string
	for _, name := range op.ParamsSchema.Keys() {
		var spec struct {
			Required any `json:"required"`
		}
		raw, _ := op.ParamsSchema.Get(name)
		if json.Unmarshal(raw, &spec) != nil || !pyTruthy(spec.Required) {
			continue
		}
		if _, ok := params.Get(name); !ok {
			out = append(out, name)
		}
	}
	return out
}

// pyTruthy is Python's bool() for a JSON value.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// userID is N:'s int(user_id): an integer, a float (truncated), a bool or an integer string;
// anything else is no user.
func userID(v any) *int64 {
	var n int64
	switch x := v.(type) {
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			f, ferr := x.Float64()
			if ferr != nil {
				return nil
			}
			i = int64(f)
		}
		n = i
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return nil
		}
		n = i
	case bool:
		if x {
			n = 1
		}
	default:
		return nil
	}
	return &n
}

func (c *Context) query(ctx context.Context, p cdPayload) map[string]any {
	operation, ok := p.m["operation"].(string)
	if !ok || operation == "" {
		return ctxFail("operation is required")
	}
	params := Object{}
	if raw, ok := p.raw["params"]; ok && !isNullish(raw) {
		var isObj bool
		if params, isObj = asObject(raw); !isObj {
			return ctxFail("params must be an object")
		}
	}
	cmds := c.commands()
	var cmd ContextCommand
	var op contextOp
	found := false
	if want := p.m["command"]; pyTruthy(want) {
		name := fmt.Sprint(want)
		installed := false
		for _, x := range cmds {
			if x.Name == name {
				cmd, installed = x, true
				break
			}
		}
		if !installed {
			return ctxFail(fmt.Sprintf("command '%s' not installed", name))
		}
		op, found = findOp(cmd, operation)
	} else {
		// No command: the first one declaring the operation answers it, so a planner can ask
		// for "availability" without knowing which package provides it.
		for _, x := range cmds {
			if op, found = findOp(x, operation); found {
				cmd = x
				break
			}
		}
	}
	if !found {
		return map[string]any{"ok": false, "error": fmt.Sprintf("no installed command provides '%s'", operation), "code": "no_provider"}
	}
	if m := op.missing(params); len(m) > 0 {
		return ctxFail("missing required params: " + strings.Join(m, ", "))
	}
	rawParams, _ := params.MarshalJSON()
	res, err := c.Providers.ContextQuery(ctx, cmd.Name, operation, rawParams, userID(p.m["user_id"]))
	if err != nil {
		c.Log.Error("context: operation raised", "command", cmd.Name, "operation", operation, "err", err)
		return ctxFail(fmt.Sprintf("%s.%s failed: %v", cmd.Name, operation, err))
	}
	out := map[string]any{}
	for _, k := range res.Keys() {
		raw, _ := res.Get(k)
		out[k] = raw
	}
	out["command_name"], out["operation"] = cmd.Name, operation
	return out
}

// isNullish is Python's `payload.get("params") or {}`: null, {}, [], "", 0 and false all mean
// no params; [] and the others that are not objects but falsy are treated as none too.
func isNullish(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	return !pyTruthy(v)
}
