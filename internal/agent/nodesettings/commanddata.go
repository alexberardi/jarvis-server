package nodesettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"
)

// The mobile command-data browser (the app's "Stored data" tab, docs/cc/12 §3.4): command-center
// relays each app call as one MQTT request on jarvis/nodes/{id}/command-data/{op} and waits for
// the answer on …/response/{correlation_id}. This is the Python node's command_data_handler.py
// over the agent's settings store, with the commands' browser hooks (display_summary,
// data_browser_create) run in the sidecar. Requests are plain JSON, like the Pi's: command data
// is not K2-encrypted on the wire.

// TopicCommandData is the subscription filter for the browser's requests.
const TopicCommandData = "command-data/+"

// ListCap is the Pi's LIST_CAP: at most this many visible records per list.
const ListCap = 200

// DataCommand is one command as the browser sees it (IJarvisCommand's data_browser_* and
// editable_fields). Fields are the FieldSpec dicts exactly as the command made them.
type DataCommand struct {
	Name           string          `json:"command_name"`
	Mode           string          `json:"mode"`
	StorageName    string          `json:"storage_name"`
	SupportsCreate bool            `json:"supports_create"`
	Fields         json.RawMessage `json:"fields"`
}

// ErrRejected is a create the command refused (its data_browser_create raised ValueError):
// the message goes back to the app as is.
type ErrRejected struct{ Message string }

func (e *ErrRejected) Error() string { return e.Message }

// DataCommands are the commands that can keep records (the sidecar's packages).
type DataCommands interface {
	// DataCommands lists every command with its browser settings.
	DataCommands() []DataCommand
	// Summaries runs display_summary on each record; a nil entry is a record whose summary failed.
	Summaries(ctx context.Context, command string, records []Object) ([]map[string]any, error)
	// Create runs data_browser_create and returns the new key and record. A refusal is
	// *ErrRejected.
	Create(ctx context.Context, command string, values Object, uid json.Number) (string, json.RawMessage, error)
}

// CommandData answers the browser's requests for one node.
type CommandData struct {
	Store    *Store
	Commands DataCommands // nil: no commands
	Log      *slog.Logger
	Now      func() time.Time // nil: time.Now

	mu sync.Mutex // one change at a time (update and delete read, then write)
}

func (cd *CommandData) now() time.Time {
	if cd.Now != nil {
		return cd.Now()
	}
	return time.Now()
}

// cdPayload is one request: its scalars decoded (numbers as json.Number), and the record
// objects (patch, data) kept in order.
type cdPayload struct {
	m   map[string]any
	raw map[string]json.RawMessage
}

func (p cdPayload) str(k string) string { s, _ := p.m[k].(string); return s }

// object is a field that is a JSON object (Python's isinstance(v, dict)).
func (p cdPayload) object(k string) (Object, bool) { return asObject(p.raw[k]) }

func asObject(raw json.RawMessage) (Object, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return Object{}, false
	}
	var o Object
	return o, json.Unmarshal(raw, &o) == nil
}

// Handle answers one request. topic is the full request topic (…/command-data/{op}); it returns
// the response topic and body, or "" when there is nothing to answer (no correlation id, an
// unknown op or a payload that isn't a JSON object, as on the Pi).
func (cd *CommandData) Handle(ctx context.Context, topic string, payload []byte) (string, []byte) {
	op := topic[strings.LastIndex(topic, "/")+1:]
	handler, ok := map[string]func(context.Context, cdPayload) map[string]any{
		"commands": cd.commands, "schema": cd.schema, "list": cd.list, "get": cd.get,
		"create": cd.create, "update": cd.update, "delete": cd.delete,
	}[op]
	if !ok {
		cd.Log.Warn("command-data: unknown op", "op", op)
		return "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var p cdPayload
	if err := dec.Decode(&p.m); err != nil || p.m == nil || json.Unmarshal(payload, &p.raw) != nil {
		cd.Log.Warn("command-data: payload is not a JSON object", "op", op)
		return "", nil
	}
	cid := p.str("correlation_id")
	if cid == "" || strings.ContainsAny(cid, "/+#") {
		return "", nil
	}
	resp := cd.safely(ctx, op, handler, p)
	body, err := json.Marshal(resp)
	if err != nil {
		cd.Log.Error("command-data: response serialisation failed", "op", op, "err", err)
		body = []byte(`{"ok":false,"error":{"message":"serialisation failed"}}`)
	}
	return topic + "/response/" + cid, body
}

// safely turns a crash into the Pi's "handler error" answer, so command-center isn't left to
// time out.
func (cd *CommandData) safely(ctx context.Context, op string, h func(context.Context, cdPayload) map[string]any, p cdPayload) (resp map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			cd.Log.Error("command-data: handler crashed", "op", op, "err", r)
			resp = handlerError(fmt.Errorf("%v", r))
		}
	}()
	return h(ctx, p)
}

