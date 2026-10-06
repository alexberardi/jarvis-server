//go:build contract

package contract

import (
	"net/http"
	"testing"
)

// healthShapes freezes GET /health per legacy listener. config-service's registry probes each
// service's health_path and only looks at the status code, so the bodies are frozen loosely
// (key presence and types) except where a field is a literal identity that callers may read.
var healthShapes = map[string]Matcher{
	Config: Obj{"status": Eq("ok")},
	Auth:   Obj{"status": Eq("ok")},
	// LEGACY-BUG: logs reports "degraded" (Loki down) with HTTP 200, and its timestamp is naive
	// (datetime.utcnow().isoformat()), unlike auth's zoned timestamps.
	Logs: Obj{
		"status":    OneOf("healthy", "degraded"),
		"timestamp": TimestampNaive,
		"services":  Obj{"loki": OneOf("available", "unavailable")},
	},
	// LEGACY-BUG: naive timestamp, as for logs.
	CommandCenter: Obj{
		"status":    Eq("healthy"),
		"service":   Eq("jarvis-command-center"),
		"timestamp": TimestampNaive,
	},
	// model_service is the model sidecar's own /health, passed through; its contents depend on
	// the backend, so only its presence is frozen.
	LLM:           Obj{"status": String, "model_service": Any, "version": String},
	Whisper:       Open{"status": Eq("healthy"), "version": String, "speaker": Open{"recognition_enabled": Bool}},
	TTS:           Obj{"status": Eq("healthy"), "version": String},
	Notifications: Obj{"status": Eq("ok"), "service": Eq("jarvis-notifications")},
	Recipes:       Obj{"status": Eq("ok")},
	OCR:           Obj{"status": Eq("ok"), "version": String},
}

func TestHealth(t *testing.T) {
	tg := T(t)
	for _, l := range Listeners {
		t.Run(l, func(t *testing.T) {
			tg.Need(t, l)
			tg.Get(t, l, "/health").Expect(http.StatusOK, healthShapes[l])
		})
	}
}

// jarvis-log-client and the admin UI ping logs; frozen with health.
func TestLogsPing(t *testing.T) {
	tg := T(t)
	tg.Need(t, Logs)
	tg.Get(t, Logs, "/ping").Expect(http.StatusOK, Obj{"message": Eq("pong")})
}
