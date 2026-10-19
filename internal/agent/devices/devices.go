// Package devices is the Go Pi node's side of the household's smart-home devices: the app's
// device scan, list and state requests, the device-cache verbs, OAuth's auth-ready, and the
// registry of direct devices. The device families themselves are Python (Pantry packages,
// jarvis_command_sdk.IJarvisDeviceProtocol) and run in the package sidecar; this package owns
// MQTT and the uploads, as the Python node's mqtt_tts_listener and device_*_handler do
// (docs/agent/go-pi-node.md §3.16).
package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// The topics under jarvis/nodes/{id}/ jarvisd publishes the app's device requests on.
const (
	TopicScan  = "device-scan"
	TopicList  = "device-list"
	TopicState = "device-state"
)

// Topics are all three.
var Topics = []string{TopicScan, TopicList, TopicState}

// The command verbs jarvisd sends when the household's devices change.
const (
	VerbInvalidate = "invalidate_device_cache"
	VerbRemoved    = "device_removed"
)

// AuthReadyTopic is where jarvisd says an OAuth flow finished, for every node; the payload
// names the node that started it.
const AuthReadyTopic = "jarvis/auth/+/ready"

// API is command-center as the node reaches it (node.CC).
type API interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Families is the sidecar's device side (sidecar.Live).
type Families interface {
	DeviceCall(method string, params map[string]any) (map[string]any, error)
	StoreAuth(provider string, values map[string]any, uid any) (string, error)
}

// Node answers the device requests for one node.
type Node struct {
	NodeID   string
	Families Families
	Registry *Registry
	Log      *slog.Logger
	// Changed, if set, runs once OAuth credentials are stored: a family that took them is now
	// configured, so the heartbeat's protocols go again.
	Changed func()

	mu   sync.Mutex
	seen []string // the last request ids run: a redelivered request runs once
}

const seenMax = 64

// take reports whether request rid is new, and remembers it.
func (n *Node) take(rid string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, s := range n.seen {
		if s == rid {
			return false
		}
	}
	n.seen = append(n.seen, rid)
	if len(n.seen) > seenMax {
		n.seen = n.seen[1:]
	}
	return true
}

func (n *Node) path(format string, a ...string) string {
	esc := make([]any, len(a))
	for i, v := range a {
		esc[i] = url.PathEscape(v)
	}
	return fmt.Sprintf(format, esc...)
}

// Handle answers one of Topics. It runs on the message's own goroutine (node.Runner), so a
// ten-second scan holds up nothing else.
func (n *Node) Handle(ctx context.Context, api API, topic string, payload []byte) {
	var req map[string]any
	if err := json.Unmarshal(payload, &req); err != nil {
		n.Log.Warn("devices: invalid JSON", "topic", topic)
		return
	}
	rid, _ := req["request_id"].(string)
	if rid == "" || len(rid) > 64 {
		n.Log.Warn("devices: request without a request_id", "topic", topic)
		return
	}
	if !n.take(rid) {
		return
	}
	var path string
	var body map[string]any
	switch topic {
	case TopicScan:
		n.Log.Info("devices: scan requested", "request_id", rid)
		path = n.path("/api/v0/nodes/%s/device-scan/%s/results", n.NodeID, rid)
		body = n.jobResult("device_scan", nil)
	case TopicList:
		manager, _ := req["manager_name"].(string)
		if manager == "" {
			manager = "jarvis_direct"
		}
		n.Log.Info("devices: list requested", "request_id", rid, "manager", manager)
		path = n.path("/api/v0/nodes/%s/device-list/%s/results", n.NodeID, rid)
		body = n.jobResult("device_list", map[string]any{"manager_name": manager})
	case TopicState:
		n.Log.Info("devices: state requested", "request_id", rid, "entity_id", req["entity_id"])
		path = n.path("/api/v0/device-state-results/%s", rid)
		res, err := n.Families.DeviceCall("device_state", map[string]any{"details": req})
		body = res
		if err != nil {
			body = map[string]any{"error": err.Error()}
		}
	default:
		return
	}
	if err := api.Do(ctx, http.MethodPost, path, body, nil); err != nil {
		n.Log.Warn("devices: result not uploaded", "topic", topic, "request_id", rid, "err", err)
	}
}