func fail(msg string) map[string]any {
	return map[string]any{"ok": false, "error": map[string]any{"message": msg}}
}

func (cd *CommandData) all() []DataCommand {
	if cd.Commands == nil {
		return nil
	}
	return cd.Commands.DataCommands()
}

// resolve is _resolve_by_command_name plus _visible: a command whose mode is "disabled" is
// unknown to the browser (unknown modes pass through for the app to decide).
func (cd *CommandData) resolve(name string) *DataCommand {
	if name == "" {
		return nil
	}
	for _, c := range cd.all() {
		if c.Name == name && c.Mode != "disabled" {
			return &c
		}
	}
	return nil
}

func fields(c *DataCommand) json.RawMessage {
	if len(c.Fields) == 0 {
		return json.RawMessage("[]")
	}
	return c.Fields
}

func schemaOf(c *DataCommand) map[string]any {
	return map[string]any{"mode": c.Mode, "supports_create": c.SupportsCreate, "fields": fields(c)}
}

// canSee is _user_can_see: rows with a user_id are that user's; rows without one, and calls
// without a requesting user, see everything.
func canSee(record Object, requester any) bool {
	if requester == nil {
		return true
	}
	owner := record.Value("user_id")
	if owner == nil {
		return true
	}
	return pyEqual(owner, requester)
}

// pyEqual is Python's == for the JSON scalars a user id can be (7 == 7.0, "7" != 7).
func pyEqual(a, b any) bool {
	na, aNum := number(a)
	nb, bNum := number(b)
	if aNum || bNum {
		return aNum && bNum && na.Cmp(nb) == 0
	}
	return a == b
}

func number(v any) (*big.Float, bool) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case float64:
		return big.NewFloat(x), true
	case int64:
		return new(big.Float).SetInt64(x), true
	case int:
		return new(big.Float).SetInt64(int64(x)), true
	default:
		return nil, false
	}
	f, _, err := big.ParseFloat(s, 10, 256, big.ToNearestEven)
	return f, err == nil
}

// stripMeta is _strip_storage_meta: keys starting with "_" are storage bookkeeping.
func stripMeta(r Object) Object {
	return r.Without(func(k string) bool { return strings.HasPrefix(k, "_") })
}

// requester is requesting_user_id as sent (JSON null and absence are both "no user").
func requester(p cdPayload) any { return p.m["requesting_user_id"] }

func (cd *CommandData) commands(_ context.Context, _ cdPayload) map[string]any {
	out := []map[string]any{}
	for _, c := range cd.all() {
		// Hidden like the Pi's discovery hides a command the household turned off.
		if c.Mode == "disabled" || !cd.Store.Enabled(c.Name) {
			continue
		}
		out = append(out, map[string]any{"command_name": c.Name, "mode": c.Mode, "storage_name": c.StorageName})
	}
	// No "ok" key: the Pi's commands answer has none.
	return map[string]any{"commands": out}
}

func (cd *CommandData) schema(_ context.Context, p cdPayload) map[string]any {
	name := p.str("command_name")
	if name == "" {
		return fail("command_name required")
	}
	c := cd.resolve(name)
	if c == nil {
		return fail("command not found")
	}
	return map[string]any{"ok": true, "mode": c.Mode, "supports_create": c.SupportsCreate, "fields": fields(c)}
}

func (cd *CommandData) list(ctx context.Context, p cdPayload) map[string]any {
	c := cd.resolve(p.str("command_name"))
	if c == nil {
		return fail("command not found")
	}
	who := requester(p)
	var keys []string
	var data []Object
	truncated := false
	for _, r := range cd.Store.Records(c.StorageName, cd.now()) {
		if len(data) >= ListCap {
			truncated = true
			break
		}
		rec := stripMeta(r.Data)
		if !canSee(rec, who) {
			continue
		}
		keys, data = append(keys, r.Key), append(data, rec)
	}
	var sums []map[string]any
	if len(data) > 0 {
		var err error
		if sums, err = cd.Commands.Summaries(ctx, c.Name, data); err != nil {
			cd.Log.Warn("command-data: display_summary failed", "command", c.Name, "err", err)
		}
	}
	records := []map[string]any{}
	for i, rec := range data {
		var sum map[string]any
		if i < len(sums) {
			sum = sums[i]
		}
		if sum == nil {
			sum = map[string]any{"title": keys[i], "subtitle": nil, "icon": "alert-circle-outline"}
		}
		records = append(records, map[string]any{"key": keys[i], "summary": sum, "data": rec})
	}
	return map[string]any{"ok": true, "records": records, "truncated": truncated, "count": len(records)}
}

