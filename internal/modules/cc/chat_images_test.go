package cc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// Test images: real magic bytes, then filler.
var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{9}, 64)...)
	webpBytes = append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{3}, 64)...)
	gifBytes  = append([]byte("GIF89a"), bytes.Repeat([]byte{1}, 64)...)
)

func img(mime string, b []byte) map[string]any {
	return map[string]any{"mime": mime, "data": base64.StdEncoding.EncodeToString(b)}
}

// jobLog captures the conversation jobs a module schedules, to run them when a test says.
type jobLog struct {
	mu   sync.Mutex
	jobs []struct {
		typ string
		j   convJob
	}
}

func (l *jobLog) hook(typ string, j convJob) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.jobs = append(l.jobs, struct {
		typ string
		j   convJob
	}{typ, j})
}

func (l *jobLog) of(typ string) []convJob {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []convJob
	for _, x := range l.jobs {
		if x.typ == typ {
			out = append(out, x.j)
		}
	}
	return out
}

// newImageChatEnv is a chat env whose live slot has vision and whose conversation jobs are
// captured.
func newImageChatEnv(t *testing.T, provider string) (*chatEnv, *jobLog) {
	t.Helper()
	ce := newChatEnv(t, provider)
	ce.eng.setVision(llm.LabelLive, true)
	jl := &jobLog{}
	ce.m.convJobHook = jl.hook
	return ce, jl
}

// imageParts returns the image_url URLs of a request's user messages.
func imageURLs(req map[string]any) []string {
	var out []string
	for _, m := range messagesOf(req) {
		parts, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			po := p.(map[string]any)
			if po["type"] == "image_url" {
				out = append(out, po["image_url"].(map[string]any)["url"].(string))
			}
		}
	}
	return out
}

func lastFrame(t *testing.T, frames []string) map[string]any {
	t.Helper()
	return event(t, frames[len(frames)-1])
}

func TestChatCapabilities(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ce.do("GET", "/api/v0/mobile/chat/capabilities", nil, nil).want(401)
	r := ce.do("GET", "/api/v0/mobile/chat/capabilities", nil, bearer("tok-7")).want(200).json()
	if r["images"] != false || r["max_images"] != 4.0 || r["max_image_bytes"] != 2097152.0 {
		t.Fatalf("capabilities without vision %v", r)
	}
	ce.eng.setVision(llm.LabelLive, true)
	if r := ce.do("GET", "/api/v0/mobile/chat/capabilities", nil, bearer("tok-7")).want(200).json(); r["images"] != true {
		t.Fatalf("capabilities with live vision %v", r)
	}
	// Background vision alone does not make chat images available: the live slot answers.
	ce.eng.setVision(llm.LabelLive, false)
	ce.eng.setVision(llm.LabelBackground, true)
	if r := ce.do("GET", "/api/v0/mobile/chat/capabilities", nil, bearer("tok-7")).want(200).json(); r["images"] != false {
		t.Fatalf("capabilities with background vision only %v", r)
	}
}

