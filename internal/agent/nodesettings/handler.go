package nodesettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// MQTT sub-topics under jarvis/nodes/{id}/ that this package answers. Each message is a nudge
// carrying ids only; the node fetches the material over its authenticated HTTP channel.
const (
	TopicK2Provision     = "k2/provision"
	TopicSettingsRequest = "settings/request"
	TopicConfigPush      = "config/push"
)

// API is command-center as the node sees it (node.CC.Do: X-API-Key on every call).
type API interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Handler answers the K2 relay, settings requests and config pushes for one node.
type Handler struct {
	NodeID string
	API    API
	Store  *Store
	// Commands lists what the snapshot shows, enabled or not.
	Commands func(ctx context.Context) []Command
	// Devices, if set, are the snapshot's device families and managers (a Pi's sidecar).
	Devices func(ctx context.Context) Devices
	// NodeConfig, if set, is the snapshot's node_config (a Pi's hardware and voice settings);
	// nil reports a computer's (DesktopNodeConfig).
	NodeConfig func() map[string]any
	Log        *slog.Logger
	// KeyChanged, if set, runs after K2 is stored (the heartbeat then says needs_k2: false).
	KeyChanged func()
	// SecretsChanged, if set, runs after a secrets push is applied (a Pi's device families
	// read their secrets to say whether they are configured).
	SecretsChanged func()

	pushMu sync.Mutex // one config-push pass at a time (a nudge and the backstop can overlap)
}

func (h *Handler) path(format string, a ...any) string {
	esc := make([]any, len(a))
	for i, v := range a {
		esc[i] = url.PathEscape(fmt.Sprint(v))
	}
	return "/api/v0/nodes/" + url.PathEscape(h.NodeID) + fmt.Sprintf(format, esc...)
}

