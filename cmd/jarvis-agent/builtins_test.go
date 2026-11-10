package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/builtins"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

type routineMusic struct{ play func() error }

func TestRoutineVerbAdvertisesSimpleSupport(t *testing.T) {
	r := &node.Runner{Capabilities: func() map[string]any { return map[string]any{"led_mode": "on"} }}
	wireRoutineVerb(r, routineMusic{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if caps := r.Capabilities(); caps["simple_routines"] != true || caps["led_mode"] != "on" {
		t.Fatalf("capabilities=%v", caps)
	}
}

func (routineMusic) Available(context.Context) []node.ToolDef { return nil }
func (m routineMusic) Call(context.Context, string, map[string]any, node.Request) (map[string]any, bool) {
	return map[string]any{"success": true, "message": "Now playing the radio.",
		node.SignalsKey: node.Signals{OnResponseComplete: m.play}}, true
}

func TestRoutineVerbStartsDeferredPlayback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		valid  bool
		failed bool
	}{
		{"run now or scheduled", true, false},
		{"playback error is logged", true, true},
		{"unverified request", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posted atomic.Bool
			played := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v0/commands/r1/verify":
					_ = json.NewEncoder(w).Encode(map[string]bool{"valid": tc.valid})
				case "/api/v0/device-control-results/r1":
					var body struct{ Output map[string]any }
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Output["success"] != true || body.Output["passed"] != float64(1) || body.Output[node.SignalsKey] != nil {
						t.Errorf("posted result = %v", body.Output)
					}
					posted.Store(true)
					_, _ = io.WriteString(w, `{}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			music := routineMusic{play: func() error {
				played++
				if !posted.Load() {
					t.Error("playback started before the result was posted")
				}
				if tc.failed {
					return errors.New("speaker unavailable")
				}
				return nil
			}}
			routine := &builtins.Routine{Tools: music, CC: func() *node.CC { return nil }}
			runner := &node.Runner{}
			var logs bytes.Buffer
			wireRoutineVerb(runner, routine, slog.New(slog.NewTextHandler(&logs, nil)))
			runner.Verbs["routine"](context.Background(), node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k"}), map[string]any{
				"routine_name": "radio_time", "reply_request_id": "r1",
				"routine": map[string]any{"steps": []any{map[string]any{"command": "music"}}},
			})
			want := 0
			if tc.valid {
				want = 1
			}
			if played != want || posted.Load() != tc.valid {
				t.Fatalf("played %d times (want %d), posted %v", played, want, posted.Load())
			}
			if tc.failed && !strings.Contains(logs.String(), "speaker unavailable") {
				t.Fatalf("playback failure not logged: %s", &logs)
			}
		})
	}
}