func TestChatImagesValidation(t *testing.T) {
	ce, _ := newImageChatEnv(t, prompts.Qwen3_8B)
	cid := ce.warm("tok-7")
	post := func(body map[string]any) *resp {
		b := map[string]any{"node_id": ce.node.id, "household_id": voiceHH, "conversation_id": cid}
		for k, v := range body {
			b[k] = v
		}
		return ce.do("POST", "/api/v0/mobile/chat", b, bearer("tok-7"))
	}
	code := func(r *resp, status int, want string) {
		t.Helper()
		r.want(status)
		j := r.json()
		if j["code"] != want || j["detail"] == "" {
			t.Fatalf("body %s, want code %s", r.body, want)
		}
	}
	five := []any{img("image/png", pngBytes), img("image/png", pngBytes), img("image/png", pngBytes), img("image/png", pngBytes), img("image/png", pngBytes)}
	code(post(map[string]any{"message": "hi", "images": five}), 422, codeImagesInvalid)
	big := append(append([]byte(nil), pngBytes...), make([]byte, chatMaxImageBytes)...)
	code(post(map[string]any{"message": "hi", "images": []any{img("image/png", big)}}), 422, codeImagesInvalid)
	code(post(map[string]any{"message": "hi", "images": []any{img("image/png", gifBytes)}}), 422, codeImagesInvalid) // sniffed, not declared
	code(post(map[string]any{"message": "hi", "images": []any{map[string]any{"mime": "image/png", "data": "!!!not base64"}}}), 422, codeImagesInvalid)
	code(post(map[string]any{"message": "hi", "images": []any{"just a string"}}), 422, codeImagesInvalid)
	code(post(map[string]any{"message": "hi", "images": []any{map[string]any{"mime": "image/png"}}}), 422, codeImagesInvalid)
	// Not a list: the usual 400 validation error.
	post(map[string]any{"message": "hi", "images": "x"}).want(400)
	// An empty message still needs images.
	post(map[string]any{"message": ""}).want(400)
	post(map[string]any{"images": []any{}}).want(400)

	// Each accepted type; an empty (or absent) message with an image is fine.
	for _, b := range [][]byte{jpegBytes, pngBytes, webpBytes} {
		ce.eng.say("It is an image.")
		frames := ce.chat("tok-7", map[string]any{"message": "", "conversation_id": cid, "images": []any{img("image/jpeg", b)}})
		if f := lastFrame(t, frames); f["type"] != "done" {
			t.Fatalf("frames %v", frames)
		}
	}
	ce.eng.say("Still an image.")
	frames := ce.chat("tok-7", map[string]any{"conversation_id": cid, "images": []any{img("image/png", pngBytes)}})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatalf("absent message: %v", frames)
	}
	// A body with four maximal images fits the raised limit.
	maxImg := append(append([]byte(nil), jpegBytes...), make([]byte, chatMaxImageBytes-len(jpegBytes))...)
	four := []any{img("image/jpeg", maxImg), img("image/jpeg", maxImg), img("image/jpeg", maxImg), img("image/jpeg", maxImg)}
	ce.eng.say("Four.")
	frames = ce.chat("tok-7", map[string]any{"message": "four", "conversation_id": cid, "images": four})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatalf("four maximal images: %v", frames)
	}

	// Vision off on the live slot: images_unavailable, before any validation.
	ce.eng.setVision(llm.LabelLive, false)
	code(post(map[string]any{"message": "hi", "images": []any{img("image/png", pngBytes)}}), 422, codeImagesUnavailable)
	code(post(map[string]any{"message": "hi", "images": five}), 422, codeImagesUnavailable)
	// Text-only chat is unaffected.
	ce.eng.say("Hello.")
	if f := lastFrame(t, ce.chat("tok-7", map[string]any{"message": "hi", "conversation_id": cid})); f["type"] != "done" {
		t.Fatal(f)
	}
}

