package main

import (
	"context"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// voiceProfileVerbs records enrollment and verification takes on any agent's
// microphone. Samples belong to the member's household profile, not this device.
func voiceProfileVerbs(mic *voice.Mic, log *slog.Logger) map[string]func(context.Context, *node.CC, map[string]any) {
	vp := &voice.VoiceProfiles{Mic: mic, Log: log}
	srv := func(cc *node.CC) *voice.HTTPServer { return voice.NewHTTPServer(cc.BaseURL, cc.Creds) }
	return map[string]func(context.Context, *node.CC, map[string]any){
		"enroll_voice": func(ctx context.Context, cc *node.CC, det map[string]any) { vp.Enroll(ctx, srv(cc), det) },
		"verify_voice": func(ctx context.Context, cc *node.CC, det map[string]any) { vp.Verify(ctx, srv(cc), det) },
	}
}
