//go:build parity

package ocr

// Parity harness: serves this module alone, through the real module.Runner, so the contract
// suite (contract/ocr_test.go) can run against Go before main.go wires it. It is a test
// behind the `parity` build tag, so it never reaches the release binary.
//
// The contract fixtures create apps and users in a legacy jarvis-auth, so app credentials are
// checked against that auth's /internal/app-ping and user JWTs against its /auth/me. Run (see
// docs/contract/README.md, "OCR"):
//
//	JARVIS_PARITY_PORT=27031 JARVIS_PARITY_AUTH_URL=http://127.0.0.1:7701 \
//	JARVIS_PARITY_SERVE_FOR=10m \
//	go test -tags parity -run TestParityServe -v ./internal/modules/ocr/
//
// Optional: JARVIS_PARITY_LLM_URL (+ _LLM_APP_ID/_LLM_APP_KEY) for LLM vision and validation,
// JARVIS_PARITY_OSX_URL/_OSX_KEY for Apple Vision, JARVIS_PARITY_TESSERACT ("-" disables).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
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
	cfg := pconfig.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{pconfig.ListenerOCR: port}}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	blobs, err := blob.NewFS(filepath.Join(home, "blobs"), log)
	if err != nil {
		t.Fatal(err)
	}
	q := queue.New(d, log)
	auth := legacyAuthority{base: authURL, client: &http.Client{Timeout: 5 * time.Second}}
	r := &module.Runner{
		Deps: module.Deps{Config: cfg, DB: d, Log: log, Queue: q, Scheduler: scheduler.New(d, q, log), Blobs: blobs},
		Modules: []module.Module{&Module{
			Auth:           auth,
			SettingsRead:   settings.CombinedGuard(auth.verifyUser, auth.ValidateApp),
			SettingsWrite:  settings.SuperuserGuard(auth.verifyUser),
			Version:        "parity",
			LLM:            parityLLMFromEnv(),
			AppleVisionURL: os.Getenv("JARVIS_PARITY_OSX_URL"),
			AppleVisionKey: os.Getenv("JARVIS_PARITY_OSX_KEY"),
			TesseractPath:  os.Getenv("JARVIS_PARITY_TESSERACT"),
			AppID:          "jarvisd-parity",
			AppKey:         "parity",
		}},
	}
	t.Logf("serving ocr on 127.0.0.1:%d for %s", port, serveFor)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

// parityLLM sends the module's chat requests to an OpenAI-compatible endpoint
// (JARVIS_PARITY_LLM_URL with JARVIS_PARITY_LLM_APP_ID/_APP_KEY); jarvisd itself calls its llm
// module in process.
type parityLLM struct{ url, id, key string }

func parityLLMFromEnv() LLM {
	l := parityLLM{os.Getenv("JARVIS_PARITY_LLM_URL"), os.Getenv("JARVIS_PARITY_LLM_APP_ID"), os.Getenv("JARVIS_PARITY_LLM_APP_KEY")}
	if l.url == "" || l.id == "" || l.key == "" {
		return nil
	}
	return l
}

func (l parityLLM) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	body := map[string]any{"model": req.Label, "messages": req.Messages, "max_tokens": req.MaxTokens,
		"temperature": req.Temperature, "response_format": req.ResponseFormat, "reasoning_budget": req.ReasoningBudget}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.url, "/")+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Jarvis-App-Id", l.id)
	hr.Header.Set("X-Jarvis-App-Key", l.key)
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("LLM endpoint returned %d: %s", resp.StatusCode, truncRunes(string(data), 200))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return nil, errors.New("invalid response format from LLM endpoint")
	}
	return &llm.ChatResponse{Content: out.Choices[0].Message.Content}, nil
}
