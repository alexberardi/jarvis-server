package cc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Chat images (docs/cc/chat-images.md). A mobile chat turn may carry images; the live slot
// sees them as OpenAI-style image_url parts for the whole turn (tool-loop continues included).
// After the reply, a job has a vision slot (background, else live) describe each image, and
// the description replaces the image in the cached conversation (CI3/CI5): follow-ups keep
// the gist without re-paying the image's tokens; tools keep the bytes (conversation.photos,
// CI8) until the conversation expires. Images live only in memory, in the cached
// conversation: never in the queue payload, a trace, a transcript or a log.

const (
	chatMaxImages     = 4
	chatMaxImageBytes = 2 << 20 // decoded
	// chatBodyMax is the chat request cap: the usual body plus the images' base64.
	chatBodyMax = httpx.MaxBody + chatMaxImages*((chatMaxImageBytes+2)/3*4+256)

	codeImagesUnavailable = "images_unavailable"
	codeImagesInvalid     = "images_invalid"

	chatDescribeJob = "cc.chat_image_describe"
	// imageNotDescribed stands in for an image no vision slot could describe.
	imageNotDescribed = "not described"
	describeMaxTokens = 200
	describeMaxRunes  = 600
)

// describeImagePrompt asks for the short factual description that replaces an image in
// history (CI3).
const describeImagePrompt = "Describe this image in one to three short, factual sentences for a conversation " +
	"history: what it shows, any readable text (verbatim, briefly), names, dates, numbers and quantities. " +
	"No preamble, no speculation."

// chatImage is one attached image as the data URL the live slot gets.
type chatImage struct {
	mime    string
	dataURL string
}

// imageContent is a user message with images: the image parts, then the text.
func imageContent(imgs []chatImage, text string) *llm.Content {
	parts := make([]llm.Part, 0, len(imgs)+1)
	for _, im := range imgs {
		parts = append(parts, llm.Part{Type: "image_url", ImageURL: &llm.ImageURL{URL: im.dataURL}})
	}
	parts = append(parts, llm.Part{Type: "text", Text: text})
	return &llm.Content{Parts: parts}
}

// sniffImage names an image's type from its bytes: jpeg, png or webp, else "".
func sniffImage(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// parseChatImages validates the request's images (§6): a list of at most chatMaxImages
// {"mime", "data"} objects whose base64 data decodes to at most chatMaxImageBytes of jpeg, png
// or webp (sniffed from the bytes; the declared mime is not trusted). The error is the 422
// detail.
func parseChatImages(raw []any) ([]chatImage, error) {
	if len(raw) > chatMaxImages {
		return nil, fmt.Errorf("at most %d images per message", chatMaxImages)
	}
	out := make([]chatImage, 0, len(raw))
	for i, v := range raw {
		o, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("image %d: expected an object with mime and data", i+1)
		}
		if mv, present := o["mime"]; present {
			if _, isStr := mv.(string); !isStr {
				return nil, fmt.Errorf("image %d: mime must be a string", i+1)
			}
		}
		data, ok := o["data"].(string)
		if !ok || data == "" {
			return nil, fmt.Errorf("image %d: data must be a base64 string", i+1)
		}
		if strings.HasPrefix(data, "data:") { // a data URL is accepted too
			if j := strings.Index(data, ","); j >= 0 {
				data = data[j+1:]
			}
		}
		if base64.StdEncoding.DecodedLen(len(data)) > chatMaxImageBytes+3 {
			return nil, fmt.Errorf("image %d: larger than %d bytes", i+1, chatMaxImageBytes)
		}
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("image %d: invalid base64", i+1)
		}
		if len(b) > chatMaxImageBytes {
			return nil, fmt.Errorf("image %d: larger than %d bytes", i+1, chatMaxImageBytes)
		}
		mime := sniffImage(b)
		if mime == "" {
			return nil, fmt.Errorf("image %d: not a JPEG, PNG or WebP image", i+1)
		}
		out = append(out, chatImage{mime: mime, dataURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)})
	}
	return out, nil
}

