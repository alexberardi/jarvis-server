package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// In a container the host updates jarvisd: `jarvisd upgrade` (and --rollback) refuse with the
// admin's hint, while --check still asks for releases.
func TestUpgradeRefusesInAContainer(t *testing.T) {
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer gh.Close()
	t.Setenv(EnvUpdateAPI, gh.URL)
	t.Setenv("JARVIS_CONTAINER", "unraid")
	home := t.TempDir()
	t.Setenv("JARVIS_HOME", home)

	for _, args := range [][]string{nil, {"--version", "v9.9.9"}, {"--rollback"}} {
		var out strings.Builder
		err := runUpgrade(context.Background(), home, args, &out)
		if err == nil || !strings.Contains(err.Error(), "Unraid's Docker tab") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a refused upgrade asked for releases %d times", hits.Load())
	}
	var out strings.Builder
	err := runUpgrade(context.Background(), home, []string{"--check"}, &out)
	if hits.Load() == 0 || (err != nil && strings.Contains(err.Error(), "container")) {
		t.Fatalf("--check: %v (%d requests)", err, hits.Load())
	}
}
