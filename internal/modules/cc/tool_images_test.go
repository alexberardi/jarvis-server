package cc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
)

// Images attached to client tool results (tool_images.go): desktop_read_screen's screenshot
// reaches the live model as an image on that tool message, for that turn only, and is never
// stored.

const screenTool = `[{"type": "function", "function": {"name": "desktop_read_screen", "description": "Look at the screen",
	"parameters": {"type": "object", "properties": {"scope": {"type": "string"}}, "required": []}}}]`

func screenshotResult(id string, b []byte) map[string]any {
	return map[string]any{"tool_call_id": id, "output": map[string]any{"success": true, "scope": "window",
		"window_title": "build.log", "image_attachments": []any{map[string]any{"mime": "image/jpeg",
			"data": base64.StdEncoding.EncodeToString(b), "label": `screenshot of the window "build.log"`}}}}
}

// toolImageParts returns the image_url parts of the tool message answering id in a request.
func toolImageParts(req map[string]any, id string) (parts []map[string]any, text string, found bool) {
	for _, m := range messagesOf(req) {
		if m["role"] != "tool" || m["tool_call_id"] != id {
			continue
		}
		found = true
		switch c := m["content"].(type) {
		case string:
			text = c
		case []any:
			for _, p := range c {
				pm := p.(map[string]any)
				if pm["type"] == "image_url" {
					parts = append(parts, pm)
				} else {
					text, _ = pm["text"].(string)
				}
			}
		}
	}
	return parts, text, found
}

func readScreenTurn(t *testing.T, ve *voiceEnv, cid string) {
	t.Helper()
	ve.start(cid, screenTool)
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_s1", "type": "function",
		"function": map[string]any{"name": "desktop_read_screen", "arguments": `{"scope":"window"}`}}}})
	r := ve.turn("/api/v0/voice/command", cid, "what does this error say", nil).want(200).json()
	if r["stop_reason"] != "tool_calls" {
		t.Fatalf("expected the screen tool call: %v", r)
	}
}

func TestToolResultImageReachesTheModelThenItsDescription(t *testing.T) {
	var logBuf safeBuffer
	withEnvExtra(t, envExtra{log: &logBuf})
	jl := &jobLog{}
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B, func(m *Module) { m.convJobHook = jl.hook })
	ve.eng.setVision(llm.LabelLive, true)
	readScreenTurn(t, ve, "c1")
	// A real screenshot is bigger than the usual 1 MiB body cap.
	shot := append(append([]byte{}, jpegBytes...), bytes.Repeat([]byte{5}, 1<<20)...)
	ve.eng.say("It says the build failed on line 12.")
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c1",
		"tool_results": []any{screenshotResult("call_s1", shot)}}, ve.node.h()).want(200).json()
	if b["assistant_message"] != "It says the build failed on line 12." {
		t.Fatalf("continue %v", b)
	}
	parts, text, found := toolImageParts(ve.eng.last(), "call_s1")
	if !found || len(parts) != 1 {
		t.Fatalf("the tool message must carry the screenshot as an image part: found %v, parts %v", found, len(parts))
	}
	url := parts[0]["image_url"].(map[string]any)["url"].(string)
	if url != "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(shot) {
		t.Fatalf("image part %.60s…", url)
	}
	if strings.Contains(text, "base64") || !strings.Contains(text, `screenshot of the window \"build.log\"`+toolImageNote) {
		t.Fatalf("tool text %s", text)
	}
	// After the turn the describe job swaps the screenshot for a description in the tool
	// message (#576): a follow-up ("what colours was it?") has something to answer from.
	conv := ve.m.convs.get("c1")
	if conv.photos != nil {
		t.Fatal("a screenshot was kept as a chat photo")
	}
	js := jl.of(chatDescribeJob)
	if len(js) != 1 {
		t.Fatalf("describe jobs %v", js)
	}
	ve.eng.say("A build log in a dark terminal, red error text on line 12.")
	ve.m.describeImages(context.Background(), js[0])
	for _, m := range conv.messages {
		if len(m.images) > 0 {
			t.Fatalf("an image outlived its description in %s message %q", m.Role, m.Content)
		}
	}
	ve.eng.say("Red.")
	ve.turn("/api/v0/voice/command", "c1", "what colour was the error", map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1}).want(200)
	if parts, text, found := toolImageParts(ve.eng.last(), "call_s1"); !found || len(parts) != 0 ||
		!strings.Contains(text, "[image 1: A build log in a dark terminal, red error text") || !strings.Contains(text, "build.log") {
		t.Fatalf("next turn: found %v, %d image parts, text %s", found, len(parts), text)
	}
	b64 := base64.StdEncoding.EncodeToString(shot)
	if logs := logBuf.String(); strings.Contains(logs, b64[:200]) || strings.Contains(logs, "data:image") {
		t.Fatal("logs leak the screenshot")
	}
	for _, tr := range ve.transcripts() {
		if s, _ := json.Marshal(tr); bytes.Contains(s, []byte(b64[:200])) {
			t.Fatal("a transcript holds the screenshot")
		}
	}
}

