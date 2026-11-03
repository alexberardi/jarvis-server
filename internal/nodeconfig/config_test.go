package nodeconfig

import "testing"

func TestNonSecretBoundary(t *testing.T) {
	v := ViewOf(map[string]any{"wake_ack_audio_enabled": true, "wake_word_threshold": .4, "api_key": "SECRET", "k2": "SECRET", "mqtt_password": "SECRET", "silence_duration": map[string]any{"secret": "SECRET"}}, "desktop")
	if len(v.Values) != 2 || len(v.Fields) != 2 {
		t.Fatalf("unsafe view: %+v", v)
	}
	for _, k := range []string{"api_key", "k2", "mqtt_password", "cc_url", "auto_update", "computer_control", "__issued_at"} {
		if err := Validate(map[string]any{k: true}, Fields("pi")); err == nil {
			t.Errorf("accepted %s", k)
		}
	}
}
func TestValidation(t *testing.T) {
	for _, p := range []map[string]any{{"wake_word_threshold": 0.0}, {"wake_ack_audio_enabled": "yes"}, {"follow_up_listen_seconds": 4.0}, {"led_enabled": true}} {
		if err := Validate(p, Fields("desktop")); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
	if err := Validate(map[string]any{"wake_ack_audio_enabled": false, "follow_up_listen_seconds": 0.0}, Fields("desktop")); err != nil {
		t.Fatal(err)
	}
}