func ids(payload []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(payload, &m)
	return m
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// Handle routes one MQTT message by its sub-topic. It reports whether the topic is one of
// this package's.
func (h *Handler) Handle(ctx context.Context, sub string, payload []byte) bool {
	switch sub {
	case TopicK2Provision:
		h.K2Provision(ctx, payload)
	case TopicSettingsRequest:
		h.SettingsRequest(ctx, payload)
	case TopicConfigPush:
		h.ConfigPush(ctx)
	case TopicPackageSecrets:
		h.PackageSecrets(ctx)
	default:
		return false
	}
	return true
}

// --- K2 relay (docs/cc/05-nodes.md §3.3) ---

// K2Provision is _handle_k2_provision. The nudge carries only a request id and is untrusted:
// the key is fetched from command-center over the node's authenticated channel, so a spoofed
// broker message can't replace K2. The ack ends the waiting POST /nodes/{id}/k2 call.
func (h *Handler) K2Provision(ctx context.Context, payload []byte) {
	rid := str(ids(payload), "request_id")
	if rid == "" {
		h.Log.Warn("k2: nudge without request_id")
		return
	}
	var k struct {
		K2        string `json:"k2"`
		Kid       string `json:"kid"`
		CreatedAt string `json:"created_at"`
	}
	if err := h.API.Do(ctx, http.MethodGet, h.path("/k2/provision/%s", rid), nil, &k); err != nil || k.K2 == "" {
		h.Log.Warn("k2: provision verification failed", "err", err)
		h.ackK2(ctx, rid, errors.New("verification failed"))
		return
	}
	if k.Kid == "" {
		h.ackK2(ctx, rid, errors.New("incomplete key data"))
		return
	}
	if err := h.Store.SaveK2(k.K2, k.Kid, k.CreatedAt); err != nil {
		h.Log.Error("k2: couldn't store the key", "err", err)
		h.ackK2(ctx, rid, err)
		return
	}
	h.Log.Info("k2: settings key provisioned", "kid", k.Kid)
	h.ackK2(ctx, rid, nil)
	if h.KeyChanged != nil {
		h.KeyChanged()
	}
}

func (h *Handler) ackK2(ctx context.Context, rid string, failure error) {
	body := map[string]any{"success": failure == nil}
	if failure != nil {
		body["error"] = failure.Error()
	}
	if err := h.API.Do(ctx, http.MethodPost, h.path("/k2/ack/%s", rid), body, nil); err != nil {
		h.Log.Warn("k2: ack failed", "err", err)
	}
}

// --- settings snapshot (§3.2) ---

func optUser(v any) *int64 {
	switch n := v.(type) {
	case float64:
		u := int64(n)
		return &u
	case json.Number:
		if u, err := n.Int64(); err == nil {
			return &u
		}
	}
	return nil
}

// SettingsRequest is _handle_settings_request_notification + handle_snapshot_request.
func (h *Handler) SettingsRequest(ctx context.Context, payload []byte) {
	m := ids(payload)
	rid := str(m, "request_id")
	if rid == "" {
		h.Log.Warn("settings: request without request_id")
		return
	}
	include, _ := m["include_values"].(bool)
	h.snapshot(ctx, rid, include, optUser(m["user_id"]))
}

func (h *Handler) snapshot(ctx context.Context, rid string, include bool, uid *int64) {
	key := h.Store.Key()
	if key == nil {
		// As the Python node: no K2, no snapshot. The app shows "Generate Encryption Key".
		h.Log.Warn("settings: snapshot requested but no K2 is provisioned")
		return
	}
	// Confirm the request is real and live (404/410 otherwise).
	if err := h.API.Do(ctx, http.MethodGet, h.path("/settings/requests/%s", rid), nil, nil); err != nil {
		h.Log.Warn("settings: request confirmation failed", "err", err)
		return
	}
	var cmds []Command
	if h.Commands != nil {
		cmds = h.Commands(ctx)
	}
	var dev Devices
	if h.Devices != nil {
		dev = h.Devices(ctx)
	}
	plain, err := json.Marshal(h.Store.Snapshot(cmds, dev, h.nodeConfig(), include, uid))
	if err != nil {
		h.Log.Error("settings: snapshot", "err", err)
		return
	}
	sealed, err := Seal(key, h.NodeID, SnapshotConfigType, plain, nil)
	if err != nil {
		h.Log.Error("settings: snapshot encryption failed", "err", err)
		return
	}
	body := map[string]any{
		"ciphertext": sealed.Ciphertext, "nonce": sealed.Nonce, "tag": sealed.Tag,
		"aad_schema_version": SchemaVersion, "aad_commands_schema_version": CommandsSchemaVersion, "aad_revision": 1,
	}
	if err := h.API.Do(ctx, http.MethodPut, h.path("/settings/requests/%s/snapshot", rid), body, nil); err != nil {
		h.Log.Warn("settings: snapshot upload failed", "err", err)
		return
	}
	h.Log.Info("settings: snapshot uploaded", "commands", len(cmds), "include_values", include)
}

// --- config push (doc 07; config_push_service.py) ---

type pending struct {
	ID         string `json:"id"`
	PushID     string `json:"push_id"`
	ConfigType string `json:"config_type"`
	Sealed
}

// ConfigPush is process_pending_configs: fetch what waits, decrypt, apply, ack. A push that
// fails to decrypt or apply is not acked, as on the Pi (it stays pending).
func (h *Handler) ConfigPush(ctx context.Context) int {
	h.pushMu.Lock()
	defer h.pushMu.Unlock()
	var items []pending
	if err := h.API.Do(ctx, http.MethodGet, h.path("/config/pending"), nil, &items); err != nil {
		h.Log.Warn("config: pending fetch failed", "err", err)
		return 0
	}
	if len(items) == 0 {
		return 0
	}
	key := h.Store.Key()
	n := 0
	for _, it := range items {
		id := it.ID
		if id == "" {
			id = it.PushID
		}
		if key == nil {
			h.Log.Warn("config: push waiting but no K2 is provisioned", "config_type", it.ConfigType)
			continue
		}
		pt, err := Open(key, h.NodeID, it.ConfigType, it.Sealed)
		if err != nil {
			h.Log.Error("config: push not applied", "config_type", it.ConfigType, "err", err)
			continue
		}
		dec := json.NewDecoder(strings.NewReader(string(pt)))
		dec.UseNumber()
		var data map[string]any
		if err := dec.Decode(&data); err != nil || data == nil {
			h.Log.Error("config: push is not a JSON object", "config_type", it.ConfigType)
			continue
		}
		if err := h.apply(it.ConfigType, data); err != nil {
			h.Log.Error("config: push not applied", "config_type", it.ConfigType, "err", err)
			continue
		}
		if err := h.API.Do(ctx, http.MethodPost, h.path("/config/%s/ack", id), map[string]any{}, nil); err != nil {
			h.Log.Warn("config: ack failed", "err", err)
		}
		n++
		h.Log.Info("config: push applied", "config_type", it.ConfigType)
	}
	return n
}

// text is Python's str(value) for a decoded JSON scalar (the node stores str(value)).
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case nil:
		return "None"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// apply is _dispatch_config for what a desktop agent has. Types it has nothing for are applied
// as a no-op (and acked), with a log line, rather than left pending forever.
func (h *Handler) apply(configType string, data map[string]any) error {
	switch {
	case configType == "command_registry":
		name := text(data["command_name"])
		if data["command_name"] == nil || name == "" {
			h.Log.Warn("config: command_registry push without command_name")
			return nil
		}
		on := true
		if v, ok := data["enabled"]; ok {
			on = truthy(text(v))
		}
		return h.Store.SetEnabled(name, on)
	case configType == "fast_path_registry":
		// One pattern of a command's fast paths on or off (the app's Inspect fast-paths screen).
		name, id := text(data["command_name"]), text(data["pattern_id"])
		if data["command_name"] == nil || name == "" || data["pattern_id"] == nil || id == "" {
			h.Log.Warn("config: fast_path_registry push without command_name or pattern_id")
			return nil
		}
		on := true
		if v, ok := data["enabled"]; ok {
			on = truthy(text(v))
		}
		h.Log.Info("config: fast path switched", "command", name, "pattern", id, "enabled", on)
		return h.Store.SetFastPathEnabled(name, id, on)
	case configType == "node_config", configType == "agent_registry",
		strings.HasPrefix(configType, "auth:"):
		// node_config is the Pi's update/egress consent (the agent updates itself differently);
		// the agent runs no background agents; auth:* is a package's OAuth
		// store_auth_values, which the sidecar doesn't host yet.
		h.Log.Info("config: push type not used on a desktop agent; acknowledged", "config_type", configType)
		return nil
	}
	if err := h.applySecrets(data); err != nil {
		return err
	}
	if h.SecretsChanged != nil {
		h.SecretsChanged()
	}
	return nil
}

// applySecrets is _dispatch_secrets: with __user_id__ every value is that user's personal
// secret; otherwise each key keeps the scope it is stored under, else "integration".
func (h *Handler) applySecrets(data map[string]any) error {
	var uid *int64
	if v, ok := data["__user_id__"]; ok {
		delete(data, "__user_id__")
		if s := text(v); s != "" && s != "None" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return fmt.Errorf("bad __user_id__ %q", s)
			}
			uid = &n
		}
	}
	for k, v := range data {
		scope := "user"
		if uid == nil {
			if scope = h.Store.ScopeOf(k); scope == "" {
				scope = "integration"
			}
		}
		if err := h.Store.SetSecret(k, text(v), scope, "", uid); err != nil {
			return err
		}
	}
	return nil
}

// --- backstop (mqtt_tts_listener.py on_connect) ---

// Backstop runs on every MQTT (re)connect: settings requests and config pushes made while the
// node was away or before it had a session. Unlike the Python node it keeps include_values and
// the user from jarvisd's list (cc persists them, D40 05.Q8).
func (h *Handler) Backstop(ctx context.Context) {
	var reqs []struct {
		RequestID     string `json:"request_id"`
		IncludeValues bool   `json:"include_values"`
		UserID        any    `json:"user_id"`
	}
	if err := h.API.Do(ctx, http.MethodGet, h.path("/settings/requests"), nil, &reqs); err != nil {
		h.Log.Warn("settings: backstop poll failed", "err", err)
	}
	for _, r := range reqs {
		if r.RequestID != "" {
			h.snapshot(ctx, r.RequestID, r.IncludeValues, optUser(r.UserID))
		}
	}
	h.ConfigPush(ctx)
	h.PackageSecrets(ctx) // server-managed secrets changed while the node was away
}
