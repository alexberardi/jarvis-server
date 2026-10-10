package cc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Image parameters on any tool (docs/cc/chat-images.md §9): node tools declare them with the
// SDK's marker; jarvisd swaps the model's photo numbers for the photos before the tool_call.

// photoNodeTools are a node's client tools in the SDK's shape: measure_photo has a required
// image parameter, annotate an optional one, plus the plain weather tool.
const photoNodeTools = `[{"type":"function","function":{"name":"measure_photo","description":"Report a photo's size.",` +
	`"parameters":{"type":"object","properties":{"photos":{"type":"array","items":{"type":"integer","minimum":1},` +
	`"maxItems":4,"description":"The photos to measure. Photo numbers: a list of integers.","x-jarvis-type":"image"},` +
	`"unit":{"type":"string","description":"Unit"}},"required":["photos"]}},"keywords":["size","dimensions"]},` +
	`{"type":"function","function":{"name":"annotate","description":"Save a note, optionally with photos.",` +
	`"parameters":{"type":"object","properties":{"text":{"type":"string","description":"Text"},"photo":{"type":"array",` +
	`"items":{"type":"integer","minimum":1},"maxItems":4,"description":"Photos","x-jarvis-type":"image"}},"required":["text"]}}},` +
	`{"type":"function","function":{"name":"get_weather","description":"Get the weather forecast.",` +
	`"parameters":{"type":"object","properties":{"city":{"type":"string","description":"City"}},"required":[]}}}]`

const photoNodeCommands = `[{"command_name":"measure_photo","description":"Report a photo's size.","parameters":[` +
	`{"name":"photos","type":"image","required":true},{"name":"unit","type":"string","required":false}],` +
	`"examples":[{"voice_command":"how big is this photo","expected_parameters":{"photos":[1]}}]},` +
	`{"command_name":"get_weather","description":"Weather.","parameters":[{"name":"city","type":"string"}]}]`

// newPhotoToolEnv is a chat env whose node reports photo tools; vision as given.
func newPhotoToolEnv(t *testing.T, provider string, vision bool) (*chatEnv, *jobLog) {
	t.Helper()
	ce, jl := newImageChatEnv(t, provider)
	ce.eng.setVision(llm.LabelLive, vision)
	ce.pub.mu.Lock()
	ce.pub.tools = `{"client_tools": ` + photoNodeTools + `, "available_commands": ` + photoNodeCommands + `, "installed_packages": []}`
	ce.pub.toolReply = `{"output": {"success": true, "message": "It is 64 by 48 pixels."}}`
	ce.pub.mu.Unlock()
	return ce, jl
}

func nodeCall(name, args string) engineReply {
	return engineReply{toolCalls: []map[string]any{{"id": "call_p1", "type": "function",
		"function": map[string]any{"name": name, "arguments": args}}}}
}