// The text path drops role=tool; the screenshot rides on its formatting message, and the
// description replaces it there (not kept as a chat photo).
func TestToolResultImageTextPathIsDescribed(t *testing.T) {
	jl := &jobLog{}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.convJobHook = jl.hook })
	ve.eng.setVision(llm.LabelLive, true)
	ve.start("t1", screenTool)
	ve.eng.say(`<tool_call>{"name": "desktop_read_screen", "arguments": {"scope": "window"}}</tool_call>`)
	r := ve.turn("/api/v0/voice/command", "t1", "take a screenshot of the docs", nil).want(200).json()
	id := r["tool_calls"].([]any)[0].(map[string]any)["id"].(string)
	ve.eng.say("It's the Jarvis docs home page.")
	ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "t1",
		"tool_results": []any{screenshotResult(id, jpegBytes)}}, ve.node.h()).want(200)
	conv := ve.m.convs.get("t1")
	if conv.photos != nil {
		t.Fatal("a screenshot was kept as a chat photo")
	}
	js := jl.of(chatDescribeJob)
	if len(js) != 1 {
		t.Fatalf("describe jobs %v", js)
	}
	ve.eng.say("A docs page with a navy header and orange links.")
	ve.m.describeImages(context.Background(), js[0])
	found := false
	for _, m := range conv.messages {
		if len(m.images) > 0 {
			t.Fatalf("an image outlived its description in %s message %q", m.Role, m.Content)
		}
		found = found || strings.Contains(m.Content, "[image 1: A docs page with a navy header and orange links.]")
	}
	if !found {
		t.Fatalf("no description in history %v", conv.messages)
	}
}

type fakeOCR struct {
	text  string
	err   error
	calls int
}

func (f *fakeOCR) Recognize(_ context.Context, imgs []ocr.Image, _ ocr.Options, engines []string) ([]ocr.Reading, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var rs []ocr.ImageReading
	for i := range imgs {
		rs = append(rs, ocr.ImageReading{Index: i, Text: f.text})
	}
	return []ocr.Reading{{Engine: "tesseract", Results: rs}}, nil
}

func TestToolResultImageWithoutVisionIsOCRText(t *testing.T) {
	fo := &fakeOCR{text: "error: build failed at line 12"}
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B, func(m *Module) { m.OCR = fo })
	ve.eng.setVision(llm.LabelLive, false)
	readScreenTurn(t, ve, "c2")
	ve.eng.say("The build failed at line 12.")
	ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c2",
		"tool_results": []any{screenshotResult("call_s1", jpegBytes)}}, ve.node.h()).want(200)
	parts, text, found := toolImageParts(ve.eng.last(), "call_s1")
	if !found || len(parts) != 0 || fo.calls != 1 {
		t.Fatalf("no vision: found %v, %d image parts, %d OCR calls", found, len(parts), fo.calls)
	}
	if !strings.Contains(text, `"image_text": "error: build failed at line 12"`) || !strings.Contains(text, "can't see images") {
		t.Fatalf("tool text %s", text)
	}
	// History keeps the OCR text for a follow-up (#576).
	ve.eng.say("Line 12.")
	ve.turn("/api/v0/voice/command", "c2", "which line was it", map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1}).want(200)
	if _, text, _ := toolImageParts(ve.eng.last(), "call_s1"); !strings.Contains(text, `"image_text": "error: build failed at line 12"`) {
		t.Fatalf("next turn tool text %s", text)
	}
	// No OCR either: the model is told, so it can tell the user.
	ve2 := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve2.eng.setVision(llm.LabelLive, false)
	readScreenTurn(t, ve2, "c3")
	ve2.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c3",
		"tool_results": []any{screenshotResult("call_s1", jpegBytes)}}, ve2.node.h()).want(200)
	if _, text, _ := toolImageParts(ve2.eng.last(), "call_s1"); !strings.Contains(text, "no OCR engine") {
		t.Fatalf("tool text without OCR %s", text)
	}
}

func TestLiftToolImages(t *testing.T) {
	out, err := pyjson.Loads(`{"success": true, "image_attachments": [
		{"mime": "image/jpeg", "data": "` + base64.StdEncoding.EncodeToString(jpegBytes) + `", "label": "shot"},
		{"data": "data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngBytes) + `"},
		{"data": "` + base64.StdEncoding.EncodeToString(gifBytes) + `"},
		{"data": "!!!"},
		"nope"], "after": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	clean, imgs := liftToolImages(out)
	if len(imgs) != 2 || imgs[0].mime != "image/jpeg" || imgs[1].mime != "image/png" {
		t.Fatalf("lifted %d images: %+v", len(imgs), imgs)
	}
	got := pyjson.Dumps(clean, true)
	want := `{"success": true, "image_attachments": ["shot` + toolImageNote + `", "image 2` + toolImageNote +
		`", "[image dropped: not a jpeg, png or webp image]", "[image dropped: not base64]", "[image dropped: not an object]"], "after": 1}`
	if got != want {
		t.Fatalf("clean output\n got %s\nwant %s", got, want)
	}
	if o, imgs := liftToolImages(servertools0()); len(imgs) != 0 || pyjson.Dumps(o, true) != `{"temp": 80}` {
		t.Fatal("an output without attachments changed")
	}
	if _, imgs := liftToolImages("plain string"); imgs != nil {
		t.Fatal("a string output lifted images")
	}
	big := base64.StdEncoding.EncodeToString(append(append([]byte{}, jpegBytes...), make([]byte, chatMaxImageBytes)...))
	o, _ := pyjson.Loads(`{"image_attachments": [{"data": "` + big + `"}]}`)
	if clean, imgs := liftToolImages(o); len(imgs) != 0 || !strings.Contains(pyjson.Dumps(clean, true), "too large") {
		t.Fatal("an oversized image was accepted")
	}
}

