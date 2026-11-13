package cc

import (
	"context"
	"errors"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// ChatTextRequest is one typed (or transcribed) message for ChatText.
type ChatTextRequest struct {
	Message        string
	NodeID         string
	HouseholdID    string
	ConversationID string // "" starts one
	Timezone       string // "" is the household default
	Images         []any  // the mobile chat images list (chat_images.go), optional
}

// ChatTextResult is the reply: Text is what Jarvis said, ConversationID what to send next time
// (the server may mint a new one).
type ChatTextResult struct {
	Text           string
	ConversationID string
}

// ChatText runs one chat turn as user u, exactly as POST /mobile/chat does (same node gate,
// warmup, pipeline and tools), but returns the final text instead of streaming it. The Discord
// bridge is its caller. A failure carries the same message the app would show.
func (m *Module) ChatText(ctx context.Context, u authn.User, in ChatTextRequest) (ChatTextResult, error) {
	req := chatRequest{Message: in.Message, NodeID: in.NodeID, HouseholdID: in.HouseholdID,
		ConversationID: in.ConversationID, Timezone: in.Timezone, rawImages: in.Images}
	if req.Timezone == "" {
		req.Timezone = chatDefaultTimezone
	}
	row, err := m.chatNode(ctx, u, req)
	if err != nil {
		return ChatTextResult{}, errors.New(m.errText(err))
	}
	if m.LLM == nil {
		return ChatTextResult{}, errors.New("Voice pipeline unavailable")
	}
	if len(req.rawImages) > 0 {
		if !m.slotVision(ctx, llm.LabelLive) {
			return ChatTextResult{}, errors.New("Images are not available: the live model has no image input")
		}
		imgs, err := parseChatImages(req.rawImages)
		if err != nil {
			return ChatTextResult{}, err
		}
		req.Images, req.rawImages = imgs, nil
	}
	var res ChatTextResult
	var failure string
	sse := &sseWriter{collect: func(ev *pyjson.Object) {
		typ, _ := ev.Get("type")
		switch typ {
		case "done":
			t, _ := ev.Get("full_text")
			c, _ := ev.Get("conversation_id")
			res.Text, _ = t.(string)
			res.ConversationID, _ = c.(string)
		case "error":
			t, _ := ev.Get("message")
			failure, _ = t.(string)
		}
	}}
	m.chatStream(ctx, sse, u, row, req)
	if failure != "" {
		return ChatTextResult{}, errors.New(failure)
	}
	return res, nil
}

// errText is the text of a handler error as the HTTP layer would send it.
func (m *Module) errText(err error) string {
	var se *statusErr
	if errors.As(err, &se) {
		if d, ok := se.detail.(string); ok {
			return d
		}
	}
	return err.Error()
}
