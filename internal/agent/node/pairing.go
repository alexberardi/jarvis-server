package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Device-code pairing (DESIGN §13): the agent asks jarvisd for a short code, shows it, and
// polls until a household member approves it. The approval yields an ordinary provisioning
// token, which Register redeems like any other, so no password is ever typed here.

// Pairing is a started pairing. Secret is the only thing that can read its outcome: keep it in
// memory and never print it.
type Pairing struct {
	ID        string `json:"pairing_id"`
	Secret    string `json:"poll_secret"`
	Code      string `json:"code"`
	ExpiresIn int    `json:"expires_in"`
	Interval  int    `json:"interval"`
}

// PairingResult is an approved pairing: a provisioning token for the approver's household,
// plus where the approver put this computer. HouseholdName and ApprovedBy are what the computer
// shows its user to confirm before it redeems the token.
type PairingResult struct {
	ProvisioningToken
	HouseholdID   string
	HouseholdName string
	ApprovedBy    string
	Room          string
	Name          string
}

var (
	// ErrPairingDenied: a member denied the request.
	ErrPairingDenied = errors.New("the pairing was denied")
	// ErrPairingExpired: nobody approved it in time (or jarvisd restarted).
	ErrPairingExpired = errors.New("the pairing code expired before anyone approved it")
)

// StartPairing asks command-center for a pairing code. name is how the computer is shown to
// the approver; platform is e.g. runtime.GOOS.
func StartPairing(ctx context.Context, hc *http.Client, ccURL, name, platform string) (Pairing, error) {
	var p Pairing
	err := doJSON(ctx, hc, http.MethodPost, joinURL(ccURL, "/api/v0/nodes/pairings"), nil,
		map[string]string{"name": name, "platform": platform}, &p)
	if err == nil && (p.ID == "" || p.Secret == "" || p.Code == "") {
		err = errors.New("pairing: incomplete answer")
	}
	return p, err
}

type pollAnswer struct {
	Status               string `json:"status"`
	NodeID               string `json:"node_id"`
	Token                string `json:"provisioning_token"`
	HouseholdID          string `json:"household_id"`
	HouseholdName        string `json:"household_name"`
	ApprovedBy           string `json:"approved_by"`
	Room                 string `json:"room"`
	Name                 string `json:"name"`
	NodeCommandCenterURL string `json:"node_command_center_url"`
}

// PollPairing asks once. It returns done=false while the pairing is pending.
func PollPairing(ctx context.Context, hc *http.Client, ccURL string, p Pairing) (PairingResult, bool, error) {
	var a pollAnswer
	err := doJSON(ctx, hc, http.MethodPost, joinURL(ccURL, "/api/v0/nodes/pairings/"+p.ID+"/poll"), nil,
		map[string]string{"poll_secret": p.Secret}, &a)
	var he *HTTPError
	switch {
	case errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusGone):
		return PairingResult{}, true, ErrPairingExpired
	case err != nil:
		return PairingResult{}, false, err
	}
	switch a.Status {
	case "pending":
		return PairingResult{}, false, nil
	case "denied":
		return PairingResult{}, true, ErrPairingDenied
	case "approved":
		if a.Token == "" || a.NodeID == "" {
			return PairingResult{}, true, errors.New("pairing: approved without credentials")
		}
		return PairingResult{
			ProvisioningToken: ProvisioningToken{Token: a.Token, NodeID: a.NodeID, NodeCommandCenterURL: a.NodeCommandCenterURL},
			HouseholdID:       a.HouseholdID, HouseholdName: a.HouseholdName, ApprovedBy: a.ApprovedBy,
			Room: a.Room, Name: a.Name,
		}, true, nil
	}
	return PairingResult{}, true, fmt.Errorf("pairing: unexpected status %q", a.Status)
}

// SleepCtx waits d or until ctx ends: WaitPairing's default wait.
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// WaitPairing polls until the pairing is approved, denied or expired, or ctx ends. wait sleeps
// between polls (SleepCtx outside tests). A poll that fails to connect is retried, but polling
// stops once the pairing must have expired, so a vanished server can't keep it waiting forever.
func WaitPairing(ctx context.Context, hc *http.Client, ccURL string, p Pairing, wait func(context.Context, time.Duration) error) (PairingResult, error) {
	interval := p.Interval
	if interval <= 0 {
		interval = 3
	}
	polls := p.ExpiresIn/interval + 2
	var lastErr error
	for range polls {
		res, done, err := PollPairing(ctx, hc, ccURL, p)
		if done {
			return res, err
		}
		if ctx.Err() != nil {
			return PairingResult{}, ctx.Err()
		}
		var he *HTTPError
		if errors.As(err, &he) {
			return PairingResult{}, err // jarvisd answered with something other than a status
		}
		lastErr = err
		if err := wait(ctx, time.Duration(interval)*time.Second); err != nil {
			return PairingResult{}, err
		}
	}
	if lastErr != nil {
		return PairingResult{}, fmt.Errorf("%w (last poll: %v)", ErrPairingExpired, lastErr)
	}
	return PairingResult{}, ErrPairingExpired
}