func (cd *CommandData) get(_ context.Context, p cdPayload) map[string]any {
	c := cd.resolve(p.str("command_name"))
	key := p.str("key")
	if c == nil || key == "" {
		return fail("command or key not found")
	}
	rec, ok := cd.Store.Data(c.StorageName, key, cd.now())
	if !ok || !canSee(rec, requester(p)) {
		return fail("record not found")
	}
	return map[string]any{"ok": true, "record": rec, "schema": schemaOf(c)}
}

// fieldNames are the FieldSpecs' names where keep says so.
func fieldNames(c *DataCommand, keep func(editable, createOnly bool) bool) map[string]bool {
	var specs []struct {
		Name       string `json:"name"`
		Editable   *bool  `json:"editable"`
		CreateOnly bool   `json:"create_only"`
	}
	_ = json.Unmarshal(fields(c), &specs)
	out := map[string]bool{}
	for _, f := range specs {
		// FieldSpec.to_dict leaves editable out when it is the default, true.
		if keep(f.Editable == nil || *f.Editable, f.CreateOnly) {
			out[f.Name] = true
		}
	}
	delete(out, "data_key") // the storage key is never writable
	return out
}

func (cd *CommandData) update(_ context.Context, p cdPayload) map[string]any {
	c := cd.resolve(p.str("command_name"))
	key := p.str("key")
	raw, isMap := p.object("patch")
	if c == nil || key == "" || !isMap {
		return fail("command, key, or patch missing")
	}
	if c.Mode == "readonly" {
		return fail("command is read-only")
	}
	writable := fieldNames(c, func(editable, _ bool) bool { return editable })
	patch := raw.Without(func(k string) bool { return !writable[k] })
	if patch.Len() == 0 {
		return fail("patch has no editable fields")
	}
	who := requester(p)
	cd.mu.Lock()
	defer cd.mu.Unlock()
	// Unlike the Pi's generic path, the record keeps its expiry.
	merged, found, err := cd.Store.UpdateData(c.StorageName, key, patch, cd.now(), func(r Object) bool { return canSee(r, who) })
	if err != nil {
		return handlerError(err)
	}
	if !found {
		return fail("record not found")
	}
	cd.Log.Info("command-data: record updated", "command", c.Name, "key", key, "fields", patch.Keys(), "user", who)
	return map[string]any{"ok": true, "record": merged}
}

// handlerError is the Pi's answer when a handler raises (a store write failed, the sidecar is
// gone): the error text goes back, so command-center answers rather than timing out.
func handlerError(err error) map[string]any { return fail("handler error: " + err.Error()) }

func (cd *CommandData) create(ctx context.Context, p cdPayload) map[string]any {
	c := cd.resolve(p.str("command_name"))
	raw, isMap := p.object("data")
	if c == nil || !isMap {
		return fail("command or values missing")
	}
	if c.Mode == "readonly" {
		return fail("command is read-only")
	}
	if !c.SupportsCreate {
		return fail("command does not support adding records")
	}
	creatable := fieldNames(c, func(editable, createOnly bool) bool { return editable || createOnly })
	values := raw.Without(func(k string) bool { return !creatable[k] })
	// The owner comes only from command-center (the JWT); the command stamps it and refuses
	// a call without one.
	var uid json.Number
	switch v := p.m["requesting_user_id"].(type) {
	case json.Number:
		uid = v
	case nil:
	default:
		return fail(fmt.Sprintf("bad requesting_user_id %v", v))
	}
	key, rec, err := cd.Commands.Create(ctx, c.Name, values, uid)
	var rej *ErrRejected
	if errors.As(err, &rej) {
		return fail(rej.Message)
	}
	if err != nil {
		return handlerError(err)
	}
	cd.Log.Info("command-data: record created", "command", c.Name, "key", key, "user", requester(p))
	var out any // the command may return any value; a dict loses its "_" keys
	if r, ok := asObject(rec); ok {
		out = stripMeta(r)
	} else if len(rec) > 0 {
		out = rec
	}
	return map[string]any{"ok": true, "record": out, "key": key}
}

func (cd *CommandData) delete(_ context.Context, p cdPayload) map[string]any {
	c := cd.resolve(p.str("command_name"))
	key := p.str("key")
	if c == nil || key == "" {
		return fail("command or key not found")
	}
	if c.Mode == "readonly" {
		return fail("command is read-only")
	}
	cd.mu.Lock()
	defer cd.mu.Unlock()
	rec, ok := cd.Store.Data(c.StorageName, key, cd.now())
	if !ok || !canSee(rec, requester(p)) {
		return fail("record not found")
	}
	deleted, err := cd.Store.DeleteData(c.StorageName, key)
	if err != nil {
		return handlerError(err)
	}
	if !deleted {
		return fail("delete failed")
	}
	cd.Log.Info("command-data: record deleted", "command", c.Name, "key", key, "user", requester(p))
	return map[string]any{"ok": true}
}
