package main

import (
	"context"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// voiceProfileVerbs are a Pi's enroll_voice and verify_voice: the app's "record on this node"
// takes, recorded through the voice pipeline's microphone. A desktop has none: its speaker is
// the signed-in user (#143), so those commands stay ignored.
func voiceProfileVerbs(p profile, mic *voice.Mic, log *slog.Logger) map[string]func(context.Context, *node.CC, map[string]any) {
	if p.desktop {
		return nil
	}
	vp := &voice.VoiceProfiles{Mic: mic, Log: log}
	srv := func(cc *node.CC) *voice.HTTPServer { return voice.NewHTTPServer(cc.BaseURL, cc.Creds) }
	return map[string]func(context.Context, *node.CC, map[string]any){
		"enroll_voice": func(ctx context.Context, cc *node.CC, det map[string]any) { vp.Enroll(ctx, srv(cc), det) },
		"verify_voice": func(ctx context.Context, cc *node.CC, det map[string]any) { vp.Verify(ctx, srv(cc), det) },
	}
}
