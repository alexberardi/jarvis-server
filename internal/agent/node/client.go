package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPError is a non-2xx answer from jarvisd. It carries the status and jarvisd's "detail",
// never request headers, so it is safe to print.
type HTTPError struct {
	Method, Path string
	Status       int
	Detail       string
}

func (e *HTTPError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.Status, e.Detail)
	}
	return fmt.Sprintf("%s %s: %d", e.Method, e.Path, e.Status)
}

// doJSON sends body (nil for none) and decodes a JSON answer into out (nil to discard).
func doJSON(ctx context.Context, hc *http.Client, method, rawURL string, hdr map[string]string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		he := &HTTPError{Method: method, Path: req.URL.Path, Status: resp.StatusCode}
		var d struct {
			Detail any `json:"detail"`
		}
		if json.Unmarshal(raw, &d) == nil && d.Detail != nil {
			he.Detail = fmt.Sprint(d.Detail)
		}
		return he
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func joinURL(base, path string) string { return strings.TrimRight(base, "/") + path }

// --- discovery ---

// Services is jarvisd's registry as this node should see it: name -> base URL.
type Services map[string]string

const (
	SvcCommandCenter = "jarvis-command-center"
	SvcAuth          = "jarvis-auth"
	SvcMQTT          = "jarvis-mqtt-broker"
)

// Discover reads GET {configURL}/services?style=remote&remote_host={host}: the LAN-side URLs,
// with jarvisd's "localhost" rows rewritten to the host the node reached config on. A service
// that isn't listed is an error at the call site, never a fabricated localhost.
func Discover(ctx context.Context, hc *http.Client, configURL string) (Services, error) {
	u, err := url.Parse(configURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad config URL %q", configURL)
	}
	q := url.Values{"style": {"remote"}, "remote_host": {u.Hostname()}}
	var resp struct {
		Services []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"services"`
	}
	if err := doJSON(ctx, hc, http.MethodGet, joinURL(configURL, "/services?"+q.Encode()), nil, nil, &resp); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	out := Services{}
	for _, s := range resp.Services {
		if s.Name != "" && s.URL != "" {
			out[s.Name] = s.URL
		}
	}
	return out, nil
}

// Need returns a service URL or an error naming it.
func (s Services) Need(name string) (string, error) {
	if u := s[name]; u != "" {
		return u, nil
	}
	return "", fmt.Errorf("discovery: jarvisd lists no %s", name)
}

// --- node-authenticated command-center client ---

// CC is command-center as a node sees it: every call carries X-API-Key node_id:node_key.
type CC struct {
	BaseURL string
	Creds   Creds
	HTTP    *http.Client
}

func NewCC(baseURL string, c Creds) *CC {
	return &CC{BaseURL: baseURL, Creds: c, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *CC) do(ctx context.Context, method, path string, body, out any) error {
	return doJSON(ctx, c.HTTP, method, joinURL(c.BaseURL, path), map[string]string{"X-API-Key": c.Creds.APIKey()}, body, out)
}

// Runtime is version_info.runtime in every heartbeat from this node core.
const Runtime = "go"

// MQTTCreds is the broker credential CC hands this node. Both empty: connect anonymously
// (jarvisd running without a broker).
type MQTTCreds struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c *CC) MQTTCredentials(ctx context.Context) (MQTTCreds, error) {
	var m struct {
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v0/node/mqtt-credentials", nil, &m); err != nil {
		return MQTTCreds{}, err
	}
	var out MQTTCreds
	if m.Username != nil {
		out.Username = *m.Username
	}
	if m.Password != nil {
		out.Password = *m.Password
	}
	return out, nil
}

// Heartbeat stamps last_seen and reports what this node is. version_info.install_mode is how
// the app can tell a desktop agent from a Pi, and jarvisd only offers remote updates to
// "tarball" installs (cc/updates.go), so the desktop agent must not claim it.
// version_info.runtime tells a Go node from the Python one (go-pi-node.md §8 Q6).
func (c *CC) Heartbeat(ctx context.Context, version, installMode string) error {
	body := map[string]any{
		"version_info": map[string]any{"version": version, "install_mode": installMode, "runtime": Runtime},
		"is_busy":      false,
		// K2 encrypts command secrets on a Pi; nothing on this node uses it yet, so don't ask
		// the app to provision one.
		"needs_k2": false,
	}
	return c.do(ctx, http.MethodPost, "/api/v0/admin/nodes/heartbeat", body, nil)
}

// Verify asks CC whether it really issued request id rid to this node (single use).
func (c *CC) Verify(ctx context.Context, rid string) (bool, error) {
	var v struct {
		Valid bool `json:"valid"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v0/commands/"+url.PathEscape(rid)+"/verify", map[string]any{}, &v); err != nil {
		return false, err
	}
	return v.Valid, nil
}

// PostToolReport answers report_tools.
func (c *CC) PostToolReport(ctx context.Context, rid string, report any) error {
	return c.do(ctx, http.MethodPost, "/api/v0/mobile/node-tool-reports/"+url.PathEscape(rid), report, nil)
}

// PostToolResult answers tool_call: {"output": {...}} on the device-control result sink.
func (c *CC) PostToolResult(ctx context.Context, rid string, output map[string]any) error {
	return c.do(ctx, http.MethodPost, "/api/v0/device-control-results/"+url.PathEscape(rid), map[string]any{"output": output}, nil)
}
