//go:build parity

package tts

// Parity harness: serves this module alone, through the real module.Runner, so the contract
// suite (contract/tts_test.go, plus the tts rows of health and settings) can run against Go
// before main.go wires it. It is a test behind the `parity` build tag, so it never reaches the
// release binary.
//
// App credentials are checked against a legacy jarvis-auth's /internal/app-ping and user JWTs
// against its /auth/me. The model is a Kokoro directory given directly (no model manager):
//
//	JARVIS_PARITY_PORT=27707 JARVIS_PARITY_AUTH_URL=http://127.0.0.1:7701 \
//	JARVIS_PARITY_KOKORO_DIR=$JARVIS_SHERPA_MODELS/kokoro-multi-lang-v1_0 \
//	JARVIS_PARITY_SERVE_FOR=10m \
//	go test -tags parity -run TestParityServe -v ./internal/modules/tts/
//
// Then: JARVIS_CONTRACT_HOST=127.0.0.1 JARVIS_CONTRACT_PORT_TTS=27707 scripts/contract.sh -run 'TestTTS|TestHealth/tts|TestSettingsAppAuth/tts'

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

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

// verifyUser asks the legacy auth who the token belongs to.
func (a legacyAuthority) verifyUser(ctx context.Context, token string) (authn.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/auth/me", nil)
	if err != nil {
		return authn.User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.client.Do(req)
	if err != nil {
		return authn.User{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return authn.User{}, errors.New("invalid token")
	}
	var me struct {
		ID          int64 `json:"id"`
		IsSuperuser bool  `json:"is_superuser"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return authn.User{}, err
	}
	return authn.User{ID: me.ID, IsSuperuser: me.IsSuperuser}, nil
}

func TestParityServe(t *testing.T) {
	port, _ := strconv.Atoi(os.Getenv("JARVIS_PARITY_PORT"))
	authURL := os.Getenv("JARVIS_PARITY_AUTH_URL")
	if port == 0 || authURL == "" {
		t.Skip("JARVIS_PARITY_PORT and JARVIS_PARITY_AUTH_URL are required")
	}
	serveFor, err := time.ParseDuration(os.Getenv("JARVIS_PARITY_SERVE_FOR"))
	if err != nil {
		serveFor = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), serveFor)
	defer cancel()

	home := t.TempDir()
	cfg := pconfig.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{pconfig.ListenerTTS: port}}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	auth := legacyAuthority{base: authURL, client: &http.Client{Timeout: 5 * time.Second}}
	r := &module.Runner{
		Deps: module.Deps{Config: cfg, DB: d, Log: log},
		Modules: []module.Module{&Module{
			Auth:          auth,
			SettingsRead:  settings.CombinedGuard(auth.verifyUser, auth.ValidateApp),
			SettingsWrite: settings.SuperuserGuard(auth.verifyUser),
			Version:       "parity",
			ModelDir:      os.Getenv("JARVIS_PARITY_KOKORO_DIR"),
			LibDir:        filepath.Join(os.TempDir(), "jarvis-sherpa-test"),
		}},
	}
	t.Logf("serving tts on 127.0.0.1:%d for %s", port, serveFor)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
