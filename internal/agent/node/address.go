package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// DefaultConfigPort is the config service's port, assumed when an address has none, and tried
// on the same host when the address given is another of the server's ports (the web app's 7710).
const DefaultConfigPort = "7700"

// configServiceName is what the config service's GET /info says it is.
const configServiceName = "jarvis-config-service"

// Plain-words errors for an address someone typed or pasted (enroll --server, the setup page).
var (
	ErrNoAddress  = errors.New("Type the server's address, like 192.168.1.10.")
	ErrBadAddress = errors.New("That doesn't look like an address. Try something like 192.168.1.10 or jarvis.local.")
)

// NotJarvisError: something answered at the address, but no Jarvis Automation server.
type NotJarvisError struct{ Host string }

func (e *NotJarvisError) Error() string {
	return "That address doesn't look like a Jarvis Automation server (" + e.Host + " answered, but not as one). " +
		"Check the address: the computer that runs Jarvis Automation, like 192.168.1.10."
}

// NoAnswerError: nothing answered at the address.
type NoAnswerError struct{ Host string }

func (e *NoAnswerError) Error() string {
	return "No Jarvis Automation server answered at " + e.Host + ". Check the address, and that the server is on."
}

// NormalizeAddress turns what someone typed or pasted into a URL to try first: a bare host
// ("192.168.1.10", "jarvis.local"), host:port, or any URL of the server, the web app's
// included ("http://192.168.1.10:7710/addons"). The path, query and fragment are dropped;
// plain http without a port gets the config service's (7700). It says what's wrong in plain
// words, and never contacts anything.
func NormalizeAddress(s string) (string, error) { return normalizeAddress(s, DefaultConfigPort) }

func normalizeAddress(s, port string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrNoAddress
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrBadAddress
	}
	host := u.Host
	if u.Port() == "" && u.Scheme == "http" {
		host = net.JoinHostPort(u.Hostname(), port)
	}
	return u.Scheme + "://" + host, nil
}

// IsConfigService asks base/info whether it is Jarvis Automation's config service. reached is
// false when nothing answered at all.
func IsConfigService(ctx context.Context, hc *http.Client, base string) (ok, reached bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/info", nil)
	if err != nil {
		return false, false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var info struct {
		Service string `json:"service"`
	}
	ok = resp.StatusCode == http.StatusOK &&
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&info) == nil && info.Service == configServiceName
	return ok, true
}

// ResolveConfigURL finds the config service from any address of a Jarvis Automation server
// (NormalizeAddress): the address itself when it is the config service, else the same host on
// the config service's port (someone pasted the web app's URL, which serves a page, not JSON).
// The errors are NotJarvisError, NoAnswerError or NormalizeAddress's, all in plain words.
//
// The web app's port can't say where the config service is to someone not signed in (its
// /api/system/info is superuser-only), so a server whose config service isn't on 7700 must be
// given by that port.
func ResolveConfigURL(ctx context.Context, hc *http.Client, typed string) (string, error) {
	return ResolveConfigURLOn(ctx, hc, typed, DefaultConfigPort)
}

// ResolveConfigURLOn is ResolveConfigURL with the config service's port to assume (tests).
func ResolveConfigURLOn(ctx context.Context, hc *http.Client, typed, port string) (string, error) {
	first, err := normalizeAddress(typed, port)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(first)
	cands := []string{first}
	for _, c := range []string{
		u.Scheme + "://" + net.JoinHostPort(u.Hostname(), port),
		"http://" + net.JoinHostPort(u.Hostname(), port),
	} {
		if !slices.Contains(cands, c) {
			cands = append(cands, c)
		}
	}
	reachedAny := false
	for _, c := range cands {
		ok, reached := IsConfigService(ctx, hc, c)
		if ok {
			return c, nil
		}
		reachedAny = reachedAny || reached
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	if reachedAny {
		return "", &NotJarvisError{Host: u.Host}
	}
	return "", &NoAnswerError{Host: u.Host}
}
