//go:build parity

package notifications

// Parity harness: serves this module alone, through the real module.Runner, so the contract
// suite (contract/notifications_test.go) can run against Go before main.go wires it. It is a
// test behind the `parity` build tag, so it never reaches the release binary.
//
// The contract fixtures create apps and users in a legacy jarvis-auth, so app credentials are
// checked against that auth's /internal/app-ping and user JWTs are verified locally with its
// HS256 secret, exactly as the legacy service did. Run (see docs/contract/README.md):
//
//	JARVIS_PARITY_PORT=27712 JARVIS_PARITY_AUTH_URL=http://127.0.0.1:27701 \
//	JARVIS_PARITY_HMAC=<AUTH_SECRET_KEY> JARVIS_PARITY_ADMIN_KEY=<key> \
//	JARVIS_PARITY_RELAY_URL=http://127.0.0.1:7735 JARVIS_PARITY_SERVE_FOR=10m \
//	go test -tags parity -run TestParityServe -v ./internal/modules/notifications/

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// legacyAuthority validates app credentials against a legacy jarvis-auth.
type legacyAuthority struct {
	base   string
	client *http.Client
}

func (a legacyAuthority) ValidateApp(ctx context.Context, id, key string) (authn.App, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/internal/app-ping", nil)
	if err != nil {
		return authn.App{}, false, err
	}
	req.Header.Set("X-Jarvis-App-Id", id)
	req.Header.Set("X-Jarvis-App-Key", key)
	resp, err := a.client.Do(req)
	if err != nil {
		return authn.App{}, false, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return authn.App{ID: id}, resp.StatusCode == http.StatusOK, nil
}

func (legacyAuthority) ValidateNode(context.Context, string, string, string) (authn.NodeValidation, error) {
	return authn.NodeValidation{}, nil
}

func (legacyAuthority) HouseholdRole(context.Context, int64, string) (authn.Role, bool, error) {
	return "", false, nil
}

func TestParityServe(t *testing.T) {
	port, _ := strconv.Atoi(os.Getenv("JARVIS_PARITY_PORT"))
	authURL, hmac := os.Getenv("JARVIS_PARITY_AUTH_URL"), os.Getenv("JARVIS_PARITY_HMAC")
	if port == 0 || authURL == "" || hmac == "" {
		t.Skip("JARVIS_PARITY_PORT, JARVIS_PARITY_AUTH_URL and JARVIS_PARITY_HMAC are required")
	}
	serveFor, err := time.ParseDuration(os.Getenv("JARVIS_PARITY_SERVE_FOR"))
	if err != nil {
		serveFor = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), serveFor)
	defer cancel()

	home := t.TempDir()
	cfg := pconfig.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{pconfig.ListenerNotifications: port}}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	q := queue.New(d, log)
	r := &module.Runner{
		Deps: module.Deps{Config: cfg, DB: d, Log: log, Queue: q, Scheduler: scheduler.New(d, q, log)},
		Modules: []module.Module{&Module{
			Auth:     legacyAuthority{base: authURL, client: &http.Client{Timeout: 5 * time.Second}},
			Users:    KeysVerifier{Keys: authn.Keys{HMAC: []byte(hmac)}},
			AdminKey: os.Getenv("JARVIS_PARITY_ADMIN_KEY"),
			RelayURL: os.Getenv("JARVIS_PARITY_RELAY_URL"),
		}},
	}
	t.Logf("serving notifications on 127.0.0.1:%d for %s", port, serveFor)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