// wireArgsOf decodes a tool_call's arguments (an object with photos, or the model's JSON string).
func wireArgsOf(t *testing.T, d map[string]any) map[string]any {
	t.Helper()
	switch a := d["arguments"].(type) {
	case map[string]any:
		return a
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(a), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Fatalf("arguments %T", d["arguments"])
	return nil
}

func TestNodePhotoToolGetsPhotos(t *testing.T) {
	var logBuf safeBuffer
	withEnvExtra(t, envExtra{log: &logBuf})
	ce, _ := newPhotoToolEnv(t, prompts.Qwen3_5_9B, true)
	cid := ce.warm("tok-7")
	// Offered, with the marker stripped from what the LLM sees.
	warm := ce.eng.last()
	if names := toolNamesOf(warm); !has(names, "measure_photo") || !has(names, "annotate") {
		t.Fatalf("tools %v", names)
	}
	if raw, _ := json.Marshal(warm["tools"]); strings.Contains(string(raw), "x-jarvis-type") ||
		!strings.Contains(string(raw), `"minimum":1`) {
		t.Fatalf("tools sent to the LLM %s", raw)
	}

	ce.eng.push(nodeCall("measure_photo", `{"photos": [2, 1], "unit": "px"}`))
	ce.eng.say("The first is 64 by 48.")
	frames := ce.chat("tok-7", map[string]any{"message": "what size are these?", "conversation_id": cid,
		"images": []any{img("image/png", pngBytes), img("image/jpeg", jpegBytes)}})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatalf("frames %v", frames)
	}
	calls := ce.pub.commands("tool_call")
	if len(calls) != 1 {
		t.Fatalf("tool calls %v", calls)
	}
	args := wireArgsOf(t, calls[0])
	photos, _ := args["photos"].([]any)
	if len(photos) != 2 || args["unit"] != "px" {
		t.Fatalf("arguments %v", args)
	}
	for i, want := range []map[string]any{img("image/jpeg", jpegBytes), img("image/png", pngBytes)} {
		p := photos[i].(map[string]any)
		if p["mime"] != want["mime"] || p["data"] != want["data"] || len(p) != 2 {
			t.Fatalf("photo %d: %v", i+1, p)
		}
	}

	// Nothing but the node's tool_call holds the bytes: not the history, the model's view of
	// the call, the log or the trace.
	b64 := []string{base64.StdEncoding.EncodeToString(pngBytes), base64.StdEncoding.EncodeToString(jpegBytes)}
	conv := ce.m.convs.get(cid)
	conv.mu.Lock()
	var history strings.Builder
	for _, m := range conv.messages {
		history.WriteString(m.Content)
		for _, tc := range m.ToolCalls {
			history.WriteString(tc.Function.Arguments)
			if tc.Function.Name == "measure_photo" && tc.Function.Arguments != `{"photos": [2, 1], "unit": "px"}` {
				t.Errorf("history call args %s", tc.Function.Arguments)
			}
		}
	}
	conv.mu.Unlock()
	reqs := ce.eng.requests()
	cont := reqs[len(reqs)-1]
	raw, _ := json.Marshal(cont)
	var spans string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := ce.d.Read.QueryRow(`SELECT spans_json FROM cc_request_traces WHERE conversation_id = ? AND request_type = 'mobile_chat'`, cid).Scan(&spans)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if spans == "" {
		t.Fatal("no trace")
	}
	for _, b := range b64 {
		// The continue request still carries this turn's image parts (CI3); its tool calls
		// and replies must not.
		for _, m := range messagesOf(cont) {
			if m["role"] == "user" {
				continue
			}
			if s, _ := json.Marshal(m); strings.Contains(string(s), b) {
				t.Fatalf("LLM message carries image data: %s", s)
			}
		}
		if strings.Contains(history.String(), b) || strings.Contains(logBuf.String(), b) || strings.Contains(spans, b) {
			t.Fatal("image data in the history, the log or the trace")
		}
	}
	if !strings.Contains(string(raw), `"photos\": [2, 1]`) && !strings.Contains(string(raw), `[2, 1]`) {
		t.Fatalf("continue lost the model's call: %s", raw)
	}
}

// An absent image argument: every photo for a required parameter, none for an optional one.
func TestNodePhotoToolAbsentArgument(t *testing.T) {
	ce, _ := newPhotoToolEnv(t, prompts.Qwen3_5_9B, true)
	cid := ce.warm("tok-7")
	ce.eng.push(nodeCall("measure_photo", `{}`))
	ce.eng.say("Done.")
	ce.chat("tok-7", map[string]any{"message": "measure", "conversation_id": cid,
		"images": []any{img("image/png", pngBytes), img("image/jpeg", jpegBytes)}})
	ce.eng.push(nodeCall("annotate", `{"text": "milk"}`))
	ce.eng.say("Noted.")
	ce.chat("tok-7", map[string]any{"message": "note milk", "conversation_id": cid})
	calls := ce.pub.commands("tool_call")
	if len(calls) != 2 {
		t.Fatalf("tool calls %d", len(calls))
	}
	if p, _ := wireArgsOf(t, calls[0])["photos"].([]any); len(p) != 2 || p[0].(map[string]any)["mime"] != "image/png" {
		t.Fatalf("required absent: %v", calls[0]["arguments"])
	}
	if a := wireArgsOf(t, calls[1]); a["photo"] != nil || a["text"] != "milk" {
		t.Fatalf("optional absent: %v", a)
	}
	if _, isString := calls[1]["arguments"].(string); !isString {
		t.Fatal("a call without photos should keep the model's JSON string")
	}
}

