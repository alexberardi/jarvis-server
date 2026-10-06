//go:build contract

// Package contract is the black-box wire-contract suite (PLAN §4 layer 1, Phase 0 items 2 and 4).
//
// It speaks only HTTP (and later MQTT) to a target stack, so the same tests run against the
// legacy Python services today and against jarvisd later. A contract test only counts once it
// passes against Python. Every file carries the `contract` build tag, so a plain
// `go test ./...` skips the package; run it with scripts/contract.sh.
//
// See docs/contract/README.md for the environment variables and what is covered.
package contract

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Legacy listener names and default ports. Mirrors internal/platform/config on purpose rather
// than importing it: the suite must stay black-box.
const (
	Config        = "config"
	Auth          = "auth"
	Logs          = "logs"
	CommandCenter = "command-center"
	LLM           = "llm"
	Whisper       = "whisper"
	TTS           = "tts"
	Notifications = "notifications"
	Recipes       = "recipes"
	OCR           = "ocr"
)

// DefaultPorts maps each listener to its legacy port (PLAN §3.1).
var DefaultPorts = map[string]int{
	Config:        7700,
	Auth:          7701,
	Logs:          7702,
	CommandCenter: 7703,
	LLM:           7704,
	Whisper:       7706,
	TTS:           7707,
	Notifications: 7712,
	Recipes:       7030,
	OCR:           7031,
}

// Listeners is DefaultPorts' keys in a stable order.
var Listeners = []string{Config, Auth, Logs, CommandCenter, LLM, Whisper, TTS, Notifications, Recipes, OCR}

// Env var names. Secrets are only ever read from the environment.
const (
	EnvHost       = "JARVIS_CONTRACT_HOST"             // required: host or IP of the target stack
	EnvScheme     = "JARVIS_CONTRACT_SCHEME"           // optional, default http
	EnvPortPrefix = "JARVIS_CONTRACT_PORT_"            // optional: JARVIS_CONTRACT_PORT_COMMAND_CENTER=17703
	EnvSkip       = "JARVIS_CONTRACT_SKIP"             // optional: comma list of listeners the target doesn't run
	EnvAdminToken = "JARVIS_CONTRACT_AUTH_ADMIN_TOKEN" // jarvis-auth master admin token (fixtures)
	EnvAppID      = "JARVIS_CONTRACT_APP_ID"           // optional: reuse an existing app client...
	EnvAppKey     = "JARVIS_CONTRACT_APP_KEY"          // ...instead of minting a throwaway one
	EnvTimeout    = "JARVIS_CONTRACT_TIMEOUT"          // optional per-request timeout, Go duration, default 15s
)

// Target is the stack under test.
type Target struct {
	Host       string
	Scheme     string
	Ports      map[string]int
	Skip       map[string]bool
	AdminToken string
	AppID      string
	AppKey     string
	HTTP       *http.Client
	// RunID tags everything this run creates (app ids, emails, node ids) so leftovers are
	// recognisable on the target.
	RunID string
}

// LoadTarget reads the target from the environment. ok is false when JARVIS_CONTRACT_HOST is unset.
func LoadTarget(getenv func(string) string) (*Target, bool, error) {
	host := strings.TrimSpace(getenv(EnvHost))
	if host == "" {
		return nil, false, nil
	}
	tg := &Target{
		Host:       host,
		Scheme:     "http",
		Ports:      map[string]int{},
		Skip:       map[string]bool{},
		AdminToken: getenv(EnvAdminToken),
		AppID:      getenv(EnvAppID),
		AppKey:     getenv(EnvAppKey),
		RunID:      randHex(4),
	}
	if s := getenv(EnvScheme); s != "" {
		tg.Scheme = s
	}
	for name, port := range DefaultPorts {
		tg.Ports[name] = port
		key := EnvPortPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if v := getenv(key); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p <= 0 || p > 65535 {
				return nil, true, fmt.Errorf("%s=%q is not a port", key, v)
			}
			tg.Ports[name] = p
		}
	}
	for _, s := range strings.Split(getenv(EnvSkip), ",") {
		if s = strings.TrimSpace(s); s != "" {
			if _, known := DefaultPorts[s]; !known {
				return nil, true, fmt.Errorf("%s: unknown listener %q", EnvSkip, s)
			}
			tg.Skip[s] = true
		}
	}
	timeout := 15 * time.Second
	if v := getenv(EnvTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, true, fmt.Errorf("%s: %w", EnvTimeout, err)
		}
		timeout = d
	}
	tg.HTTP = &http.Client{Timeout: timeout}
	return tg, true, nil
}

var (
	target    *Target
	targetErr error
)

func init() {
	target, _, targetErr = LoadTarget(os.Getenv)
}

