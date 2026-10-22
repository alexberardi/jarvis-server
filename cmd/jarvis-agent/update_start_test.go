package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/selfupdate"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// A version with no published release puts nothing in the island: "Downloading …" shows only
// once the release is found.
func TestStartUpdateSaysNothingWithoutARelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	dir := t.TempDir()
	u := &selfupdate.Updater{Current: "v0.1.0-rc18", Paths: selfupdate.Paths{Dir: filepath.Join(dir, "updates")},
		Target: selfupdate.Target{GOOS: "linux", Exe: filepath.Join(dir, "jarvis-agent")},
		Source: update.Source{APIBase: srv.URL}, Keys: []update.PublicKey{}}
	var status []string
	start := startUpdate(u, func(s string) { status = append(status, s) }, slog.New(slog.DiscardHandler))
	err := start(context.Background(), selfupdate.StageOptions{Target: "v0.1.0-rc18.3", By: selfupdate.ByAuto})
	if !errors.Is(err, update.ErrNoRelease) {
		t.Fatalf("err = %v, want ErrNoRelease", err)
	}
	if len(status) != 0 {
		t.Fatalf("status shown for an unpublished release: %q", status)
	}
}
