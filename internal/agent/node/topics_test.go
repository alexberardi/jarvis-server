package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/mqtt"
)

// The runner subscribes the extra node topics (the K2 relay, settings requests, config pushes)
// and runs the connect backstop; each message reaches its handler with the run's CC client.
func TestRunnerServesExtraTopicsAndTheConnectBackstop(t *testing.T) {
	b, err := mqtt.New(nil, mqtt.Options{TCPAddr: "127.0.0.1:0", AllowAnonymous: true, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var srvURL string
	beats := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]string{
				{"name": SvcCommandCenter, "url": srvURL},
				{"name": SvcMQTT, "url": "mqtt://" + b.TCPAddr()},
			}})
		case "/api/v0/node/mqtt-credentials":
			_, _ = w.Write([]byte(`{"username":null,"password":null}`))
		case "/api/v0/admin/nodes/heartbeat":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			beats <- m
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	var needs atomic.Bool
	needs.Store(true)
	connected := make(chan *CC, 4)
	got := make(chan string, 4)
	r := &Runner{Creds: Creds{ConfigURL: srv.URL, NodeID: "n1", NodeKey: "k"}, Tools: Multi{}, Log: quiet,
		Heartbeat: 1 << 40,
		Topics: map[string]func(context.Context, *CC, []byte){
			"k2/provision": func(_ context.Context, cc *CC, p []byte) {
				if cc == nil {
					t.Error("no CC client")
				}
				got <- string(p)
			},
		},
		OnMQTTConnect: func(_ context.Context, cc *CC) { connected <- cc },
		NeedsK2:       func() bool { return needs.Load() },
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	if beat := <-beats; beat["needs_k2"] != true {
		t.Fatalf("first beat needs_k2 = %v", beat["needs_k2"])
	}
	if cc := <-connected; cc == nil || cc.Creds.NodeID != "n1" {
		t.Fatalf("backstop got %v", cc)
	}
	// Subscribed before the backstop runs, so this nudge is delivered.
	if err := b.Publish("jarvis/nodes/n1/k2/provision", []byte(`{"request_id":"r1"}`), 1, false); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p != `{"request_id":"r1"}` {
		t.Fatalf("payload = %s", p)
	}
	// BeatNow sends a heartbeat without waiting for the (never reached) tick.
	needs.Store(false)
	r.BeatNow()
	if beat := <-beats; beat["needs_k2"] != false {
		t.Fatalf("kicked beat needs_k2 = %v", beat["needs_k2"])
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
