//go:build contract

package contract

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The notifications → push relay request (PLAN Phase 0 item 2, the fake relay). The relay
// fronts Expo Push and holds its credentials; jarvis-notifications (and jarvisd's
// notifications module) talk to it with:
//
//	POST {relay}/v1/register {"household_id"} → {"jwt"}       (skipped when a JWT is pinned)
//	POST {relay}/v1/send  Authorization: Bearer <jwt>, X-Household-Id: <household>
//	     {"tokens", "title", "body", "data", "priority"} → {"results": [{status, error?, token?}]}
//
// The test runs a fake relay on 127.0.0.1:$JARVIS_CONTRACT_RELAY_PORT and freezes what arrives
// there. The target must be pointed at it:
//
//   - Legacy (MBP): RELAY_URL is http://host.docker.internal:7735, so a reverse tunnel needs no
//     stack change: `ssh -f -N -o ExitOnForwardFailure=yes -R 7735:127.0.0.1:<port>
//     alexanderberardi@10.0.0.103`. Docker Desktop's host.docker.internal reaches the Mac's
//     loopback, where sshd binds the forward (verified 2026-10-07). The MBP pins
//     RELAY_HOUSEHOLD_JWT, so it never calls /v1/register.
//   - jarvisd: the `relay.url` notifications setting (env fallback RELAY_URL) =
//     http://127.0.0.1:<port>. The push is a durable queue job (D31), so the request arrives
//     after the /notify response.
//
// Leftovers: none beyond the notification_log rows every notify writes (pruned after 30 days).

const EnvRelayPort = "JARVIS_CONTRACT_RELAY_PORT"

type relayReq struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// fakeRelay records every request and answers like the real relay. results decides the
// per-token /v1/send outcome.
type fakeRelay struct {
	mu      sync.Mutex
	reqs    []relayReq
	arrived chan struct{}
	jwt     string
	results func(token string) map[string]any
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, relayReq{r.Method, r.URL.Path, r.Header.Clone(), body})
	results := f.results
	f.mu.Unlock()
	select {
	case f.arrived <- struct{}{}:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/register":
		json.NewEncoder(w).Encode(map[string]any{"jwt": f.jwt})
	case "/v1/send":
		var in struct {
			Tokens []string `json:"tokens"`
		}
		json.Unmarshal(body, &in)
		out := []map[string]any{}
		for _, tok := range in.Tokens {
			out = append(out, results(tok))
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// waitSend returns the first /v1/send carrying token, plus any /v1/register seen before it.
func (f *fakeRelay) waitSend(t *testing.T, token string, timeout time.Duration) (send relayReq, register *relayReq) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		f.mu.Lock()
		for i, r := range f.reqs {
			if r.Path == "/v1/register" && register == nil {
				rr := f.reqs[i]
				register = &rr
			}
			if r.Path == "/v1/send" && strings.Contains(string(r.Body), token) {
				f.mu.Unlock()
				return r, register
			}
		}
		f.mu.Unlock()
		select {
		case <-f.arrived:
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatalf("relay: no /v1/send for %s within %s (is the target's relay pointed at this listener?)", token, timeout)
		}
	}
}

func startFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	port := os.Getenv(EnvRelayPort)
	if port == "" {
		t.Skipf("contract: %s is not set (fake push relay; see notifications_relay_test.go)", EnvRelayPort)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("fake relay listen: %v", err)
	}
	f := &fakeRelay{arrived: make(chan struct{}, 1), jwt: "contract-relay-jwt-" + randHex(4),
		results: func(string) map[string]any { return map[string]any{"status": "ok"} }}
	srv := &http.Server{Handler: f}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return f
}

func relayJSON(t *testing.T, r relayReq) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("relay %s body is not JSON: %v: %s", r.Path, err, r.Body)
	}
	return v
}

func expectRelayShape(t *testing.T, r relayReq, m Matcher) {
	t.Helper()
	if errs := m.Match(r.Path, relayJSON(t, r)); len(errs) > 0 {
		t.Fatalf("relay %s %s body: %s\n%s", r.Method, r.Path, strings.Join(errs, "; "), r.Body)
	}
}