// TestChatImagePartsReachLLM: the user message goes to the live slot as image_url parts plus
// the text, and stays that way through the tool loop's continue.
func TestChatImagePartsReachLLM(t *testing.T) {
	ce, jobs := newImageChatEnv(t, prompts.ChatGPT)
	ce.pub.toolReply = `{"output": {"success": true, "message": "Sunny, 20 degrees."}}`
	cid := ce.warm("tok-7")
	before := len(ce.eng.requests())
	ce.eng.push(engineReply{content: "", toolCalls: []map[string]any{{"id": "call_1", "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": `{"city": "Boston", "resolved_datetimes": ["2026-10-06T12:00:00"]}`}}}})
	ce.eng.say("It is sunny where that photo was taken.")
	frames := ce.chat("tok-7", map[string]any{"message": "what's the weather where this is?", "conversation_id": cid,
		"images": []any{img("image/png", pngBytes), img("image/jpeg", jpegBytes)}})
	if f := lastFrame(t, frames); f["type"] != "done" || f["full_text"] != "It is sunny where that photo was taken." {
		t.Fatalf("frames %v", frames)
	}
	reqs := ce.eng.requests()[before:]
	if len(reqs) != 2 {
		t.Fatalf("%d LLM requests, want the tool call and its continue", len(reqs))
	}
	wantURLs := []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes),
		"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)}
	for i, r := range reqs {
		urls := imageURLs(r)
		if strings.Join(urls, "|") != strings.Join(wantURLs, "|") {
			t.Fatalf("request %d image parts %d, want both images", i, len(urls))
		}
		var text string
		for _, m := range messagesOf(r) {
			if parts, ok := m["content"].([]any); ok {
				last := parts[len(parts)-1].(map[string]any)
				text, _ = last["text"].(string)
			}
		}
		if !strings.Contains(text, "what's the weather where this is?") {
			t.Fatalf("request %d text part %q", i, text)
		}
	}
	// The description is queued once, after the reply, with ids only.
	got := jobs.of(chatDescribeJob)
	if len(got) != 1 || got[0].ConversationID != cid || got[0].MessageID == 0 {
		t.Fatalf("describe jobs %+v", got)
	}
	payload, _ := json.Marshal(got[0])
	if strings.Contains(string(payload), "base64") || len(payload) > 100 {
		t.Fatalf("job payload %s", payload)
	}
}

// chatWithImage runs one image turn and returns the captured description job.
func chatWithImage(t *testing.T, ce *chatEnv, jobs *jobLog, cid, msg, answer string) convJob {
	t.Helper()
	ce.eng.say(answer)
	frames := ce.chat("tok-7", map[string]any{"message": msg, "conversation_id": cid, "images": []any{img("image/png", pngBytes)}})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatalf("frames %v", frames)
	}
	got := jobs.of(chatDescribeJob)
	return got[len(got)-1]
}

func userMessages(conv *conversation) []chatMsg {
	conv.mu.Lock()
	defer conv.mu.Unlock()
	var out []chatMsg
	for _, m := range conv.messages {
		if m.Role == "user" {
			out = append(out, m)
		}
	}
	return out
}

func TestChatImageDescriptionReplacesParts(t *testing.T) {
	ce, jobs := newImageChatEnv(t, prompts.Qwen3_8B)
	ce.eng.setVision(llm.LabelBackground, true)
	cid := ce.warm("tok-7")
	j := chatWithImage(t, ce, jobs, cid, "what is this?", "A mug.")
	before := len(ce.eng.requests())
	ce.eng.say("<think>looking</think>A red mug [with a] chipped handle.")
	ce.m.describeImages(context.Background(), j)
	reqs := ce.eng.requests()[before:]
	if len(reqs) != 1 || reqs[0]["model"] != "fake-background.gguf" || len(imageURLs(reqs[0])) != 1 {
		t.Fatalf("description requests %v", reqs)
	}
	um := userMessages(ce.m.convs.get(cid))
	last := um[len(um)-1]
	if len(last.images) != 0 || !strings.HasPrefix(last.Content, "[image 1: A red mug (with a) chipped handle.]\n") ||
		!strings.Contains(last.Content, "what is this?") {
		t.Fatalf("described message %q (images %d)", last.Content, len(last.images))
	}
	// The next turn sends text only.
	ce.eng.say("Yes, red.")
	ce.chat("tok-7", map[string]any{"message": "is it red?", "conversation_id": cid})
	r := ce.eng.last()
	if len(imageURLs(r)) != 0 || !strings.Contains(fmtMessages(r), "[image 1: A red mug") {
		t.Fatalf("follow-up request %s", fmtMessages(r))
	}
	// Running the job again is a no-op.
	n := len(ce.eng.requests())
	ce.m.describeImages(context.Background(), j)
	if len(ce.eng.requests()) != n {
		t.Fatal("a described message was described again")
	}
}

func fmtMessages(req map[string]any) string {
	b, _ := json.Marshal(req["messages"])
	return string(b)
}