// endpoint resolves a slot's engine for its capabilities (vision, context size); ok=false
// without a resolver or when the slot can't serve.
func (m *Module) endpoint(ctx context.Context, label string) (llm.Endpoint, bool) {
	if m.Endpoints == nil {
		return llm.Endpoint{}, false
	}
	ep, err := m.Endpoints(ctx, label)
	return ep, err == nil
}

// slotVision reports whether a slot's running endpoint accepts images.
func (m *Module) slotVision(ctx context.Context, label string) bool {
	ep, ok := m.endpoint(ctx, label)
	return ok && ep.Vision
}

// handleChatCapabilities is GET /mobile/chat/capabilities (§6): the app shows its image
// picker only when images is true.
func (m *Module) handleChatCapabilities(w http.ResponseWriter, r *http.Request, _ authn.User) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"images":          m.LLM != nil && m.slotVision(r.Context(), llm.LabelLive),
		"max_images":      chatMaxImages,
		"max_image_bytes": chatMaxImageBytes,
	})
}

// --- jobs on a cached conversation ---

// convJob is a conversation job's queue payload: ids only, never content (the conversation
// itself is in memory; after a restart the job finds nothing and does nothing).
type convJob struct {
	ConversationID string `json:"conversation_id"`
	MessageID      uint64 `json:"message_id,omitempty"`
}

// registerConvJobs registers the description and compaction jobs.
func (m *Module) registerConvJobs(q *queue.Queue) {
	run := func(fn func(ctx context.Context, j convJob)) queue.HandlerFunc {
		return func(ctx context.Context, qj queue.Job) ([]byte, error) {
			var j convJob
			if err := json.Unmarshal(qj.Payload, &j); err != nil {
				return nil, queue.Permanent(err)
			}
			fn(ctx, j)
			return nil, nil
		}
	}
	q.Register(chatDescribeJob, queue.Handler{Run: run(m.describeImages), MaxAttempts: 1, Lease: 10 * time.Minute})
	q.Register(convCompactJob, queue.Handler{Run: run(m.compactJob), MaxAttempts: 1, Lease: 10 * time.Minute})
}

// scheduleConvJob queues a conversation job (dedup collapses repeats). Without a queue (a
// queue-less embed) it runs in a goroutine; tests capture jobs through convJobHook.
func (m *Module) scheduleConvJob(jobType string, j convJob, dedup string) {
	if m.convJobHook != nil {
		m.convJobHook(jobType, j)
		return
	}
	if m.deps.Queue == nil {
		fn := m.describeImages
		if jobType == convCompactJob {
			fn = m.compactJob
		}
		go fn(context.Background(), j)
		return
	}
	payload, _ := json.Marshal(j)
	if _, err := m.deps.Queue.Enqueue(context.Background(), jobType, payload, queue.Options{DedupKey: dedup}); err != nil &&
		!errors.Is(err, queue.ErrDuplicate) {
		m.deps.Log.Warn("cc: conversation job not queued", "type", jobType, "conversation_id", j.ConversationID, "err", err)
	}
}

// scheduleDescribe queues the description of a chat turn's images (after the reply, CI5).
func (m *Module) scheduleDescribe(cid string, msgID uint64) {
	m.scheduleConvJob(chatDescribeJob, convJob{ConversationID: cid, MessageID: msgID}, fmt.Sprintf("%s:%s:%d", chatDescribeJob, cid, msgID))
}

// findMsg returns the index of the message with id, or -1.
func findMsg(msgs []chatMsg, id uint64) int {
	for i := range msgs {
		if msgs[i].id == id {
			return i
		}
	}
	return -1
}

// describeLabels lists the slots that may describe images, in order (CI5): background when it
// has vision, then live when it has; none means "[image N: not described]".
func (m *Module) describeLabels(ctx context.Context) []string {
	var out []string
	for _, l := range []string{llm.LabelBackground, llm.LabelLive} {
		if m.slotVision(ctx, l) {
			out = append(out, l)
		}
	}
	return out
}