// T returns the target or skips the test when none is configured.
func T(t testing.TB) *Target {
	t.Helper()
	if targetErr != nil {
		t.Fatalf("contract config: %v", targetErr)
	}
	if target == nil {
		t.Skipf("contract: %s is not set; see docs/contract/README.md", EnvHost)
	}
	return target
}

// Need skips the test when the target doesn't run one of the listeners (JARVIS_CONTRACT_SKIP).
func (tg *Target) Need(t testing.TB, listeners ...string) {
	t.Helper()
	for _, l := range listeners {
		if tg.Skip[l] {
			t.Skipf("contract: listener %q is in %s", l, EnvSkip)
		}
	}
}

// NeedAdmin skips the test when the jarvis-auth admin token is not configured.
func (tg *Target) NeedAdmin(t testing.TB) {
	t.Helper()
	tg.Need(t, Auth)
	if tg.AdminToken == "" {
		t.Skipf("contract: %s is not set (needed to create throwaway fixtures)", EnvAdminToken)
	}
}

// URL is the absolute URL of path on a listener.
func (tg *Target) URL(listener, path string) string {
	port, ok := tg.Ports[listener]
	if !ok {
		panic("contract: unknown listener " + listener)
	}
	return fmt.Sprintf("%s://%s:%d%s", tg.Scheme, tg.Host, port, path)
}

// H is a set of request headers.
type H map[string]string

// AdminH carries the jarvis-auth master admin token.
func (tg *Target) AdminH() H { return H{"X-Jarvis-Admin-Token": tg.AdminToken} }

// Bearer is a user-JWT header.
func Bearer(token string) H { return H{"Authorization": "Bearer " + token} }

// Merge combines header sets; later ones win.
func Merge(hs ...H) H {
	out := H{}
	for _, h := range hs {
		for k, v := range h {
			out[k] = v
		}
	}
	return out
}

// Do sends a request. body may be nil, a []byte/string (sent raw) or any value (sent as JSON).
// Transport errors fail the test; HTTP error statuses do not.
func (tg *Target) Do(t testing.TB, listener, method, path string, body any, hs ...H) *Resp {
	t.Helper()
	r, err := tg.do(listener, method, path, body, hs...)
	if err != nil {
		t.Fatalf("%s %s: %v", method, tg.URL(listener, path), err)
	}
	r.t = t
	return r
}

func (tg *Target) Get(t testing.TB, listener, path string, hs ...H) *Resp {
	t.Helper()
	return tg.Do(t, listener, http.MethodGet, path, nil, hs...)
}

func (tg *Target) Post(t testing.TB, listener, path string, body any, hs ...H) *Resp {
	t.Helper()
	return tg.Do(t, listener, http.MethodPost, path, body, hs...)
}

// maxRateLimitWait bounds how long do waits out a 429. jarvis-auth's per-IP limiter on
// register/login/refresh answers Retry-After: 60, and back-to-back runs can trip it; the failed
// login lockout answers with a much longer Retry-After and is returned to the caller instead.
const maxRateLimitWait = 65 * time.Second

// do is the error-returning core, also used by fixtures created outside a test. A 429 with a
// short Retry-After is waited out and retried: rate limiting is the target's flood guard, not
// part of any contract under test here.
func (tg *Target) do(listener, method, path string, body any, hs ...H) (*Resp, error) {
	var payload []byte
	isJSON := false
	switch b := body.(type) {
	case nil:
	case []byte:
		payload = b
	case string:
		payload = []byte(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		payload = buf
		isJSON = true
	}
	url := tg.URL(listener, path)
	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequest(method, url, rdr)
		if err != nil {
			return nil, err
		}
		if isJSON || body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for _, h := range hs {
			for k, v := range h {
				req.Header.Set(k, v)
			}
		}
		res, err := tg.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			if secs, perr := strconv.Atoi(res.Header.Get("Retry-After")); perr == nil && secs >= 0 &&
				time.Duration(secs)*time.Second <= maxRateLimitWait {
				fmt.Fprintf(os.Stderr, "contract: %s %s rate-limited; waiting %ds\n", method, url, secs)
				time.Sleep(time.Duration(secs)*time.Second + time.Second)
				continue
			}
		}
		return &Resp{Method: method, URL: url, Status: res.StatusCode, Header: res.Header, Body: data}, nil
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// EnvImpl names the implementation under test: "python" (default) or "jarvisd". Tests for
// behaviour jarvisd deliberately changes (a LEGACY-BUG fixed under a decision) branch on it,
// so both sides stay tested: Python must still behave as frozen, jarvisd as decided.
const EnvImpl = "JARVIS_CONTRACT_IMPL"

// Jarvisd reports whether the target is jarvisd rather than the legacy Python stack.
func Jarvisd() bool { return os.Getenv(EnvImpl) == "jarvisd" }
