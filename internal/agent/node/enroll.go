package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Enrollment without a hotspot. Two honest paths:
//
//   - Login: the user signs in with their Jarvis account; the CLI uses the JWT to mint a
//     provisioning token for one of their households (POST /provisioning/token requires
//     membership), then redeems it. The JWT lives only in memory for the duration.
//   - Token: the user pastes a token + node id someone minted elsewhere (admin, app).
//
// Both end at POST /nodes/register, which returns the node key exactly once.

// Household is one of the user's households.
type Household struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// ErrPasswordChangeRequired: the account signed in with a temporary password. jarvisd accepts
// that session's token only for changing the password (403 password_change_required on
// everything else), so enrolment can't continue until the user changes it.
var ErrPasswordChangeRequired = errors.New("this account has a temporary password: change it first " +
	"(sign in to the Jarvis Automation app or web page and set a new password), then enroll again")

// Login signs in and returns an access token (kept in memory only).
func Login(ctx context.Context, hc *http.Client, authURL, email, password string) (string, error) {
	var resp struct {
		AccessToken        string `json:"access_token"`
		MustChangePassword bool   `json:"must_change_password"`
	}
	err := doJSON(ctx, hc, http.MethodPost, joinURL(authURL, "/auth/login"), nil,
		map[string]string{"email": email, "password": password}, &resp)
	if err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("login: no access token in the answer")
	}
	if resp.MustChangePassword {
		return "", ErrPasswordChangeRequired
	}
	return resp.AccessToken, nil
}

// Households lists the signed-in user's households.
func Households(ctx context.Context, hc *http.Client, authURL, jwt string) ([]Household, error) {
	var out []Household
	err := doJSON(ctx, hc, http.MethodGet, joinURL(authURL, "/households"), map[string]string{"Authorization": "Bearer " + jwt}, nil, &out)
	return out, err
}

// ProvisioningToken is a minted, single-use token bound to one household and node id.
type ProvisioningToken struct {
	Token  string `json:"token"`
	NodeID string `json:"node_id"`
	// The CC URL jarvisd wants nodes to use; may differ from the one the CLI reached.
	NodeCommandCenterURL string `json:"node_command_center_url"`
}

// MintToken asks CC for a provisioning token with a user JWT.
func MintToken(ctx context.Context, hc *http.Client, ccURL, jwt, householdID, name, room string) (ProvisioningToken, error) {
	body := map[string]any{"household_id": householdID}
	if name != "" {
		body["name"] = name
	}
	if room != "" {
		body["room"] = room
	}
	var t ProvisioningToken
	err := doJSON(ctx, hc, http.MethodPost, joinURL(ccURL, "/api/v0/provisioning/token"),
		map[string]string{"Authorization": "Bearer " + jwt}, body, &t)
	if err == nil && (t.Token == "" || t.NodeID == "") {
		err = fmt.Errorf("provisioning: incomplete token answer")
	}
	return t, err
}

// Registration is what a node says about itself when it redeems a token. Every field is
// optional; a jarvisd that predates one ignores it.
type Registration struct {
	Room string // overrides the token's room
	// DeviceID is the computer's stable identity (internal/agent/deviceid), so jarvisd can retire
	// the node an earlier enrolment of the same computer left behind. "" for a Pi.
	DeviceID string
	// NodeKind is "desktop" for the desktop agent, as in heartbeats.
	NodeKind string
}

// Register redeems a provisioning token and returns the node's credentials.
func Register(ctx context.Context, hc *http.Client, ccURL, configURL, nodeID, token string, reg Registration) (Creds, error) {
	body := map[string]any{"node_id": nodeID, "provisioning_token": token}
	if reg.Room != "" {
		body["room"] = reg.Room
	}
	if reg.DeviceID != "" {
		body["device_id"] = reg.DeviceID
	}
	if reg.NodeKind != "" {
		body["node_kind"] = reg.NodeKind
	}
	var resp struct {
		NodeID  string `json:"node_id"`
		Room    string `json:"room"`
		NodeKey string `json:"node_key"`
	}
	if err := doJSON(ctx, hc, http.MethodPost, joinURL(ccURL, "/api/v0/nodes/register"), nil, body, &resp); err != nil {
		return Creds{}, err
	}
	if resp.NodeKey == "" {
		return Creds{}, fmt.Errorf("register: no node key in the answer")
	}
	return Creds{ConfigURL: configURL, NodeID: resp.NodeID, NodeKey: resp.NodeKey, Room: resp.Room, EnrolledAt: time.Now().UTC()}, nil
}

// RegisterPreferring redeems a token at nodeCC, the command-center URL jarvisd wants nodes to
// use, and falls back to ccURL (the one discovery gave) only when nodeCC can't be reached: a
// VPN, or a loopback-only dev server. The token is single use, so an answer from nodeCC (any
// HTTP error) never falls back. note, when set, hears about the fallback.
func RegisterPreferring(ctx context.Context, hc *http.Client, nodeCC, ccURL, configURL, nodeID, token string, reg Registration, note func(error)) (Creds, error) {
	if nodeCC == "" || nodeCC == ccURL {
		return Register(ctx, hc, ccURL, configURL, nodeID, token, reg)
	}
	c, err := Register(ctx, hc, nodeCC, configURL, nodeID, token, reg)
	var he *HTTPError
	if err != nil && !errors.As(err, &he) {
		if note != nil {
			note(err)
		}
		return Register(ctx, hc, ccURL, configURL, nodeID, token, reg)
	}
	return c, err
}
