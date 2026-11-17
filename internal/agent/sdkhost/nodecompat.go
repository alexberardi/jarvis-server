package sdkhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The calls behind the sidecar's Python-node compatibility modules (internal/agent/sidecar/
// nodecompat): the Python node's Config, RestClient and DirectDeviceService, which Pantry
// packages written for that node import. The node key stays here: Python gets the answers.

// nodeInfo is GET /api/v0/node/info, fetched once per server (CC).
type nodeInfo struct {
	mu   sync.Mutex
	base string
	info map[string]any
}

// node answers node.info: {node_id, room, household_id, command_center_url, home_context} as the
// Python node's Config had them. Without jarvisd's /node/info (an older server) the enrolled
// node id and room only.
func (h *Host) node(ctx context.Context, method string) (any, error) {
	if method != "node.info" {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	cc := h.cc.Load()
	if cc == nil {
		return nil, errNoCC
	}
	h.info.mu.Lock()
	defer h.info.mu.Unlock()
	if h.info.info == nil || h.info.base != cc.BaseURL {
		info := map[string]any{}
		if err := cc.Do(ctx, http.MethodGet, "/api/v0/node/info", nil, &info); err != nil {
			h.log().Warn("sdkhost: node info unavailable; the enrolled id and room only", "err", err)
			info = map[string]any{"node_id": cc.Creds.NodeID, "room": cc.Creds.Room}
		} else {
			h.info.base = cc.BaseURL
		}
		info["command_center_url"] = strings.TrimRight(cc.BaseURL, "/")
		if h.info.base == cc.BaseURL {
			h.info.info = info
		}
		return info, nil
	}
	return h.info.info, nil
}

// ccRequestParams are cc.request's: one node-authenticated call, as the Python node's RestClient.
type ccRequestParams struct {
	Method  string          `json:"method"`
	Path    string          `json:"path"`
	JSON    json.RawMessage `json:"json"`
	Timeout float64         `json:"timeout"`
}

// ccRequest is cc.request: GET, POST or PUT to an /api/v0/ path of command-center, as the node.
// It answers {"status", "body"}: RestClient turns a non-2xx into None as on the node. jarvisd
// decides what a node key may do; the path is only kept to the API (no "..", no other host).
func (h *Host) ccRequest(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ccRequestParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	method := strings.ToUpper(p.Method)
	if method != http.MethodGet && method != http.MethodPost && method != http.MethodPut {
		return nil, fmt.Errorf("cc.request: method %q not allowed", p.Method)
	}
	u, err := url.Parse(p.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Fragment != "" {
		return nil, fmt.Errorf("cc.request: %q is not a command-center path", p.Path)
	}
	clean := path.Clean("/" + strings.TrimLeft(u.Path, "/"))
	if !strings.HasPrefix(clean, "/api/v0/") {
		return nil, fmt.Errorf("cc.request is limited to /api/v0/ paths, not %q", p.Path)
	}
	if u.RawQuery != "" {
		clean += "?" + u.RawQuery
	}
	cc := h.cc.Load()
	if cc == nil {
		return nil, errNoCC
	}
	if p.Timeout > 0 {
		cc = cc.WithTimeout(time.Duration(p.Timeout * float64(time.Second)))
	}
	var body any
	if len(p.JSON) > 0 && method != http.MethodGet {
		body = p.JSON
	}
	var out any
	if err := cc.Do(ctx, method, clean, body, &out); err != nil {
		var he *node.HTTPError
		if errors.As(err, &he) {
			return map[string]any{"status": he.Status, "body": nil}, nil
		}
		return nil, err
	}
	return map[string]any{"status": http.StatusOK, "body": out}, nil
}

// devices answers devices.list: the household's direct devices as this node caches them
// (devices.Registry, GET /api/v0/node/devices), for DirectDeviceService.
func (h *Host) devices(method string) (any, error) {
	if method != "devices.list" {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	if h.DirectDevices == nil {
		return []any{}, nil
	}
	return h.DirectDevices(), nil
}
