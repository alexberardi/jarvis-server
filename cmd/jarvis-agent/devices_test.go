package main

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/devices"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// The Pi's device side: the three device topics beside the others, the two verbs beside the
// others, OAuth's auth-ready, and the families in the heartbeat (none with no sidecar yet).
func TestWireDevicesSubscribesAndReports(t *testing.T) {
	r := &node.Runner{Topics: map[string]func(context.Context, *node.CC, []byte){"k2/provision": nil},
		Verbs: map[string]func(context.Context, *node.CC, map[string]any){"enroll_voice": nil}}
	wireDevices(r, &sidecar.Live{}, &devices.Registry{}, slog.New(slog.DiscardHandler))

	var topics, verbs []string
	for k := range r.Topics {
		topics = append(topics, k)
	}
	for k := range r.Verbs {
		verbs = append(verbs, k)
	}
	sort.Strings(topics)
	sort.Strings(verbs)
	if !slices.Equal(topics, []string{"device-list", "device-scan", "device-state", "k2/provision"}) {
		t.Fatalf("topics %v", topics)
	}
	if !slices.Equal(verbs, []string{"device_removed", "display_media", "enroll_voice", "invalidate_device_cache"}) {
		t.Fatalf("verbs %v", verbs)
	}
	if _, ok := r.Absolute[devices.AuthReadyTopic]; !ok || len(r.Absolute) != 1 {
		t.Fatalf("absolute %v", r.Absolute)
	}
	if p := r.Protocols(); p == nil || len(p) != 0 {
		t.Fatalf("protocols %v", p)
	}
	if r.OnMQTTConnect == nil {
		t.Fatal("the registry isn't started on connect")
	}
}
