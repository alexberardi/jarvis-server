package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// TestReadScreenLive runs "read the screen" end to end against a real model: the desktop
// agent's own tool (spec, capture → scale → JPEG → image_attachments) over a fixed image, the
// voice turn and the continue through command-center, and a vision llama-server.
//
//	JARVIS_SCREEN_EVAL_LLM=http://127.0.0.1:28116 JARVIS_SCREEN_EVAL_IMAGE=shot.jpg \
//	  [JARVIS_SCREEN_EVAL_ASK="what does this error say"] [JARVIS_SCREEN_EVAL_WANT=parseConfig] \
//	  [JARVIS_SCREEN_EVAL_EXPECT_NONE=1 (the ask isn't about the screen: no screenshot)] \
//	  go test -run TestReadScreenLive -v -timeout 30m ./internal/modules/cc/
//
// The llama-server needs the model's projector (--mmproj) and jarvisd's pinned template.
func TestReadScreenLive(t *testing.T) {
	base, path := os.Getenv("JARVIS_SCREEN_EVAL_LLM"), os.Getenv("JARVIS_SCREEN_EVAL_IMAGE")
	if base == "" || path == "" {
		t.Skip("JARVIS_SCREEN_EVAL_LLM and JARVIS_SCREEN_EVAL_IMAGE not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	ask := os.Getenv("JARVIS_SCREEN_EVAL_ASK")
	if ask == "" {
		ask = "what does the error on my screen say"
	}
	tools := &screen.Tools{C: fixedCapture{img: img}}
	defs := tools.Available(context.Background())
	clientTools, _ := json.Marshal([]any{defs[0].ClientTool})

	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.eng.strict.Store(true) // jarvisd's pinned template (ID12)
	ve.eng.ctxLen = 16384
	ve.eng.setVision(llm.LabelLive, true)
	ve.start("c1", string(clientTools))
	ve.eng.mu.Lock()
	ve.eng.respond = realEngine(t, base)
	ve.eng.mu.Unlock()

	start := time.Now()
	b := ve.turn("/api/v0/voice/command", "c1", ask, nil).want(200).json()
	t.Logf("turn (%.1fs): stop=%v message=%q", time.Since(start).Seconds(), b["stop_reason"], b["assistant_message"])
	calls, _ := b["tool_calls"].([]any)
	if os.Getenv("JARVIS_SCREEN_EVAL_EXPECT_NONE") != "" {
		// A request that isn't about the screen must not take a screenshot.
		for _, c := range calls {
			if c.(map[string]any)["function"].(map[string]any)["name"] == screen.ToolRead {
				t.Fatalf("screenshot taken unprompted for %q", ask)
			}
		}
		return
	}
	if b["stop_reason"] != "tool_calls" || len(calls) == 0 {
		t.Fatalf("the model didn't call %s: %v", screen.ToolRead, b)
	}
	call := calls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if fn["name"] != screen.ToolRead {
		t.Fatalf("called %v", fn["name"])
	}
	out, _ := tools.Call(context.Background(), screen.ToolRead, node.DecodeArgs(fn["arguments"]), node.Request{})
	start = time.Now()
	b = ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c1",
		"tool_results": []any{map[string]any{"tool_call_id": call["id"], "output": out}}}, ve.node.h()).want(200).json()
	answer, _ := b["assistant_message"].(string)
	t.Logf("continue (%.1fs): stop=%v answer=%q", time.Since(start).Seconds(), b["stop_reason"], answer)
	if want := os.Getenv("JARVIS_SCREEN_EVAL_WANT"); want != "" && !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) {
		t.Fatalf("answer doesn't mention %q", want)
	}
}

type fixedCapture struct{ img image.Image }

func (f fixedCapture) Available(context.Context) (bool, string) { return true, "" }
func (f fixedCapture) Capture(context.Context, string) (screen.Shot, error) {
	return screen.Shot{Image: f.img, Title: "weatherd — foot", App: "foot"}, nil
}
