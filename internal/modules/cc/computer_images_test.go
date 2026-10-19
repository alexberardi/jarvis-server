package cc

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// TestVisionLabel: background vision goes to the background slot, else to live, else nowhere.
func TestVisionLabel(t *testing.T) {
	ce := newChatEnv(t, prompts.ChatGPT)
	ctx := context.Background()
	if l, ok := ce.m.visionLabel(ctx); ok || l != "" {
		t.Fatalf("no vision = %q %v", l, ok)
	}
	ce.eng.setVision(llm.LabelLive, true)
	if l, ok := ce.m.visionLabel(ctx); !ok || l != llm.LabelLive {
		t.Fatalf("live only = %q %v", l, ok)
	}
	ce.eng.setVision(llm.LabelBackground, true)
	if l, ok := ce.m.visionLabel(ctx); !ok || l != llm.LabelBackground {
		t.Fatalf("background and live = %q %v", l, ok)
	}
	ce.eng.setVision(llm.LabelLive, false)
	if l, ok := ce.m.visionLabel(ctx); !ok || l != llm.LabelBackground {
		t.Fatalf("background only = %q %v", l, ok)
	}
}

func TestImageParts(t *testing.T) {
	parts := imageParts([]ToolImage{{MIME: "image/png", Data: pngBytes, Label: "shot"}})
	if len(parts) != 1 || parts[0].Type != "image_url" ||
		parts[0].ImageURL.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("parts = %+v", parts)
	}
	if len(imageParts(nil)) != 0 {
		t.Fatal("parts from no images")
	}
}
