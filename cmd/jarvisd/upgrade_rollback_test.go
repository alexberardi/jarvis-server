package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/service"
)

// statusManager is an installed, running service whose home's config listener is the test
// server.
type statusManager struct {
	service.Manager
	home string
}

func (m statusManager) Status(context.Context) (service.Status, error) {
	return service.Status{Installed: true, Running: true, Home: m.home}, nil
}

// A10c: `jarvisd upgrade --rollback` returned as soon as the service manager had restarted
// jarvisd, without saying whether the restored version came up; it now waits for /health
// like an upgrade does.
func TestReportHealthy(t *testing.T) {
	var calls atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 || down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	home := t.TempDir()
	t.Setenv("JARVIS_HOME", home)
	t.Setenv("JARVIS_HOST", "127.0.0.1")
	t.Setenv("JARVIS_PORT_CONFIG", port)
	mgr := statusManager{home: home}

	var b strings.Builder
	if err := reportHealthy(context.Background(), mgr, 10*time.Second, "v1.0.0", "journalctl", &b); err != nil {
		t.Fatal(err)
	}
	if b.String() != "jarvisd v1.0.0 is up and healthy\n" || calls.Load() < 2 {
		t.Fatalf("%q after %d probes", b.String(), calls.Load())
	}

	// Unknown version: no version in the line.
	b.Reset()
	if err := reportHealthy(context.Background(), mgr, 0, "", "journalctl", &b); err != nil || b.String() != "jarvisd is up and healthy\n" {
		t.Fatalf("%q %v", b.String(), err)
	}

	// Never healthy: an error naming the version and the log.
	down.Store(true)
	b.Reset()
	err := reportHealthy(context.Background(), mgr, 0, "v1.0.0", "journalctl -u jarvisd", &b)
	if err == nil || !strings.Contains(err.Error(), "v1.0.0 didn't come up healthy") ||
		!strings.Contains(err.Error(), "journalctl -u jarvisd") || b.Len() != 0 {
		t.Fatalf("%v %q", err, b.String())
	}
}
