package cc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/nodeconfig"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

var nodeConfigWait = 8 * time.Second

// Plain configuration never touches the encrypted settings-request store. The
// server constructs both the schema and response from its own scalar allowlist.
func (m *Module) handlePlainNodeConfig(w http.ResponseWriter, r *http.Request, u authn.User) {
	n, err := m.requireNodeAccess(r.Context(), u, r.PathValue("node_id"), authn.RoleMember, "Not authorized")
	if err != nil {
		m.writeErr(w, err)
		return
	}
	profile := "pi"
	if n.nodeKind.String == "desktop" {
		profile = "desktop"
	} else if n.runtime.String != "go" {
		profile = "legacy"
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("schema") == "1" {
		httpx.WriteJSON(w, 200, nodeconfig.View{Values: map[string]any{}, Fields: nodeconfig.Fields(profile)})
		return
	}
	verb := "get_node_config"
	details := map[string]any{}
	if r.Method != http.MethodGet {
		var body struct {
			Settings map[string]any `json:"settings"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || dec.Decode(&struct{}{}) != io.EOF {
			detail(w, 400, "Invalid settings request")
			return
		}
		if err := nodeconfig.Validate(body.Settings, nodeconfig.Fields(profile)); err != nil {
			detail(w, 400, err.Error())
			return
		}
		verb = "update_node_config"
		details = map[string]any{"settings": body.Settings, "restart": false, "confirm": true}
		// Compatibility only: an explicit POST is sent without claiming readback or
		// confirmation. It still passes the same server-side allowlist.
		if r.Method == http.MethodPost {
			if !m.bus.Available() {
				detail(w, 503, "Node connection unavailable")
				return
			}
			if up, known := m.bus.NodeSession(n.nodeID); known && !up {
				detail(w, 503, "Node is offline")
				return
			}
			delete(details, "confirm")
			rid := m.bus.Command(n.nodeID, verb, details)
			httpx.WriteJSON(w, 200, map[string]any{"status": "sent", "request_id": rid})
			return
		}
	}
	if !m.bus.Available() {
		detail(w, 503, "Node connection unavailable")
		return
	}
	if up, known := m.bus.NodeSession(n.nodeID); known && !up {
		detail(w, 503, "Node is offline")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nodeConfigWait)
	defer cancel()
	_, raw, err := m.bus.CommandAwait(ctx, n.nodeID, verb, details, "")
	if err != nil {
		detail(w, 504, "Node did not confirm. It may be offline or need an update. Reload before retrying a save.")
		return
	}
	var result struct {
		Output struct {
			Success  bool           `json:"success"`
			Values   map[string]any `json:"values"`
			ReadOnly []string       `json:"read_only"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Output.Success {
		// Never relay arbitrary node error strings, which may contain secret values.
		detail(w, 502, "Node could not complete the settings request. Reload to check its saved values.")
		return
	}
	v := nodeconfig.ViewOf(result.Output.Values, profile)
	for _, f := range v.Fields {
		for _, k := range result.Output.ReadOnly {
			if k == f.Key {
				v.ReadOnly = append(v.ReadOnly, k)
				break
			}
		}
	}
	httpx.WriteJSON(w, 200, v)
}