// TestLiftImagesLimits: the errand lift keeps liftToolImages' limits (at most 4, at most
// 2 MiB, a sniffed type), returns the decoded bytes, and leaves labels, never data, behind.
func TestLiftImagesLimits(t *testing.T) {
	jpeg := base64.StdEncoding.EncodeToString(jpegBytes)
	big := base64.StdEncoding.EncodeToString(append(append([]byte{}, jpegBytes...), make([]byte, chatMaxImageBytes)...))
	out, err := pyjson.Loads(`{"success": true, "image_attachments": [
		{"data": "` + jpeg + `", "label": "one"},
		{"data": "data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngBytes) + `", "label": "two"},
		{"data": "` + big + `", "label": "huge"},
		{"data": "` + base64.StdEncoding.EncodeToString(gifBytes) + `"},
		{"data": "` + jpeg + `"},
		{"data": "` + jpeg + `", "label": "four"},
		{"data": "` + jpeg + `", "label": "five"}], "after": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	clean, imgs, err := liftImages(out.(*pyjson.Object))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 4 || imgs[0].MIME != "image/jpeg" || imgs[1].MIME != "image/png" || imgs[0].Label != "one" ||
		imgs[2].Label != "image 5" || imgs[3].Label != "four" || !bytes.Equal(imgs[0].Data, jpegBytes) || !bytes.Equal(imgs[1].Data, pngBytes) {
		t.Fatalf("lifted %+v", imgs)
	}
	got := pyjson.Dumps(clean, true)
	want := `{"success": true, "image_attachments": ["one` + toolImageNote + `", "two` + toolImageNote +
		`", "[image dropped: too large]", "[image dropped: not a jpeg, png or webp image]", "image 5` + toolImageNote +
		`", "four` + toolImageNote + `", "[image dropped: more than 4 attached]"], "after": 1}`
	if got != want {
		t.Fatalf("clean output\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, jpeg[:16]) {
		t.Fatal("image data left in the output")
	}
	if o, imgs, err := liftImages(servertools0()); err != nil || imgs != nil || pyjson.Dumps(o, true) != `{"temp": 80}` {
		t.Fatal("an output without attachments changed")
	}
	bad, _ := pyjson.Loads(`{"image_attachments": "` + jpeg + `"}`)
	if o, imgs, err := liftImages(bad.(*pyjson.Object)); err == nil || imgs != nil || pyjson.Dumps(o, true) != `{"image_attachments": []}` {
		t.Fatalf("a non-list attachment kept: %s %v", pyjson.Dumps(o, true), err)
	}
}

func servertools0() *pyjson.Object {
	o := pyjson.NewObject()
	o.Set("temp", 80)
	return o
}

// TestChatToolResultImageReachesTheModel: a mobile chat turn whose node tool (over MQTT) attaches
// a screenshot shows it to the model; the generic redaction doesn't eat it.
func TestChatToolResultImageReachesTheModel(t *testing.T) {
	ce, jl := newImageChatEnv(t, prompts.Qwen3_5_9B)
	ce.pub.tools = `{"client_tools": ` + screenTool + `, "available_commands": [], "installed_packages": []}`
	res, _ := json.Marshal(map[string]any{"output": screenshotResult("x", jpegBytes)["output"]})
	ce.pub.toolReply = string(res)
	cid := ce.warm("tok-7")
	ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_c1", "type": "function",
		"function": map[string]any{"name": "desktop_read_screen", "arguments": `{}`}}}})
	ce.eng.say("It's a build log.")
	frames := ce.chat("tok-7", map[string]any{"message": "what's on my screen", "conversation_id": cid})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatal(frames)
	}
	parts, text, found := toolImageParts(ce.eng.last(), "call_c1")
	if !found || len(parts) != 1 || strings.Contains(text, "[image]\"") {
		t.Fatalf("chat: found %v, %d image parts, text %s", found, len(parts), text)
	}
	js := jl.of(chatDescribeJob)
	conv := ce.m.convs.get(cid)
	if len(js) != 1 || conv.messages[findMsg(conv.messages, js[0].MessageID)].Role != "tool" {
		t.Fatalf("the chat screenshot must be described: jobs %v", js)
	}
}
