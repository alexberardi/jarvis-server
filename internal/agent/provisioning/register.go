package provisioning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The retry policy of N:provisioning/registration.py. Registration runs seconds after the Wi-Fi
// join, before DHCP and DNS may have settled, so a connect failure is retried for about 30 s. A
// 5xx gets a few tries; a 4xx is CC's answer for this token and is never retried.
const (
	connectRetries     = 8
	serverErrorRetries = 3
	registerTimeout    = 30 * time.Second
)

// Failure kinds (RegistrationResult.error_kind).
const (
	kindTokenRejected = "token_rejected" // 401/403
	kindRejected      = "rejected"       // another 4xx
	kindServerError   = "server_error"   // 5xx after retries
	kindNetworkError  = "network_error"  // connect/timeout after retries
	kindBadResponse   = "bad_response"   // 2xx without a node key
)

// registration is a registration attempt's outcome, with what the app shows.
type registration struct {
	creds  node.Creds
	kind   string // "" on success
	status int    // CC's HTTP status when it refused; 0 otherwise
	detail string
}

func (r registration) ok() bool { return r.kind == "" }

// reason is RegistrationResult.reason: the status's error text.
func (r registration) reason() string {
	with := func(base string) string {
		if r.detail != "" {
			return base + ": " + r.detail
		}
		return base
	}
	switch {
	case r.ok():
		return ""
	case r.status != 0:
		return with(fmt.Sprintf("Command center rejected registration (HTTP %d)", r.status))
	case r.kind == kindNetworkError:
		return with("Could not reach command center")
	case r.detail != "":
		return r.detail
	}
	return "Registration failed"
}

// register redeems the provisioning token at ccURL (POST /api/v0/nodes/register, through
// node.Register: same body as registration.py), retrying as above with delay between tries.
func register(ctx context.Context, hc *http.Client, ccURL, configURL, nodeID, token, room string, delay time.Duration) registration {
	var network, server int
	for {
		actx, cancel := context.WithTimeout(ctx, registerTimeout)
		c, err := node.Register(actx, hc, ccURL, configURL, nodeID, token, node.Registration{Room: room})
		cancel()
		if err == nil {
			return registration{creds: c}
		}
		var he *node.HTTPError
		var ue *url.Error
		r := registration{detail: truncate(err.Error(), 200)}
		switch {
		case errors.As(err, &he):
			r.status, r.detail = he.Status, truncate(he.Detail, 200)
			switch {
			case he.Status >= 500:
				if server++; server < serverErrorRetries {
					break
				}
				r.kind = kindServerError
			case he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden:
				r.kind = kindTokenRejected
			default:
				r.kind = kindRejected
			}
		case errors.As(err, &ue):
			if network++; network < connectRetries {
				break
			}
			r.kind = kindNetworkError
		default:
			return registration{kind: kindBadResponse, detail: "Command center did not return node credentials"}
		}
		if r.kind != "" || ctx.Err() != nil {
			if r.kind == "" {
				r.kind = kindNetworkError
			}
			return r
		}
		select {
		case <-ctx.Done():
			r.kind = kindNetworkError
			return r
		case <-time.After(delay):
		}
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
