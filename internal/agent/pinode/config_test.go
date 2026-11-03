package pinode

import (
	"context"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"path/filepath"
	"testing"
)

func TestConfirmedConfigPersistsBeforeTuning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	n := New(initial(), path, quiet)
	n.Desktop = true
	v := &fakeVoice{}
	n.Voice = v
	if _, err := n.ApplyConfig(context.Background(), map[string]any{"wake_ack_audio_enabled": false, "follow_up_listen_seconds": 1.5}); err != nil {
		t.Fatal(err)
	}
	s, _, err := settings.Load(path)
	if err != nil || s.Chimes == nil || *s.Chimes || *s.FollowUp != 1.5 || n.State().FollowUpListen != 1.5 || v.cfg.FollowUp.ListenSecs != 1.5 {
		t.Fatalf("save/live mismatch %+v %v", s, err)
	}
	if _, err := n.ApplyConfig(context.Background(), map[string]any{"api_key": "secret", "wake_ack_audio_enabled": true}); err == nil || n.State().WakeAck {
		t.Fatal("unsafe partial update")
	}
	if _, ok := n.Config(context.Background()).Values["led_enabled"]; ok {
		t.Fatal("desktop exposed LEDs")
	}
	n.path = filepath.Join(path, "bad.json")
	if _, err := n.ApplyConfig(context.Background(), map[string]any{"wake_ack_audio_enabled": true}); err == nil || n.State().WakeAck {
		t.Fatal("failed persistence applied live")
	}
}

func TestConfirmedConfigRejectsOverridesAndUnsupportedVoice(t *testing.T) {
	n := New(initial(), filepath.Join(t.TempDir(), "agent.json"), quiet)
	n.Desktop = true
	n.ReadOnly = []string{"wake_ack_audio_enabled"}
	if _, err := n.ApplyConfig(context.Background(), map[string]any{"wake_ack_audio_enabled": false}); err == nil {
		t.Fatal("changed command-line setting")
	}
	n.VoiceUnsupported = true
	if len(n.Config(context.Background()).Fields) != 0 {
		t.Fatal("exposed unsupported voice")
	}
	if _, err := n.ApplyConfig(context.Background(), map[string]any{"follow_up_listen_seconds": 1.0}); err == nil {
		t.Fatal("accepted unsupported voice")
	}
}
