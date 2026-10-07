//go:build parity

package llm

// Parity harness: serves this module alone, through the real module.Runner, so the contract
// suite (contract/llm_test.go) can run against Go before main.go wires it. It is a test
// behind the `parity` build tag, so it never reaches the release binary.
//
// The engine is an OpenAI-compatible server: by default the in-process fake from
// fake_test.go (answers "OK", streams word by word, 8-dim embeddings), or a real one via
// JARVIS_PARITY_LLM_URL (+ _LLM_MODEL, _LLM_KEY; a llama-server, the legacy stack's sidecar,
// or any remote) and JARVIS_PARITY_EMBED_URL (+ _EMBED_MODEL). The contract fixtures create
// apps in a legacy jarvis-auth, so app credentials are checked against its
// /internal/app-ping. Run (see docs/contract/README.md, "LLM"):
//
//	JARVIS_PARITY_PORT=27704 JARVIS_PARITY_AUTH_URL=http://127.0.0.1:7701 \
//	JARVIS_PARITY_SERVE_FOR=10m \
//	go test -tags parity -run TestParityServe -v ./internal/modules/llm/
//
// then the contract suite with JARVIS_CONTRACT_PORT_LLM=27704 JARVIS_CONTRACT_IMPL=jarvisd.

import (
	"context"
	"encoding/json"
	"errors"
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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
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

	fake := newFakeEngine()
	defer fake.close()
	fake.streamDelay = 20 * time.Millisecond // so the contract's cancel lands mid-stream
	llmURL := envOr("JARVIS_PARITY_LLM_URL", fake.srv.URL)
	embedURL := envOr("JARVIS_PARITY_EMBED_URL", fake.srv.URL)
	live := Endpoint{BaseURL: llmURL, APIKey: os.Getenv("JARVIS_PARITY_LLM_KEY"),
		Model: envOr("JARVIS_PARITY_LLM_MODEL", "parity-live.gguf"), ContextLength: 8192}
	bg := live
	bg.Model = envOr("JARVIS_PARITY_BG_MODEL", live.Model)
	res := &fakeResolver{eps: map[string]Endpoint{
		LabelLive: live, LabelBackground: bg,
		LabelEmbeddings: {BaseURL: embedURL, Model: envOr("JARVIS_PARITY_EMBED_MODEL", "all-MiniLM-L6-v2"), Embeddings: true},
	}, err: map[string]error{}}
	if os.Getenv("JARVIS_PARITY_LLM_URL") == "" {
		fake.streamText = "One two three four five six seven eight nine ten eleven twelve " +
			"thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty"
	}

	home := t.TempDir()
	cfg := pconfig.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{pconfig.ListenerLLM: port}}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	q := queue.New(d, log)
	auth := legacyAuthority{base: authURL, client: &http.Client{Timeout: 5 * time.Second}}
	r := &module.Runner{
		Deps: module.Deps{Config: cfg, DB: d, Log: log, Queue: q, Scheduler: scheduler.New(d, q, log)},
		Modules: []module.Module{&Module{
			Auth:          auth,
			SettingsRead:  settings.CombinedGuard(auth.verifyUser, auth.ValidateApp),
			SettingsWrite: settings.SuperuserGuard(auth.verifyUser),
			Version:       "parity",
			Resolver:      res,
			AppID:         "jarvisd-parity",
			AppKey:        "parity",
		}},
	}
	t.Logf("serving llm on 127.0.0.1:%d for %s (engine %s)", port, serveFor, llmURL)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
