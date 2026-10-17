package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakePairing serves start and poll; answers are the poll replies in order (the last repeats).
type fakePairing struct {
	mu      sync.Mutex
	answers []func(w http.ResponseWriter)
	polls   int
	secrets []string
	started map[string]string
}

func (f *fakePairing) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v0/nodes/pairings", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.started)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"pairing_id":"p1","poll_secret":"s3cret","code":"ABCD-EFGH","expires_in":12,"interval":3}`))
	})
	mux.HandleFunc("POST /api/v0/nodes/pairings/p1/poll", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.secrets = append(f.secrets, b["poll_secret"])
		i := min(f.polls, len(f.answers)-1)
		f.polls++
		f.answers[i](w)
	})
	return mux
}

func reply(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const pendingJSON = `{"status":"pending","expires_in":500,"interval":3}`

func noWait(context.Context, time.Duration) error { return nil }

func startFake(t *testing.T, answers ...func(http.ResponseWriter)) (*fakePairing, *httptest.Server, Pairing) {
	t.Helper()
	f := &fakePairing{answers: answers}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	p, err := StartPairing(context.Background(), srv.Client(), srv.URL, "Office-PC", "linux")
	if err != nil {
		t.Fatal(err)
	}
	return f, srv, p
}

func TestPairingApproved(t *testing.T) {
	f, srv, p := startFake(t,
		reply(200, pendingJSON), reply(200, pendingJSON),
		reply(200, `{"status":"approved","node_id":"n1","provisioning_token":"prov_x","household_id":"hh1","household_name":"The Smiths","approved_by":"sam@example.com","room":"office","name":"Office-PC","node_command_center_url":"http://192.0.2.1:7703"}`))
	if p.Code != "ABCD-EFGH" || f.started["name"] != "Office-PC" || f.started["platform"] != "linux" {
		t.Fatalf("start: %+v %v", p, f.started)
	}
	var waits []time.Duration
	res, err := WaitPairing(context.Background(), srv.Client(), srv.URL, p, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != "prov_x" || res.NodeID != "n1" || res.Room != "office" || res.HouseholdID != "hh1" || res.HouseholdName != "The Smiths" || res.ApprovedBy != "sam@example.com" || res.NodeCommandCenterURL != "http://192.0.2.1:7703" {
		t.Fatalf("result: %+v", res)
	}
	if len(waits) != 2 || waits[0] != 3*time.Second {
		t.Fatalf("waits: %v", waits)
	}
	for _, s := range f.secrets {
		if s != "s3cret" {
			t.Fatalf("poll sent secret %q", s)
		}
	}
}

func TestPairingDeniedAndExpired(t *testing.T) {
	_, srv, p := startFake(t, reply(200, pendingJSON), reply(200, `{"status":"denied"}`))
	if _, err := WaitPairing(context.Background(), srv.Client(), srv.URL, p, noWait); !errors.Is(err, ErrPairingDenied) {
		t.Fatalf("denied: %v", err)
	}
	_, srv, p = startFake(t, reply(404, `{"detail":"Pairing not found or expired"}`))
	if _, err := WaitPairing(context.Background(), srv.Client(), srv.URL, p, noWait); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("expired: %v", err)
	}
	// Never answered: polling stops at the expiry (12 s / 3 s + 2 polls), not forever.
	f, srv, p := startFake(t, reply(200, pendingJSON))
	if _, err := WaitPairing(context.Background(), srv.Client(), srv.URL, p, noWait); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("never approved: %v", err)
	}
	if f.polls != 6 {
		t.Fatalf("polls %d", f.polls)
	}
}

func TestPairingRetriesConnectionErrors(t *testing.T) {
	_, srv, p := startFake(t, reply(200, pendingJSON))
	url := srv.URL
	srv.Close() // nothing listens any more
	calls := 0
	_, err := WaitPairing(context.Background(), &http.Client{Timeout: time.Second}, url, p, func(context.Context, time.Duration) error {
		calls++
		return nil
	})
	if !errors.Is(err, ErrPairingExpired) || calls != 6 {
		t.Fatalf("err %v after %d waits", err, calls)
	}
	// An HTTP error that isn't a pairing status stops at once.
	_, srv, p = startFake(t, reply(500, `{"detail":"boom"}`))
	_, err = WaitPairing(context.Background(), srv.Client(), srv.URL, p, noWait)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 500 {
		t.Fatalf("500: %v", err)
	}
}

func TestPairingStopsWithContext(t *testing.T) {
	_, srv, p := startFake(t, reply(200, pendingJSON))
	ctx, cancel := context.WithCancel(context.Background())
	_, err := WaitPairing(ctx, srv.Client(), srv.URL, p, func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}
