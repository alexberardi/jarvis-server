package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/logging"
)

func TestLogSink(t *testing.T) {
	var got struct {
		path, id, key string
		body          struct{ Logs []map[string]any }
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.id, got.key = r.URL.Path, r.Header.Get("X-Node-Id"), r.Header.Get("X-Node-Key")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var s LogSink
	rec := []logging.Record{{Time: time.Date(2026, 11, 1, 0, 39, 4, 0, time.UTC), Level: slog.LevelWarn, Message: "wake-suppressed-gate",
		Source: "jarvisd", Attrs: map[string]any{"score": "0.512", "err": errors.New("boom"), "wait": 20 * time.Second,
			"creds": slog.AnyValue(Creds{NodeID: "n1", NodeKey: "SECRET"}).Resolve().Any()}}}
	if err := s.Write(context.Background(), rec); !errors.Is(err, logging.ErrNotReady) {
		t.Fatalf("before Connect: %v, want ErrNotReady", err)
	}
	s.Connect(&CC{Creds: Creds{NodeID: "n1", NodeKey: "k1"}}) // discovery listed no jarvis-logs: still not ready
	if err := s.Write(context.Background(), rec); !errors.Is(err, logging.ErrNotReady) {
		t.Fatalf("without jarvis-logs: %v, want ErrNotReady", err)
	}
	s.Connect(&CC{Creds: Creds{NodeID: "n1", NodeKey: "k1"}, Services: Services{SvcLogs: srv.URL + "/logs"}})
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got.path != "/logs/api/v0/node/logs/batch" || got.id != "n1" || got.key != "k1" || len(got.body.Logs) != 1 {
		t.Fatalf("request: %+v", got)
	}
	e := got.body.Logs[0]
	c := e["context"].(map[string]any)
	if e["service"] != "jarvis-agent" || e["level"] != "WARNING" || e["message"] != "wake-suppressed-gate" ||
		e["timestamp"] != "2026-11-01T00:39:04Z" || c["score"] != "0.512" || c["err"] != "boom" || c["wait"] != "20s" || c["module"] != nil ||
		c["creds"].(map[string]any)["node_id"] != "n1" || strings.Contains(fmt.Sprint(c), "SECRET") {
		t.Fatalf("entry: %v", e)
	}
}