func TestNotificationsRelay(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	f := startFakeRelay(t)
	app := SharedApp(t)
	u := NewUser(t)
	waitFor := 30 * time.Second

	notify := func(t *testing.T, title string, extra map[string]any) map[string]any {
		b := map[string]any{"target_type": "user", "target_id": fmt.Sprint(u.ID), "title": title, "body": "contract relay " + tg.RunID}
		for k, v := range extra {
			b[k] = v
		}
		return tg.Post(t, Notifications, "/api/v0/notify", b, app.H()).ExpectStatus(http.StatusOK).Object()
	}

	t.Run("send", func(t *testing.T) {
		tok := fakePushToken(tg, "relay")
		tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": tok, "device_type": "ios"}, u.H()).ExpectStatus(http.StatusOK)
		res := notify(t, "relay send "+randHex(3), map[string]any{
			"priority": "high", "category": "contract", "data": map[string]any{"type": "contract", "n": 1},
		})
		// Legacy delivers inline: the relay said ok, so delivered 1/1. jarvisd answers
		// "pending" with zero counts and the job records the outcome later (D31).
		want := with(notifyShape, "delivery_status", Eq("delivered"), "token_count", Eq(1), "success_count", Eq(1), "failure_count", Eq(0))
		if Jarvisd() {
			want = with(notifyShape, "delivery_status", Eq("pending"), "token_count", Eq(1), "success_count", Eq(0), "failure_count", Eq(0))
		}
		if errs := want.Match("notify", res); len(errs) > 0 {
			t.Fatalf("notify: %v", errs)
		}

		send, register := f.waitSend(t, tok, waitFor)
		if register != nil {
			// Only without a pinned JWT: the household registers once and caches the JWT.
			if register.Method != http.MethodPost {
				t.Fatalf("register method %s", register.Method)
			}
			expectRelayShape(t, *register, Obj{"household_id": Eq(u.HouseholdID)})
			if got := send.Header.Get("Authorization"); got != "Bearer "+f.jwt {
				t.Fatalf("send after register: Authorization %q, want the minted JWT", got)
			}
		}
		if send.Method != http.MethodPost {
			t.Fatalf("send method %s", send.Method)
		}
		if a := send.Header.Get("Authorization"); !strings.HasPrefix(a, "Bearer ") || len(a) == len("Bearer ") {
			t.Fatalf("send Authorization: %q", a)
		}
		if got := send.Header.Get("X-Household-Id"); got != u.HouseholdID {
			t.Fatalf("send X-Household-Id %q, want %s", got, u.HouseholdID)
		}
		if ct := send.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("send Content-Type %q", ct)
		}
		expectRelayShape(t, send, Obj{
			"tokens": Eq([]any{tok}), "title": String, "body": Eq("contract relay " + tg.RunID),
			"data": Obj{"type": Eq("contract"), "n": Eq(1)}, "priority": Eq("high"),
		})

		// No data: an empty object, never null; priority defaults to "default".
		f.mu.Lock()
		f.reqs = nil
		f.mu.Unlock()
		notify(t, "relay bare "+randHex(3), nil)
		send, _ = f.waitSend(t, tok, waitFor)
		expectRelayShape(t, send, Obj{
			"tokens": Eq([]any{tok}), "title": String, "body": String, "data": Eq(map[string]any{}), "priority": Eq("default"),
		})
	})

	t.Run("device_not_registered", func(t *testing.T) {
		// Expo's DeviceNotRegistered deactivates the token.
		u2 := NewUser(t)
		tok := fakePushToken(tg, "relay-dead")
		tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": tok, "device_type": "android"}, u2.H()).ExpectStatus(http.StatusOK)
		f.mu.Lock()
		f.results = func(tk string) map[string]any {
			if tk == tok {
				return map[string]any{"status": "error", "error": "DeviceNotRegistered"}
			}
			return map[string]any{"status": "ok"}
		}
		f.mu.Unlock()
		res := tg.Post(t, Notifications, "/api/v0/notify", map[string]any{
			"target_type": "user", "target_id": fmt.Sprint(u2.ID), "title": "relay dead " + randHex(3), "body": "contract",
		}, app.H()).ExpectStatus(http.StatusOK).Object()
		if !Jarvisd() {
			if errs := with(notifyShape, "delivery_status", Eq("failed"), "token_count", Eq(1), "success_count", Eq(0), "failure_count", Eq(1)).Match("notify", res); len(errs) > 0 {
				t.Fatalf("notify: %v", errs)
			}
		}
		f.waitSend(t, tok, waitFor)
		deadline := time.Now().Add(15 * time.Second)
		for {
			list := tg.Get(t, Notifications, "/api/v0/tokens/me", u2.H()).Expect(http.StatusOK, ArrayOf(tokenShape)).JSON().([]any)
			if len(list) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("token still active after DeviceNotRegistered: %v", list)
			}
			time.Sleep(500 * time.Millisecond)
		}
	})
}
