package firstrun

import (
	"context"
	"github.com/alexberardi/jarvis-server/internal/nodeconfig"
	"net/http"
	"testing"
)

func TestSettingsRoutesKeepLocalBoundary(t *testing.T) {
	calls := 0
	s := testServer(t, Deps{Settings: func(_ context.Context, p map[string]any) (nodeconfig.View, error) {
		calls++
		return nodeconfig.ViewOf(map[string]any{"wake_ack_audio_enabled": true}, "desktop"), nil
	}})
	r := req(s, "POST", "api/settings", map[string]any{"settings": map[string]any{"wake_ack_audio_enabled": false}})
	r.Header.Set("Origin", "https://evil.example")
	if w := send(s, r); w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
	if calls != 0 {
		t.Fatal("cross-origin edit")
	}
	if w := send(s, req(s, "GET", "api/settings", nil)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := send(s, req(s, "POST", "api/settings", map[string]any{"settings": map[string]any{"wake_ack_audio_enabled": false}})); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
