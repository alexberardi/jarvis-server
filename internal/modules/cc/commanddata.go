package cc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The mobile command-data browser (doc 12 §3.4): browse and edit the records a command keeps
// on a node through JarvisStorage. Every route is a synchronous MQTT round trip on
// jarvis/nodes/{id}/command-data/{op}, answered on …/response/{correlation_id} (the Bus's
// request/response, which registers the waiter before publishing). The node enforces
// visibility and ownership; CC is the only source of requesting_user_id (from the JWT).
//
// Node payloads (FieldSpecs, records) are opaque: they are decoded with UseNumber into
// generic maps and passed through; the only mutation is the {field}_display injection.

// Timeouts and TTLs (§5; vars for tests).
var (
	cmdDataTimeout = 10 * time.Second
	schemaCacheTTL = 600 * time.Second
)

// --- schema cache (per (node, command), in process; invalidated on package changes, Q10) ---

type schemaKey struct{ node, command string }

type schemaEntry struct {
	value map[string]any
	at    time.Time
}

type schemaCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[schemaKey]schemaEntry
}

func newSchemaCache(now func() time.Time) *schemaCache {
	return &schemaCache{now: now, m: map[schemaKey]schemaEntry{}}
}

func (c *schemaCache) get(node, command string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := schemaKey{node, command}
	e, ok := c.m[k]
	if !ok {
		return nil
	}
	if c.now().Sub(e.at) > schemaCacheTTL {
		delete(c.m, k)
		return nil
	}
	return e.value
}

func (c *schemaCache) put(node, command string, v map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[schemaKey{node, command}] = schemaEntry{value: v, at: c.now()}
}

// invalidateNode drops every cached schema for a node (an install, uninstall or revert
// finished, so its commands and FieldSpecs may have changed).
func (c *schemaCache) invalidateNode(node string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.node == node {
			delete(c.m, k)
		}
	}
}

// --- auth scoping ---

// cdNode is _resolve_node_in_household: 404 for an unknown node, else the caller must be a
// member of its household among all of their memberships (D5). A node with no household
// fails closed (D40 12.Q5; legacy let any JWT user in).
func (m *Module) cdNode(ctx context.Context, u authn.User, nodeID string) error {
	n, err := m.nodeByID(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return fail(http.StatusNotFound, fmt.Sprintf("Node %s not found", nodeID))
	}
	if err != nil {
		return err
	}
	if !n.householdID.Valid || n.householdID.String == "" {
		return fail(http.StatusForbidden, "Not authorized")
	}
	return m.requireRole(ctx, u.ID, n.householdID.String, authn.RoleMember)
}

// --- MQTT round trip ---

// cdRequest is _mqtt_request: 503 without a broker, 504 on timeout, 502 for a non-JSON (or
// non-object) answer. The Bus adds the correlation id and records proof of life.
func (m *Module) cdRequest(ctx context.Context, nodeID, op string, payload map[string]any) (map[string]any, error) {
	if !m.bus.Available() {
		return nil, fail(http.StatusServiceUnavailable, "MQTT client not available")
	}
	rctx, cancel := context.WithTimeout(ctx, cmdDataTimeout)
	defer cancel()
	raw, err := m.bus.Request(rctx, nodeID, "command-data/"+op, payload)
	switch {
	case errors.Is(err, ErrNoResult):
		return nil, fail(http.StatusGatewayTimeout, fmt.Sprintf("Node %s did not respond within %ss", nodeID, pySeconds(cmdDataTimeout)))
	case errors.Is(err, ErrNoBroker):
		return nil, fail(http.StatusServiceUnavailable, "MQTT client not available")
	case err != nil:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		m.deps.Log.Warn("cc: command-data request failed", "node", nodeID, "op", op, "err", err)
		return nil, fail(http.StatusServiceUnavailable, "MQTT client not available")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		m.deps.Log.Error("cc: node returned non-JSON command-data response", "node", nodeID, "op", op)
		return nil, fail(http.StatusBadGateway, "Node returned invalid response")
	}
	return out, nil
}

