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
// signed in on (owner_user_id), instead of the seeded kitchen Pi ("owner": "removed": one whose
// owner has left the household). Its voice turns act as that
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

// nodeFor returns the node a scenario plays as: "" the seeded node, "desktop" a desktop
// agent. owner "" makes the primary its owner; "removed" pairs it as a throwaway member the
// primary then removes from the household, so it has no usable owner.
func (d *Driver) nodeFor(ctx context.Context, kind, owner string) (*Node, error) {
	switch kind {
	case "", "kitchen":
		if owner != "" {
			return nil, fmt.Errorf(`owner %q: only a "desktop" node has an owner`, owner)
		}
		return d.Node, nil
	case "desktop":
	default:
		return nil, fmt.Errorf(`node %q: want "" (the seeded node) or "desktop"`, kind)
	}
	state := ""
	switch owner {
	case "":
		state = "desktop-node.json"
	case "removed":
		state = "desktop-removed-owner-node.json"
	default:
		return nil, fmt.Errorf(`owner %q: want "" (the primary) or "removed"`, owner)
	}
	if n := d.desktop[owner]; n != nil {
		return n, nil
	}
	if d.Desktop == nil || d.Desktop.AuthURL == "" || d.Desktop.Email == "" {
		return nil, errors.New(`node "desktop": the sandbox exports no SBX_AUTH / SBX_EMAIL (scripts/sandbox up again)`)
	}
	statePath := filepath.Join(filepath.Dir(d.Desktop.StateFile), state)
	n := &Node{BaseURL: d.Node.BaseURL, HTTP: d.Node.HTTP}
	if b, err := os.ReadFile(statePath); err == nil {
		var st desktopState
		if json.Unmarshal(b, &st) == nil && st.NodeID != "" {
			n.ID, n.Key = st.NodeID, st.Key
			// A heartbeat both proves the key and (re)states the kind.
			if desktopHeartbeat(ctx, n) == nil {
				d.remember(owner, n)
				return n, nil
			}
		}
	}
	if err := d.pairDesktop(ctx, n, owner == "removed", statePath); err != nil {
		return nil, fmt.Errorf(`node "desktop": %w`, err)
	}
	d.remember(owner, n)
	return n, nil
}

func (d *Driver) remember(owner string, n *Node) {
	if d.desktop == nil {
		d.desktop = map[string]*Node{}
	}
	d.desktop[owner] = n
}

// pairDesktop does what the agent's first-run sign-in does: a member mints a provisioning
// token for their household and the agent redeems it, so that member is its owner. With
// removeOwner the member is a new one the primary invites, and removes once the agent is
// paired (decision 2026-10-14: an owner who left the household no longer counts).
func (d *Driver) pairDesktop(ctx context.Context, n *Node, removeOwner bool, statePath string) error {
	primary, hh, err := d.primaryHousehold(ctx)
	if err != nil {
		return err
	}
	pairer, ownerID := primary, int64(0)
	if removeOwner {
		if pairer, ownerID, err = d.newMember(ctx, hh, primary); err != nil {
			return err
		}
	}
	var tok struct {
		Token  string `json:"token"`
		NodeID string `json:"node_id"`
	}
	b, err := json.Marshal(map[string]string{"household_id": hh, "room": "office", "name": "pipeline-desktop"})
	if err != nil {
		return err
	}
	rb, err := doWant(ctx, nil, n.BaseURL+"/api/v0/provisioning/token", "application/json", b, pairer, http.StatusOK, http.StatusCreated)
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
	if removeOwner {
		url := fmt.Sprintf("%s/households/%s/members/%d", d.Desktop.AuthURL, hh, ownerID)
		if err := doDelete(ctx, url, primary); err != nil {
			return fmt.Errorf("removing the owner: %w", err)
		}
	}
	b, _ = json.Marshal(desktopState{NodeID: n.ID, Key: n.Key})
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(statePath, b, 0o600)
}

// primaryLoginTTL is how long primaryHousehold reuses a sign-in: a login per run hit the auth
// rate limit (429) during run --repeat, and the run failed as if the model had (#435).
const primaryLoginTTL = 5 * time.Minute

// primaryHousehold signs the primary user in and returns their bearer header and the
// household they administer (else their first). A sign-in is reused for primaryLoginTTL.
func (d *Driver) primaryHousehold(ctx context.Context) (map[string]string, string, error) {
	if d.Desktop == nil || d.Desktop.AuthURL == "" || d.Desktop.Email == "" {
		return nil, "", errors.New("the sandbox exports no SBX_AUTH / SBX_EMAIL (scripts/sandbox up again)")
	}
	if d.primary != nil && time.Since(d.primary.at) < primaryLoginTTL {
		return d.primary.hdr, d.primary.hh, nil
	}
	hdr, hh, err := d.signInPrimary(ctx)
	if err == nil {
		d.primary = &primarySignIn{hdr: hdr, hh: hh, at: time.Now()}
	}
	return hdr, hh, err
}

type primarySignIn struct {
	hdr map[string]string
	hh  string
	at  time.Time
}

func (d *Driver) signInPrimary(ctx context.Context) (map[string]string, string, error) {
	var login struct {
		AccessToken string `json:"access_token"`
	}
	if err := postJSON(ctx, d.Desktop.AuthURL+"/auth/login", nil,
		map[string]string{"email": d.Desktop.Email, "password": d.Desktop.Password}, &login); err != nil {
		return nil, "", err
	}
	primary := map[string]string{"Authorization": "Bearer " + login.AccessToken}
	var households []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := getJSON(ctx, d.Desktop.AuthURL+"/households", primary, &households); err != nil {
		return nil, "", err
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
		return nil, "", errors.New("the primary user is in no household")
	}
	return primary, hh, nil
}

// newMember invites and registers a throwaway member of hh, returning its bearer header and id.
func (d *Driver) newMember(ctx context.Context, hh string, primary map[string]string) (map[string]string, int64, error) {
	var inv struct {
		Code string `json:"code"`
	}
	b, _ := json.Marshal(map[string]any{"max_uses": 1})
	rb, err := doWant(ctx, nil, d.Desktop.AuthURL+"/households/"+hh+"/invites", "application/json", b, primary, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, 0, fmt.Errorf("invite: %w", err)
	}
	if err := json.Unmarshal(rb, &inv); err != nil {
		return nil, 0, err
	}
	var reg struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	b, _ = json.Marshal(map[string]string{"email": "pipeline-owner-" + newUUID()[:8] + "@example.com",
		"password": "pipeline-" + newUUID(), "invite_code": inv.Code})
	if rb, err = doWant(ctx, nil, d.Desktop.AuthURL+"/auth/register", "application/json", b, nil, http.StatusOK, http.StatusCreated); err != nil {
		return nil, 0, fmt.Errorf("register: %w", err)
	}
	if err := json.Unmarshal(rb, &reg); err != nil {
		return nil, 0, err
	}
	if reg.User.ID == 0 {
		return nil, 0, errors.New("register: no user id")
	}
	return map[string]string{"Authorization": "Bearer " + reg.AccessToken}, reg.User.ID, nil
}

// doDelete sends a DELETE and wants a 2xx.
func doDelete(ctx context.Context, url string, header map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
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
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("DELETE %s: %d %s", req.URL.Path, resp.StatusCode, truncate(string(b), 500))
	}
	return nil
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
