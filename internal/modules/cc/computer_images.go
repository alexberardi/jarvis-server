package cc

import (
	"context"
	"encoding/base64"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// imageParts are a background caller's tool images as image_url parts (data URLs), for the
// one model call they were lifted for.
func imageParts(imgs []ToolImage) []llm.Part {
	parts := make([]llm.Part, 0, len(imgs))
	for _, im := range imgs {
		url := "data:" + im.MIME + ";base64," + base64.StdEncoding.EncodeToString(im.Data)
		parts = append(parts, llm.Part{Type: "image_url", ImageURL: &llm.ImageURL{URL: url}})
	}
	return parts
}

// visionLabel is the slot a background caller sends images to: background when it has vision,
// else live when it has, else none (ok=false).
func (m *Module) visionLabel(ctx context.Context) (string, bool) {
	for _, label := range []string{llm.LabelBackground, llm.LabelLive} {
		if m.slotVision(ctx, label) {
			return label, true
		}
	}
	return "", false
}