func TestChatImageDescriptionFallbackOrder(t *testing.T) {
	t.Run("background without vision: live describes", func(t *testing.T) {
		ce, jobs := newImageChatEnv(t, prompts.Qwen3_8B)
		cid := ce.warm("tok-7")
		j := chatWithImage(t, ce, jobs, cid, "what is this?", "A cat.")
		n := len(ce.eng.requests())
		ce.eng.say("A grey cat on a sofa.")
		ce.m.describeImages(context.Background(), j)
		reqs := ce.eng.requests()[n:]
		if len(reqs) != 1 || reqs[0]["model"] != "fake.gguf" {
			t.Fatalf("requests %v", reqs)
		}
		if um := userMessages(ce.m.convs.get(cid)); !strings.HasPrefix(um[len(um)-1].Content, "[image 1: A grey cat on a sofa.]") {
			t.Fatal(um[len(um)-1].Content)
		}
	})
	t.Run("background fails: live describes", func(t *testing.T) {
		ce, jobs := newImageChatEnv(t, prompts.Qwen3_8B)
		ce.eng.setVision(llm.LabelBackground, true)
		cid := ce.warm("tok-7")
		j := chatWithImage(t, ce, jobs, cid, "what is this?", "A cat.")
		n := len(ce.eng.requests())
		ce.eng.push(engineReply{status: 500, content: "boom"}, engineReply{content: "A grey cat."})
		ce.m.describeImages(context.Background(), j)
		reqs := ce.eng.requests()[n:]
		if len(reqs) != 2 || reqs[0]["model"] != "fake-background.gguf" || reqs[1]["model"] != "fake.gguf" {
			t.Fatalf("requests %d", len(reqs))
		}
	})
	t.Run("no vision anywhere: a placeholder", func(t *testing.T) {
		ce, jobs := newImageChatEnv(t, prompts.Qwen3_8B)
		cid := ce.warm("tok-7")
		j := chatWithImage(t, ce, jobs, cid, "what is this?", "A cat.")
		ce.eng.setVision(llm.LabelLive, false) // turned off since the turn
		n := len(ce.eng.requests())
		ce.m.describeImages(context.Background(), j)
		if len(ce.eng.requests()) != n {
			t.Fatal("called an LLM without vision")
		}
		um := userMessages(ce.m.convs.get(cid))
		if last := um[len(um)-1]; len(last.images) != 0 || !strings.HasPrefix(last.Content, "[image 1: not described]") {
			t.Fatal(last.Content)
		}
	})
}