// Refusals come back to the model as tool results, like a server tool's, and the node never
// gets the call.
func TestNodePhotoToolRefusals(t *testing.T) {
	for _, provider := range []string{prompts.Qwen3_5_9B, prompts.Qwen3_8B} { // native, text path
		t.Run(provider, func(t *testing.T) {
			ce, jobs := newPhotoToolEnv(t, provider, true)
			cid := ce.warm("tok-7")
			text := provider == prompts.Qwen3_8B
			// run makes the model call measure_photo with args; it returns what the model got
			// back (native: the tool reply) or, on the text path, the reply the user got: the
			// refusal's message through the formatting fast path (D23).
			run := func(args string, body map[string]any) string {
				t.Helper()
				if text {
					ce.eng.say(`<tool_call>{"name": "measure_photo", "arguments": ` + args + `}</tool_call>`)
				} else {
					ce.eng.push(nodeCall("measure_photo", args))
					ce.eng.say("Please attach the photo.")
				}
				body["conversation_id"] = cid
				frames := ce.chat("tok-7", body)
				if text {
					return lastFrame(t, frames)["full_text"].(string)
				}
				r := toolReplies(ce.eng.last())
				if len(r) == 0 {
					t.Fatalf("no tool reply: %s", fmtMessages(ce.eng.last()))
				}
				return r[len(r)-1]
			}
			code := func(native, spoken string) string {
				if text {
					return spoken
				}
				return native
			}

			if r := run(`{"photos": [1]}`, map[string]any{"message": "how big is it"}); !strings.Contains(r, code("no_image", "attach the photo")) {
				t.Fatalf("no photo: %s", r)
			}
			one := []any{img("image/png", pngBytes)}
			if r := run(`{"photos": [3]}`, map[string]any{"message": "how big", "images": one}); !strings.Contains(r, code("invalid_image", "")) ||
				!strings.Contains(r, "only image 1 is attached") {
				t.Fatalf("bad number: %s", r)
			}
			if r := run(`{"photos": ["first"]}`, map[string]any{"message": "how big", "images": one}); !strings.Contains(r, code("invalid_arguments", "list of image numbers")) {
				t.Fatalf("bad argument: %s", r)
			}
			// Described, and the kept bytes gone: expired.
			j := jobs.of(chatDescribeJob)
			ce.eng.say("A mug.")
			ce.m.describeImages(context.Background(), j[len(j)-1])
			conv := ce.m.convs.get(cid)
			conv.mu.Lock()
			conv.photos = nil
			conv.mu.Unlock()
			if r := run(`{"photos": [1]}`, map[string]any{"message": "how big was it"}); !strings.Contains(r, code("image_expired", "no longer available")) {
				t.Fatalf("expired: %s", r)
			}
			if n := len(ce.pub.commands("tool_call")); n != 0 {
				t.Fatalf("%d tool_calls reached the node", n)
			}
		})
	}
}

// Photo tools are offered only where a photo can arrive; the PHOTOS block follows any photo
// tool, server or node.
func TestPhotoToolsHidden(t *testing.T) {
	t.Run("chat without vision", func(t *testing.T) {
		ce, _ := newPhotoToolEnv(t, prompts.Qwen3_5_9B, false)
		ce.warm("tok-7")
		names := toolNamesOf(ce.eng.last())
		if has(names, "measure_photo") || has(names, "annotate") || !has(names, "get_weather") {
			t.Fatalf("tools %v", names)
		}
		if strings.Contains(fmtMessages(ce.eng.last()), "how big is this photo") {
			t.Fatal("a hidden tool's examples reached the prompt")
		}
	})
	t.Run("voice", func(t *testing.T) {
		ce, _ := newPhotoToolEnv(t, prompts.Qwen3_5_9B, true)
		ce.start("conv-voice", photoNodeTools, `"available_commands":`+photoNodeCommands)
		names := toolNamesOf(ce.eng.last())
		if has(names, "measure_photo") || has(names, "annotate") || !has(names, "get_weather") {
			t.Fatalf("voice tools %v", names)
		}
		// The model calls it anyway: refused, never handed to the node.
		ce.eng.push(nodeCall("measure_photo", `{"photos": [1]}`))
		ce.eng.say("I can't see photos here.")
		res := ce.turn("/api/v0/voice/command", "conv-voice", "how big is this photo", nil).want(200).json()
		if res["stop_reason"] != "complete" || res["tool_calls"] != nil && len(res["tool_calls"].([]any)) > 0 {
			t.Fatalf("voice result %v", res)
		}
		if r := toolReplies(ce.eng.last()); len(r) == 0 || !strings.Contains(r[0], "no_image") {
			t.Fatalf("voice refusal %v", r)
		}
	})
	t.Run("text path", func(t *testing.T) {
		ce, _ := newPhotoToolEnv(t, prompts.Qwen3_8B, false)
		ce.warm("tok-7")
		if s := fmtMessages(ce.eng.last()); strings.Contains(s, "measure_photo") {
			t.Fatal("hidden tool in the text-path prompt")
		}
		ce2, _ := newPhotoToolEnv(t, prompts.Qwen3_8B, true)
		ce2.warm("tok-7")
		if s := fmtMessages(ce2.eng.last()); !strings.Contains(s, "measure_photo") || strings.Contains(s, "x-jarvis-type") {
			t.Fatal("text-path prompt: tool missing or marker kept")
		}
	})
	t.Run("photos block with a node photo tool", func(t *testing.T) {
		ce, _ := newPhotoToolEnv(t, prompts.Qwen3_5_9B, true)
		cid := ce.warm("tok-7")
		ce.eng.say("Hi.")
		ce.chat("tok-7", map[string]any{"message": "hello", "conversation_id": cid})
		if strings.Contains(fmtMessages(ce.eng.last()), "PHOTOS:") {
			t.Fatal("block without photos")
		}
		ce.eng.say("A mug. Want me to measure it?")
		ce.chat("tok-7", map[string]any{"message": "", "conversation_id": cid, "images": []any{img("image/png", pngBytes)}})
		if !strings.Contains(fmtMessages(ce.eng.last()), "PHOTOS:") {
			t.Fatal("no block with a photo and a node photo tool")
		}
	})
}