// describeImages is the description job: it reads the images from the cached conversation,
// asks a vision slot to describe each, and replaces the message's images with
// "[image N: …]" text. The conversation is unlocked during the LLM calls; a turn arriving
// meanwhile still sends the real images. The swap matches the message by id, so newer
// history is never touched.
func (m *Module) describeImages(ctx context.Context, j convJob) {
	conv := m.convs.peek(j.ConversationID)
	if conv == nil || m.LLM == nil {
		return
	}
	conv.mu.Lock()
	i := findMsg(conv.messages, j.MessageID)
	var imgs []chatImage
	if i >= 0 {
		imgs = conv.messages[i].images
	}
	conv.mu.Unlock()
	if len(imgs) == 0 {
		return
	}

	labels := m.describeLabels(ctx)
	var b strings.Builder
	described := 0
	used := ""
	for n, im := range imgs {
		desc := ""
		for _, l := range labels {
			d, err := m.describeImage(ctx, l, im)
			if err == nil && d != "" {
				desc, used = d, l
				break
			}
			if err != nil {
				m.deps.Log.Warn("cc: image description failed", "conversation_id", conv.id, "slot", l, "err", redactErr(err))
			}
		}
		if desc == "" {
			desc = imageNotDescribed
		} else {
			described++
		}
		if n > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[image %d: %s]", n+1, desc)
	}

	conv.mu.Lock()
	defer conv.mu.Unlock()
	i = findMsg(conv.messages, j.MessageID)
	if i < 0 || len(conv.messages[i].images) == 0 {
		return // compacted or trimmed away meanwhile
	}
	msgs := cloneMsgs(conv.messages)
	msg := msgs[i]
	msg.images = nil
	if msg.Content == "" {
		msg.Content = b.String()
	} else {
		msg.Content = b.String() + "\n" + msg.Content
	}
	msgs[i] = msg
	conv.messages = msgs
	conv.rev++
	m.deps.Log.Info("cc: chat images described", "conversation_id", conv.id, "images", len(imgs),
		"described", described, "slot", used)
}

// describeImage asks one slot for an image's description.
func (m *Module) describeImage(ctx context.Context, label string, im chatImage) (string, error) {
	maxTok := describeMaxTokens
	temp := 0.2
	zero := 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{Label: label, MaxTokens: &maxTok, Temperature: &temp, ReasoningBudget: &zero,
		Messages: []llm.Message{{Role: "user", Content: imageContent([]chatImage{im}, describeImagePrompt)}}})
	if err != nil {
		return "", err
	}
	return cleanDescription(resp.Content), nil
}

// cleanDescription drops reasoning, folds whitespace and caps the length; brackets would
// garble the "[image N: …]" marker, so they become parentheses.
func cleanDescription(s string) string {
	s = thinkCaptureRE.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(parse.PyStrip(s)), " ")
	s = strings.NewReplacer("[", "(", "]", ")").Replace(s)
	if r := []rune(s); len(r) > describeMaxRunes {
		s = string(r[:describeMaxRunes]) + "…"
	}
	return s
}

// hasImages reports whether any message carries images.
func hasImages(msgs []chatMsg) bool {
	for i := range msgs {
		if len(msgs[i].images) > 0 {
			return true
		}
	}
	return false
}

// visionGuard keeps a live call working when the live slot lost vision while images were
// still waiting for their description (the operator turned "Image input" off): the working
// copy gets "[image]" text instead of the parts the engine would refuse.
func (m *Module) visionGuard(ctx context.Context, msgs []chatMsg) []chatMsg {
	if !hasImages(msgs) || m.slotVision(ctx, llm.LabelLive) {
		return msgs
	}
	out := cloneMsgs(msgs)
	for i := range out {
		if n := len(out[i].images); n > 0 {
			out[i].Content = withImageMarkers(out[i].Content, n)
			out[i].images = nil
		}
	}
	return out
}

// withImageMarkers is what traces and transcripts record for a message with images: the
// text after one "[image]" per image, never the image data.
func withImageMarkers(text string, n int) string {
	if n == 0 {
		return text
	}
	return strings.TrimSpace(strings.Repeat("[image] ", n) + text)
}

// redactErr keeps an error loggable without image data: anything after a data URL is cut.
func redactErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "data:image/"); i >= 0 {
		return s[:i] + "[image]"
	}
	return s
}
