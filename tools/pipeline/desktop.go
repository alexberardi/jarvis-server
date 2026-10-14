package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// A scenario's "node": "desktop" plays its turns as a desktop agent the sandbox's primary user
// signed in on (owner_user_id), instead of the seeded kitchen Pi. Its voice turns act as that
// owner with no voice match (decision 2026-10-14, desktop voice speaks as its owner). The node
// is made once per sandbox through the app's own flow (login, provisioning token, register,
// a heartbeat saying node_kind desktop) and its key kept in <sandbox>/desktop-node.json.

// DesktopSetup is what making the desktop node needs: the sandbox's auth and the primary
// user's login, and where to keep the node's key.
type DesktopSetup struct {
	AuthURL         string
	Email, Password string
	StateFile       string
}

type desktopState struct {
	NodeID string `json:"node_id"`
	Key    string `json:"node_key"`
}

// nodeFor returns the node a scenario plays as: "" the seeded node, "desktop" the primary's
// desktop agent.
func (d *Driver) nodeFor(ctx context.Context, kind string) (*Node, error) {
	switch kind {
	case "", "kitchen":
		return d.Node, nil
	case "desktop":
	default:
		return nil, fmt.Errorf(`node %q: want "" (the seeded node) or "desktop"`, kind)
	}
	if d.desktop != nil {
		return d.desktop, nil
	}
	if d.Desktop == nil || d.Desktop.AuthURL == "" || d.Desktop.Email == "" {
		return nil, errors.New(`node "desktop": the sandbox exports no SBX_AUTH / SBX_EMAIL (scripts/sandbox up again)`)
	}
	n := &Node{BaseURL: d.Node.BaseURL, HTTP: d.Node.HTTP}
	if b, err := os.ReadFile(d.Desktop.StateFile); err == nil {
		var st desktopState
		if json.Unmarshal(b, &st) == nil && st.NodeID != "" {
			n.ID, n.Key = st.NodeID, st.Key
			// A heartbeat both proves the key and (re)states the kind.
			if desktopHeartbeat(ctx, n) == nil {
				d.desktop = n
				return n, nil
			}
		}
	}
	if err := d.pairDesktop(ctx, n); err != nil {
		return nil, fmt.Errorf(`node "desktop": %w`, err)
	}
	d.desktop = n
	return n, nil
}

// pairDesktop does what the agent's first-run sign-in does: the member mints a provisioning
// token for their household and the agent redeems it, so the member is its owner.
func (d *Driver) pairDesktop(ctx context.Context, n *Node) error {
	var login struct {
		AccessToken string `json:"access_token"`
	}
	if err := postJSON(ctx, d.Desktop.AuthURL+"/auth/login", nil,
		map[string]string{"email": d.Desktop.Email, "password": d.Desktop.Password}, &login); err != nil {
		return err
	}
	bearer := map[string]string{"Authorization": "Bearer " + login.AccessToken}
	var households []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := getJSON(ctx, d.Desktop.AuthURL+"/households", bearer, &households); err != nil {
		return err
	}
	hh := ""
	for _, h := range households {
		if hh == "" || h.Role == "admin" {
			hh = h.ID
		}
		if h.Role == "admin" {
			break
		}
	}
	if hh == "" {
		return errors.New("the primary user is in no household")
	}
	var tok struct {
		Token  string `json:"token"`
		NodeID string `json:"node_id"`
	}
	b, err := json.Marshal(map[string]string{"household_id": hh, "room": "office", "name": "pipeline-desktop"})
	if err != nil {
		return err
	}
	rb, err := doWant(ctx, nil, n.BaseURL+"/api/v0/provisioning/token", "application/json", b, bearer, http.StatusOK, http.StatusCreated)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(rb, &tok); err != nil {
		return err
	}
	var reg struct {
		NodeKey string `json:"node_key"`
	}
	b, err = json.Marshal(map[string]string{"node_id": tok.NodeID, "provisioning_token": tok.Token, "room": "office"})
	if err != nil {
		return err
	}
	if rb, err = doWant(ctx, nil, n.BaseURL+"/api/v0/nodes/register", "application/json", b, nil, http.StatusOK, http.StatusCreated); err != nil {
		return err
	}
	if err := json.Unmarshal(rb, &reg); err != nil {
		return err
	}
	n.ID, n.Key = tok.NodeID, reg.NodeKey
	if err := desktopHeartbeat(ctx, n); err != nil {
		return err
	}
	b, _ = json.Marshal(desktopState{NodeID: n.ID, Key: n.Key})
	if err := os.MkdirAll(filepath.Dir(d.Desktop.StateFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(d.Desktop.StateFile, b, 0o600)
}

// desktopHeartbeat reports node_kind desktop, as jarvis-agent's heartbeat does.
func desktopHeartbeat(ctx context.Context, n *Node) error {
	return n.post(ctx, "/api/v0/admin/nodes/heartbeat", map[string]any{"node_kind": "desktop"}, nil)
}

// getJSON GETs a URL and decodes a 200's body into out.
func getJSON(ctx context.Context, url string, header map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d %s", req.URL.Path, resp.StatusCode, truncate(string(b), 500))
	}
	return json.Unmarshal(b, out)
}
