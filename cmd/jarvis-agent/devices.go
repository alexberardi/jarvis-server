package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/devices"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// wireDevices answers the app's device requests (device-scan, device-list,
// device-state), jarvisd's invalidate_device_cache and device_removed verbs and OAuth's
// jarvis/auth/+/ready over the sidecar's device families (internal/agent/devices), reports the
// configured families in the heartbeat, and keeps the registry of direct devices fresh from
// the first connect (reg, which control_device reads too). Call it after wireNodeSettings (which sets Topics and OnMQTTConnect).
func wireDevices(r *node.Runner, live *sidecar.Live, reg *devices.Registry, log *slog.Logger) {
	n := &devices.Node{NodeID: r.Creds.NodeID, Families: live, Registry: reg, Log: log, Changed: r.BeatNow}
	if r.Topics == nil {
		r.Topics = map[string]func(context.Context, *node.CC, []byte){}
	}
	for _, topic := range devices.Topics {
		r.Topics[topic] = func(ctx context.Context, cc *node.CC, payload []byte) { n.Handle(ctx, cc, topic, payload) }
	}
	verbs := map[string]func(context.Context, *node.CC, map[string]any){}
	for verb, h := range n.Verbs() {
		verbs[verb] = func(ctx context.Context, cc *node.CC, det map[string]any) { h(ctx, cc, det) }
	}
	verbs["display_media"] = func(ctx context.Context, cc *node.CC, details map[string]any) {
		rid, _ := details["reply_request_id"].(string)
		if rid == "" {
			return
		}
		valid, verifyErr := cc.Verify(ctx, rid)
		if verifyErr != nil || !valid {
			log.Warn("display media request failed verification")
			return
		}
		result, err := live.DeviceCall("display_media", details)
		if err != nil {
			result = map[string]any{"success": false, "error": "Home Assistant media integration unavailable"}
		}
		if err := cc.Do(ctx, "POST", "/device-control-results/"+rid, result, nil); err != nil {
			log.Warn("display media result not delivered", "err", err)
		}
	}
	addVerbs(r, verbs)
	if r.Absolute == nil {
		r.Absolute = map[string]func(context.Context, *node.CC, string, []byte){}
	}
	r.Absolute[devices.AuthReadyTopic] = func(ctx context.Context, cc *node.CC, topic string, payload []byte) {
		n.AuthReady(ctx, cc, topic, payload)
	}
	r.Protocols = live.Protocols
	live.OnSwap(r.BeatNow) // the families boot in the background, after the first beat
	var once sync.Once
	prev := r.OnMQTTConnect
	r.OnMQTTConnect = func(ctx context.Context, cc *node.CC) {
		once.Do(func() { go reg.Run(ctx, cc, log) })
		if prev != nil {
			prev(ctx, cc)
		}
	}
}