// TestChatImageNoClobber: a turn arriving before the description still sends the image, and
// the description, finishing during a later turn, replaces only its own message.
func TestChatImageNoClobber(t *testing.T) {
	ce, jobs := newImageChatEnv(t, prompts.Qwen3_8B)
	ce.eng.setVision(llm.LabelBackground, true)
	cid := ce.warm("tok-7")
	j := chatWithImage(t, ce, jobs, cid, "what is this?", "A mug.")

	// A turn before the job ran: the image is still there.
	ce.eng.say("It holds coffee.")
	ce.chat("tok-7", map[string]any{"message": "what's in it?", "conversation_id": cid})
	if len(imageURLs(ce.eng.last())) != 1 {
		t.Fatal("a turn before the description lost the image")
	}

	// The job's LLM call blocks until a third turn has committed.
	release := make(chan struct{})
	started := make(chan struct{})
	ce.eng.mu.Lock()
	ce.eng.respond = func(body map[string]any) (engineReply, bool) {
		if mt, _ := body["max_tokens"].(float64); mt == describeMaxTokens {
			close(started)
			<-release
			return engineReply{content: "A white mug."}, true
		}
		return engineReply{}, false
	}
	ce.eng.mu.Unlock()
	done := make(chan struct{})
	go func() { ce.m.describeImages(context.Background(), j); close(done) }()
	<-started
	ce.eng.say("Third answer.")
	ce.chat("tok-7", map[string]any{"message": "third question", "conversation_id": cid})
	if len(imageURLs(ce.eng.last())) != 1 {
		t.Fatal("a turn during the description lost the image")
	}
	close(release)
	<-done

	conv := ce.m.convs.get(cid)
	conv.mu.Lock()
	all := cloneMsgs(conv.messages)
	conv.mu.Unlock()
	var contents []string
	for _, m := range all {
		if !m.transient && m.Role != "system" {
			contents = append(contents, m.Role+":"+firstLine(m.Content))
		}
	}
	joined := strings.Join(contents, "|")
	for _, want := range []string{"[image 1: A white mug.]", "assistant:A mug.", "assistant:It holds coffee.", "assistant:Third answer."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("history %s lacks %q", joined, want)
		}
	}
	for _, m := range all {
		if len(m.images) != 0 {
			t.Fatal("image left in history")
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestChatImageNothingInTracesOrLogs: through the real queue, images never reach the job
// payload, the request trace, the transcript or the log; the description text isn't logged.
func TestChatImageNothingInTracesOrLogs(t *testing.T) {
	var logBuf safeBuffer
	withEnvExtra(t, envExtra{log: &logBuf, queue: true})
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ce.eng.setVision(llm.LabelLive, true)
	ce.eng.setVision(llm.LabelBackground, true)
	cid := ce.warm("tok-7")
	ce.eng.say("A mug.")
	ce.eng.mu.Lock()
	ce.eng.respond = func(body map[string]any) (engineReply, bool) {
		if mt, _ := body["max_tokens"].(float64); mt == describeMaxTokens {
			return engineReply{content: "A distinctive teal teapot."}, true
		}
		return engineReply{}, false
	}
	ce.eng.mu.Unlock()
	frames := ce.chat("tok-7", map[string]any{"message": "", "conversation_id": cid, "images": []any{img("image/png", pngBytes)}})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatal(frames)
	}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	// The job runs from the queue; its payload holds ids only.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		var payload []byte
		err := ce.d.Read.QueryRow(`SELECT state, payload FROM platform_jobs WHERE type = ?`, chatDescribeJob).Scan(&state, &payload)
		if err == nil && strings.Contains(string(payload), "base64") || strings.Contains(string(payload), b64) {
			t.Fatalf("job payload %s", payload)
		}
		if err == nil && state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("description job not done: %v %s", err, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if um := userMessages(ce.m.convs.get(cid)); !strings.HasPrefix(um[len(um)-1].Content, "[image 1: A distinctive teal teapot.]") {
		t.Fatal(um[len(um)-1].Content)
	}
	// The trace (written asynchronously) records the image as a marker.
	var userCmd, spans string
	for time.Now().Before(deadline) {
		err := ce.d.Read.QueryRow(`SELECT COALESCE(user_command, ''), spans_json FROM cc_request_traces WHERE conversation_id = ? AND request_type = 'mobile_chat'`, cid).Scan(&userCmd, &spans)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if userCmd != "[image]" || strings.Contains(spans, b64) || strings.Contains(spans, "data:image") {
		t.Fatalf("trace user_command %q spans %s", userCmd, spans)
	}
	logs := logBuf.String()
	if strings.Contains(logs, b64) || strings.Contains(logs, "data:image") || strings.Contains(logs, "teal teapot") {
		t.Fatalf("logs leak image data or its description:\n%s", logs)
	}
	if !strings.Contains(logs, "cc: chat images described") {
		t.Fatalf("no description log line:\n%s", logs)
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSniffImage(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		want string
	}{{pngBytes, "image/png"}, {jpegBytes, "image/jpeg"}, {webpBytes, "image/webp"}, {gifBytes, ""}, {nil, ""}, {[]byte("RIFF"), ""}} {
		if got := sniffImage(c.b); got != c.want {
			t.Errorf("sniff %q = %q, want %q", c.b[:min(len(c.b), 12)], got, c.want)
		}
	}
}