// jobResult is a scan's or list's upload: the sidecar's answer, or N:'s error shape.
func (n *Node) jobResult(method string, params map[string]any) map[string]any {
	res, err := n.Families.DeviceCall(method, params)
	if err == nil {
		if e, ok := res["error"].(string); ok && e != "" {
			err = fmt.Errorf("%s", e)
		}
	}
	if err != nil {
		n.Log.Warn("devices: "+method+" failed", "err", err)
		return map[string]any{"devices": []any{}, "error": err.Error()}
	}
	return res
}

// Verbs are the command verbs: invalidate_device_cache drops the registry; device_removed
// lets the device's family clean up (a HomeKit unpair, say) and then drops it.
func (n *Node) Verbs() map[string]func(context.Context, API, map[string]any) {
	return map[string]func(context.Context, API, map[string]any){
		VerbInvalidate: func(context.Context, API, map[string]any) {
			n.Registry.Invalidate()
			n.Log.Info("devices: cache invalidated by jarvisd")
		},
		VerbRemoved: func(_ context.Context, _ API, details map[string]any) {
			defer n.Registry.Invalidate()
			res, err := n.Families.DeviceCall("device_removed", map[string]any{"details": details})
			if err != nil || res["error"] != nil {
				n.Log.Warn("devices: the family's removal failed", "entity_id", details["entity_id"], "err", err, "error", res["error"])
				return
			}
			n.Log.Info("devices: device removed", "entity_id", details["entity_id"], "protocol", details["protocol"], "ran", res["ran"])
		},
	}
}

// providerRe is what a provider name may be in a URL path.
var providerRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// AuthReady handles jarvis/auth/{provider}/ready: when it is this node's, the credentials are
// pulled from jarvisd (once: it hands them out a single time) and stored by the command or
// family that declares the provider, as the user who ran the flow (N:'s
// _pull_auth_credentials).
func (n *Node) AuthReady(ctx context.Context, api API, topic string, payload []byte) {
	var msg struct {
		Provider string `json:"provider"`
		NodeID   string `json:"node_id"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		n.Log.Warn("devices: invalid auth-ready JSON")
		return
	}
	if msg.NodeID != n.NodeID {
		return
	}
	provider := msg.Provider
	if provider == "" {
		if parts := strings.Split(topic, "/"); len(parts) == 4 {
			provider = parts[2]
		}
	}
	if !providerRe.MatchString(provider) || provider == "." || provider == ".." {
		n.Log.Warn("devices: auth-ready with a bad provider", "provider", provider)
		return
	}
	n.Log.Info("devices: auth ready", "provider", provider)
	var raw json.RawMessage
	if err := api.Do(ctx, http.MethodGet, n.path("/api/v0/oauth/provider/%s/credentials", provider), nil, &raw); err != nil {
		n.Log.Warn("devices: auth credentials not pulled", "provider", provider, "err", err)
		return
	}
	var values map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // user_id stays an integer for the SDK's user scope
	if err := dec.Decode(&values); err != nil || values == nil {
		n.Log.Warn("devices: bad auth credentials", "provider", provider, "err", err)
		return
	}
	uid := values["user_id"]
	delete(values, "user_id")
	if v, ok := values["base_url"]; ok {
		if _, has := values["_base_url"]; !has {
			values["_base_url"] = v // the commands read _base_url
		}
	}
	who, err := n.Families.StoreAuth(provider, values, uid)
	switch {
	case err != nil:
		n.Log.Warn("devices: auth credentials not stored", "provider", provider, "err", err)
	case who == "":
		n.Log.Warn("devices: no command or device family uses this auth provider", "provider", provider)
	default:
		n.Log.Info("devices: auth credentials stored", "provider", provider, "by", who)
		if n.Changed != nil {
			n.Changed()
		}
	}
}
