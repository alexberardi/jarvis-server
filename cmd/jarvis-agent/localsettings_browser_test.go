package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/firstrun"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/nodeconfig"
)

// Opt-in browser integration: npm ci in e2e/web, then JARVIS_SETTINGS_BROWSER=1
// go test ./cmd/jarvis-agent -run '^TestLocalSettingsBrowser$'. No microphone,
// desktop session, server discovery or installed agent is used.
func TestLocalSettingsBrowser(t *testing.T) {
	if os.Getenv("JARVIS_SETTINGS_BROWSER") != "1" {
		t.Skip("opt-in Playwright integration")
	}
	if !voice.Supported {
		t.Skip("voice settings unavailable on this platform")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	srv, err := firstrun.New(firstrun.Deps{
		Loopback: "http://127.0.0.1:1", ComputerName: "Settings test computer",
		Settings: func(ctx context.Context, patch map[string]any) (nodeconfig.View, error) {
			d, err := stoppedSettings(path, slog.New(slog.DiscardHandler))
			if err != nil {
				return nodeconfig.View{}, err
			}
			return d.edit(ctx, patch)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	cfg, _ := json.Marshal(map[string]string{"url": srv.URL(), "settings": path, "output": os.Getenv("JARVIS_SETTINGS_SHOTS")})
	cmd := exec.CommandContext(t.Context(), "node", "../../tools/screenshots/local-settings.mjs")
	cmd.Stdin = strings.NewReader(string(cfg))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser check: %v\n%s", err, out)
	}
}