// pySeconds renders a duration like Python's float str (10.0).
func pySeconds(d time.Duration) string {
	s := strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// truthy is Python truthiness for a decoded JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
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

// nodeError is response.get("error", {}).get("message", def).
func nodeError(resp map[string]any, def string) string {
	if e, ok := resp["error"].(map[string]any); ok {
		if msg, ok := e["message"].(string); ok {
			return msg
		}
	}
	return def
}

func getOr(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// errInvalidNode: a node answered ok:true without the keys the contract requires (D8: legacy
// raised a 500 on the KeyError).
var errInvalidNode = fail(http.StatusBadGateway, "Node returned invalid response")

// schemaFrom normalises a node schema response into the cached {mode, supports_create, fields}.
func schemaFrom(resp map[string]any) (map[string]any, bool) {
	mode, okMode := resp["mode"]
	fields, okFields := resp["fields"]
	if !okMode || !okFields {
		return nil, false
	}
	return map[string]any{"mode": mode, "supports_create": getOr(resp, "supports_create", false), "fields": fields}, true
}

// --- user_ref enrichment ---

// userRefFields walks FieldSpecs (recursively through nested `fields`) for type user_ref.
func userRefFields(fields any) []string {
	l, _ := fields.([]any)
	var names []string
	for _, f := range l {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if fm["type"] == "user_ref" {
			if n, ok := fm["name"].(string); ok {
				names = append(names, n)
			}
		}
		if nested, ok := fm["fields"].([]any); ok {
			names = append(names, userRefFields(nested)...)
		}
	}
	return names
}

// jsonInt is Python's isinstance(v, int) for a decoded JSON number (integral literal only).
func jsonInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return i, err == nil
}

// recordData is where a record's field values live: record["data"] when it is a dict (list
// rows), else the record itself.
func recordData(r map[string]any) map[string]any {
	if d, ok := r["data"].(map[string]any); ok {
		return d
	}
	return r
}

// enrichRecords writes "{field}_display" next to each user_ref id, resolving names from the
// auth module in process (no batch HTTP call, no name cache). Every failure degrades to no
// _display key.
func (m *Module) enrichRecords(ctx context.Context, records []map[string]any, fields any) {
	names := userRefFields(fields)
	if len(names) == 0 || m.Names == nil {
		return
	}
	seen := map[int64]bool{}
	var ids []int64
	for _, r := range records {
		d := recordData(r)
		for _, n := range names {
			if id, ok := jsonInt(d[n]); ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	resolved, err := m.Names.UserNames(ctx, ids)
	if err != nil {
		m.deps.Log.Warn("cc: user_ref names unavailable", "err", err)
		return
	}
	for _, r := range records {
		d := recordData(r)
		for _, n := range names {
			if id, ok := jsonInt(d[n]); ok {
				if name, ok := resolved[id]; ok && name != "" {
					d[n+"_display"] = name
				}
			}
		}
	}
}

// enrichRecord enriches one record whose own keys are the field values (get / create).
func (m *Module) enrichRecord(ctx context.Context, record any, fields any) any {
	if r, ok := record.(map[string]any); ok {
		m.enrichRecords(ctx, []map[string]any{{"data": r}}, fields)
	}
	return record
}

// --- handlers ---

// cdPrelude resolves the node for the caller; it writes the error and returns false.
func (m *Module) cdPrelude(w http.ResponseWriter, r *http.Request, u authn.User) (string, bool) {
	nodeID := r.PathValue("node_id")
	if err := m.cdNode(r.Context(), u, nodeID); err != nil {
		m.writeErr(w, err)
		return "", false
	}
	return nodeID, true
}

// handleCDListNodes lists the active nodes in households the caller belongs to (nodes with
// no household are hidden, as legacy).
func (m *Module) handleCDListNodes(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT node_id, household_id, room FROM cc_nodes
		WHERE is_active = 1 AND household_id IS NOT NULL AND household_id != '' ORDER BY rowid`)
	if err != nil {
		m.internalError(w, err)
		return
	}
	type row struct{ id, hh, room string }
	var all []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.hh, &x.room); err != nil {
			rows.Close()
			m.internalError(w, err)
			return
		}
		all = append(all, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	member := map[string]bool{}
	visible := []map[string]any{}
	for _, x := range all {
		ok, checked := member[x.hh]
		if !checked {
			_, isMember, err := m.Auth.HouseholdRole(ctx, u.ID, x.hh)
			if err != nil {
				m.internalError(w, err)
				return
			}
			ok = isMember
			member[x.hh] = ok
		}
		if ok {
			visible = append(visible, map[string]any{"node_id": x.id, "household_id": x.hh, "room": x.room})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"nodes": visible})
}

func (m *Module) handleCDCommands(w http.ResponseWriter, r *http.Request, u authn.User) {
	nodeID, ok := m.cdPrelude(w, r, u)
	if !ok {
		return
	}
	resp, err := m.cdRequest(r.Context(), nodeID, "commands", map[string]any{})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"commands": getOr(resp, "commands", []any{})})
}

// fetchSchema returns the cached schema or asks the node (ok:false → 404 with its message).
func (m *Module) fetchSchema(ctx context.Context, nodeID, cmd string) (map[string]any, error) {
	if s := m.cmdData.get(nodeID, cmd); s != nil {
		return s, nil
	}
	resp, err := m.cdRequest(ctx, nodeID, "schema", map[string]any{"command_name": cmd})
	if err != nil {
		return nil, err
	}
	if !truthy(resp["ok"]) {
		return nil, fail(http.StatusNotFound, nodeError(resp, "command not found"))
	}
	s, ok := schemaFrom(resp)
	if !ok {
		return nil, errInvalidNode
	}
	m.cmdData.put(nodeID, cmd, s)
	return s, nil
}

func (m *Module) handleCDSchema(w http.ResponseWriter, r *http.Request, u authn.User) {
	nodeID, ok := m.cdPrelude(w, r, u)
	if !ok {
		return
	}
	s, err := m.fetchSchema(r.Context(), nodeID, r.PathValue("command_name"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

func (m *Module) handleCDList(w http.ResponseWriter, r *http.Request, u authn.User) {
	nodeID, ok := m.cdPrelude(w, r, u)
	if !ok {
		return
	}
	ctx := r.Context()
	cmd := r.PathValue("command_name")
	resp, err := m.cdRequest(ctx, nodeID, "list", map[string]any{"command_name": cmd, "requesting_user_id": u.ID})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !truthy(resp["ok"]) {
		detail(w, http.StatusNotFound, nodeError(resp, "command not found"))
		return
	}
	// The schema says which fields are user_refs: from the cache, else the node. A node error
	// on that second round trip fails the request, as legacy; a schema without its keys just
	// skips enrichment (D8).
	schema := m.cmdData.get(nodeID, cmd)
	if schema == nil {
		sresp, err := m.cdRequest(ctx, nodeID, "schema", map[string]any{"command_name": cmd})
		if err != nil {
			m.writeErr(w, err)
			return
		}
		if truthy(sresp["ok"]) {
			if s, ok := schemaFrom(sresp); ok {
				schema = s
				m.cmdData.put(nodeID, cmd, s)
			}
		}
	}
	records := getOr(resp, "records", []any{})
	l, isList := records.([]any)
	if schema != nil && isList {
		var recs []map[string]any
		for _, x := range l {
			if rm, ok := x.(map[string]any); ok {
				recs = append(recs, rm)
			}
		}
		m.enrichRecords(ctx, recs, schema["fields"])
	}
	count, has := resp["count"]
	if !has {
		count = len(l)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"records": records, "truncated": truthy(resp["truncated"]), "count": count})
}

func (m *Module) handleCDGet(w http.ResponseWriter, r *http.Request, u authn.User) {
	nodeID, ok := m.cdPrelude(w, r, u)
	if !ok {
		return
	}
	ctx := r.Context()
	cmd := r.PathValue("command_name")
	resp, err := m.cdRequest(ctx, nodeID, "get", map[string]any{
		"command_name": cmd, "key": r.PathValue("key"), "requesting_user_id": u.ID,
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !truthy(resp["ok"]) {
		detail(w, http.StatusNotFound, nodeError(resp, "record not found"))
		return
	}
	record, has := resp["record"]
	if !has {
		m.writeErr(w, errInvalidNode)
		return
	}
	schema, isObj := resp["schema"].(map[string]any)
	if !isObj {
		schema = map[string]any{"mode": "enabled", "fields": []any{}}
	}
	fields := getOr(schema, "fields", []any{})
	m.cmdData.put(nodeID, cmd, map[string]any{
		"mode": getOr(schema, "mode", "enabled"), "supports_create": getOr(schema, "supports_create", false), "fields": fields,
	})
	record = m.enrichRecord(ctx, record, fields)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"record": record, "schema": schema})
}

// cdBodyObject reads the single dict field of a create/patch body ({} when absent).
func cdBodyObject(w http.ResponseWriter, r *http.Request, name string) (map[string]any, bool) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return nil, false
	}
	v, present := b.m[name]
	obj := map[string]any{}
	if present {
		o, isObj := v.(map[string]any)
		if !isObj {
			b.fail(name, "Input should be a valid dictionary")
		} else {
			obj = o
		}
	}
	if !b.done(w) {
		return nil, false
	}
	return obj, true
}

// Create: the client sends only field values; the node mints the key and stamps the owner from
// requesting_user_id (never client-asserted).
func (m *Module) handleCDCreate(w http.ResponseWriter, r *http.Request, u authn.User) {
	data, ok := cdBodyObject(w, r, "data")
	if !ok {
		return
	}
	nodeID, ok := m.cdPrelude(w, r, u)
	if !ok {
		return
	}
	ctx := r.Context()
	cmd := r.PathValue("command_name")
	resp, err := m.cdRequest(ctx, nodeID, "create", map[string]any{"command_name": cmd, "data": data, "requesting_user_id": u.ID})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !truthy(resp["ok"]) {
		msg := nodeError(resp, "create failed")
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "read-only"):
			detail(w, http.StatusForbidden, msg)
		case strings.Contains(low, "not found"), strings.Contains(low, "missing"), strings.Contains(low, "does not support"):
			detail(w, http.StatusNotFound, msg)
		default:
			detail(w, http.StatusBadRequest, msg)
		}
		return
	}
	record := resp["record"]
	if s := m.cmdData.get(nodeID, cmd); record != nil && s != nil {
		record = m.enrichRecord(ctx, record, s["fields"])
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"record": record, "key": resp["key"]})
}

// Update is dict-overlay; the node filters to editable fields. Not enriched (legacy).
func (m *Module) handleCDUpdate(w http.ResponseWriter, r *http.Request, u authn.User) {
	patch, ok := cdBodyObject(w, r, "patch")
	if !ok {
		return
	}
	nodeID, ok := m.cdPrelude(w, r, u)
	if !ok {
		return
	}
	resp, err := m.cdRequest(r.Context(), nodeID, "update", map[string]any{
		"command_name": r.PathValue("command_name"), "key": r.PathValue("key"), "patch": patch, "requesting_user_id": u.ID,
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !truthy(resp["ok"]) {
		msg := nodeError(resp, "update failed")
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "not found"), strings.Contains(low, "no editable"):
			detail(w, http.StatusNotFound, msg)
		case strings.Contains(low, "read-only"):
			detail(w, http.StatusForbidden, msg)
		default:
			detail(w, http.StatusBadRequest, msg)
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"record": resp["record"]})
}

func (m *Module) handleCDDelete(w http.ResponseWriter, r *http.Request, u authn.User) {
	nodeID, ok := m.cdPrelude(w, r, u)
	if !ok {
		return
	}
	resp, err := m.cdRequest(r.Context(), nodeID, "delete", map[string]any{
		"command_name": r.PathValue("command_name"), "key": r.PathValue("key"), "requesting_user_id": u.ID,
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !truthy(resp["ok"]) {
		msg := nodeError(resp, "delete failed")
		if strings.Contains(strings.ToLower(msg), "read-only") {
			detail(w, http.StatusForbidden, msg)
		} else {
			detail(w, http.StatusNotFound, msg)
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