// A command echoing its photos back: the server redacts them before the model sees the result.
func TestNodePhotoToolResultRedacted(t *testing.T) {
	ce, _ := newPhotoToolEnv(t, prompts.Qwen3_5_9B, true)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	ce.pub.mu.Lock()
	ce.pub.toolReply = `{"output": {"success": true, "context": {"echo": [{"mime": "image/png", "data": "` + b64 +
		`"}], "url": "data:image/png;base64,` + b64 + `"}}}`
	ce.pub.mu.Unlock()
	cid := ce.warm("tok-7")
	ce.eng.push(nodeCall("measure_photo", `{"photos": [1]}`))
	ce.eng.say("Done.")
	ce.chat("tok-7", map[string]any{"message": "measure it", "conversation_id": cid, "images": []any{img("image/png", pngBytes)}})
	r := toolReplies(ce.eng.last())
	if len(r) == 0 || strings.Contains(r[len(r)-1], b64) || !strings.Contains(r[len(r)-1], `"[image]"`) {
		t.Fatalf("tool reply %v", r)
	}
}

func TestErrandAndSignalMenusDropPhotoTools(t *testing.T) {
	v, _ := pyjson.Loads(photoNodeCommands)
	var kept []string
	for _, c := range v.([]any) {
		if !commandTakesImage(c.(*pyjson.Object)) {
			kept = append(kept, toolName2(c.(*pyjson.Object)))
		}
	}
	if strings.Join(kept, ",") != "get_weather" {
		t.Fatalf("errand commands %v", kept)
	}
	rep := &toolsReport{raw: `{"client_tools": ` + photoNodeTools + `}`}
	var names []string
	for _, tl := range rep.clientTools() {
		names = append(names, toolName(tl))
	}
	if strings.Join(names, ",") != "get_weather" {
		t.Fatalf("signal tools %v", names)
	}
}

// save_recipe_from_image is a photo tool by its schema, not by name.
func TestRecipeToolIsGenericPhotoTool(t *testing.T) {
	ce, _ := newRecipeChatEnv(t, prompts.Qwen3_5_9B, &fakeImporter{})
	def := (&saveRecipeTool{}).Definition()
	ps := servertools.ImageParams(def)
	if len(ps) != 1 || ps[0].Name != "images" || ps[0].Required {
		t.Fatalf("image params %+v", ps)
	}
	if got := ce.m.serverPhotoTools(); len(got) != 1 || got[saveRecipeToolName] == nil {
		t.Fatalf("server photo tools %v", got)
	}
	ce.warm("tok-7")
	if raw, _ := json.Marshal(ce.eng.last()["tools"]); strings.Contains(string(raw), "x-jarvis-type") ||
		!strings.Contains(string(raw), saveRecipeToolName) {
		t.Fatalf("tools %s", raw)
	}
}

func TestWireArguments(t *testing.T) {
	imgs := imageArgs{"photos": {{MIME: "image/png", Data: pngBytes}}}
	o := wireArguments(`{"unit": "px", "photos": [1]}`, imgs).(*pyjson.Object)
	if o.Keys()[0] != "unit" || o.Keys()[1] != "photos" {
		t.Fatalf("key order %v", o.Keys())
	}
	if s := pyjson.Dumps(o, true); !strings.Contains(s, base64.StdEncoding.EncodeToString(pngBytes)) ||
		strings.Contains(s, "data:") {
		t.Fatal(s)
	}
	if wireArguments(`{"a": 1}`, nil) != `{"a": 1}` {
		t.Fatal("no photos: keep the string")
	}
	if m, ok := wireArguments("", nil).(map[string]any); !ok || len(m) != 0 {
		t.Fatal("empty arguments")
	}
}
