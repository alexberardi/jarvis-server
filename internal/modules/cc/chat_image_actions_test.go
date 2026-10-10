package cc

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// fakeImporter records ImportRecipePhotos calls.
type fakeImporter struct {
	mu    sync.Mutex
	calls []importCall
	err   error
}

type importCall struct {
	userID int64
	hh     string
	photos [][]byte
}

func (f *fakeImporter) ImportRecipePhotos(_ context.Context, userID int64, hh string, photos [][]byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.calls = append(f.calls, importCall{userID, hh, photos})
	return "job-1", nil
}

func (f *fakeImporter) got() []importCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]importCall(nil), f.calls...)
}

// photoErr is a refusal with a user-facing message, like recipes.PhotoError.
type photoErr string

func (e photoErr) Error() string       { return string(e) }
func (e photoErr) UserMessage() string { return string(e) }

// newRecipeChatEnv is newImageChatEnv with a recipes importer wired before Register.
func newRecipeChatEnv(t *testing.T, provider string, imp RecipeImporter) (*chatEnv, *jobLog) {
	t.Helper()
	pub := &chatNodePub{tools: `{"client_tools": ` + weatherTool + `, "available_commands": [], "installed_packages": []}`}
	ve := newVoiceEnv(t, provider, func(m *Module) { m.Publisher = pub; pub.m = m; m.Recipes = imp })
	ce := &chatEnv{voiceEnv: ve, pub: pub}
	ce.setOnline(true)
	old := chatWordPause
	chatWordPause = time.Millisecond
	t.Cleanup(func() { chatWordPause = old })
	ce.eng.setVision(llm.LabelLive, true)
	jl := &jobLog{}
	ce.m.convJobHook = jl.hook
	return ce, jl
}

func toolNamesOf(req map[string]any) []string {
	var out []string
	tools, _ := req["tools"].([]any)
	for _, tl := range tools {
		fn, _ := tl.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		out = append(out, name)
	}
	return out
}

func has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// toolReplies are the role=tool contents of a request.
func toolReplies(req map[string]any) []string {
	var out []string
	for _, m := range messagesOf(req) {
		if m["role"] == "tool" {
			s, _ := m["content"].(string)
			out = append(out, s)
		}
	}
	return out
}

func tio(msgs []chatMsg) *historyImages { return turnImagesOf(msgs, nil) }

func TestTurnImagesOf(t *testing.T) {
	two := []chatImage{
		{mime: "image/png", dataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)},
		{mime: "image/jpeg", dataURL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)},
	}
	sys := chatMsg{Role: "system", Content: "you are jarvis"}

	// The current turn's photos, by 1-based number.
	ti := tio([]chatMsg{sys, {Role: "user", Content: "save this", images: two},
		{Role: "assistant", Content: "", ToolCalls: nil}})
	if ti.Count() != 2 {
		t.Fatalf("count %d", ti.Count())
	}
	for n, want := range map[int][]byte{1: pngBytes, 2: jpegBytes} {
		im, err := ti.Image(n)
		if err != nil || string(im.Data) != string(want) {
			t.Fatalf("image %d: %v", n, err)
		}
	}
	if im, _ := ti.Image(2); im.MIME != "image/jpeg" {
		t.Fatalf("mime %q", im.MIME)
	}
	for _, n := range []int{0, 3, -1} {
		var idx *servertools.ImageIndexError
		if _, err := ti.Image(n); !errors.As(err, &idx) || idx.Count != 2 {
			t.Fatalf("image %d: %v", n, err)
		}
	}
	got, err := servertools.ResolveImages(ti, []int{2, 1})
	if err != nil || len(got) != 2 || string(got[0].Data) != string(jpegBytes) {
		t.Fatalf("resolve [2 1]: %v", err)
	}
	if got, err := servertools.ResolveImages(ti, nil); err != nil || len(got) != 2 || string(got[0].Data) != string(pngBytes) {
		t.Fatalf("resolve all: %v", err)
	}

	// An earlier turn's photos whose description is still pending: the model still sees them.
	ti = tio([]chatMsg{sys, {Role: "user", Content: "what is this?", images: two[:1]},
		{Role: "assistant", Content: "A recipe card."}, {Role: "user", Content: "save it"}})
	if ti.Count() != 1 {
		t.Fatalf("pending description: count %d", ti.Count())
	}

	// Described (or vision-guarded): expired, asking for the photo again.
	for _, c := range []string{"[image 1: A recipe card for pancakes.]\nwhat is this?", "[image] what is this?"} {
		ti = tio([]chatMsg{sys, {Role: "user", Content: c}, {Role: "assistant", Content: "A recipe card."},
			{Role: "user", Content: "save it"}})
		if _, err := servertools.ResolveImages(ti, nil); !errors.Is(err, servertools.ErrImagesExpired) {
			t.Fatalf("%q: %v", c, err)
		}
		if _, err := ti.Image(1); !errors.Is(err, servertools.ErrImagesExpired) {
			t.Fatalf("%q: image 1: %v", c, err)
		}
	}
	// Never any photo.
	ti = tio([]chatMsg{sys, {Role: "user", Content: "save my pancake recipe"}})
	if _, err := servertools.ResolveImages(ti, []int{1}); !errors.Is(err, servertools.ErrNoImages) {
		t.Fatalf("no photos: %v", err)
	}
	if _, err := servertools.ResolveImages(nil, nil); !errors.Is(err, servertools.ErrNoImages) {
		t.Fatalf("nil source: %v", err)
	}
	// Text that merely mentions a marker mid-message is not a description.
	ti = tio([]chatMsg{sys, {Role: "user", Content: "what does [image 1: x] mean"}})
	if _, err := ti.Image(1); !errors.Is(err, servertools.ErrNoImages) {
		t.Fatalf("marker mid-text: %v", err)
	}
}

func TestSaveRecipeToolOffered(t *testing.T) {
	imp := &fakeImporter{}
	t.Run("chat with live vision", func(t *testing.T) {
		ce, _ := newRecipeChatEnv(t, prompts.Qwen3_5_9B, imp) // the live model's provider: native, with tool guidance
		ce.warm("tok-7")
		if names := toolNamesOf(ce.eng.last()); !has(names, saveRecipeToolName) {
			t.Fatalf("tools %v", names)
		}
		if !strings.Contains(fmtMessages(ce.eng.last()), "call save_recipe_from_image") {
			t.Fatal("no system prompt guidance")
		}
	})
	t.Run("chat without vision", func(t *testing.T) {
		ce, _ := newRecipeChatEnv(t, prompts.ChatGPT, imp)
		ce.eng.setVision(llm.LabelLive, false)
		ce.warm("tok-7")
		if names := toolNamesOf(ce.eng.last()); has(names, saveRecipeToolName) {
			t.Fatalf("offered without vision: %v", names)
		}
	})
	t.Run("voice conversation", func(t *testing.T) {
		ce, _ := newRecipeChatEnv(t, prompts.ChatGPT, imp)
		ce.start("conv-voice", weatherTool)
		if names := toolNamesOf(ce.eng.last()); has(names, saveRecipeToolName) || len(names) == 0 {
			t.Fatalf("voice tools %v", names)
		}
	})
	t.Run("no recipes module", func(t *testing.T) {
		ce, _ := newImageChatEnv(t, prompts.ChatGPT)
		ce.warm("tok-7")
		if names := toolNamesOf(ce.eng.last()); has(names, saveRecipeToolName) {
			t.Fatalf("offered without recipes: %v", names)
		}
	})
	t.Run("text path", func(t *testing.T) {
		g := prompts.ToolGates{ChatPhotos: true}
		if !has(prompts.TextServerTools(g), saveRecipeToolName) || has(prompts.TextServerTools(prompts.ToolGates{}), saveRecipeToolName) {
			t.Fatal("text path gate")
		}
	})
}

func saveCall(args string) engineReply {
	return engineReply{toolCalls: []map[string]any{{"id": "call_r1", "type": "function",
		"function": map[string]any{"name": saveRecipeToolName, "arguments": args}}}}
}

func TestSaveRecipeFromImage(t *testing.T) {
	var logBuf safeBuffer
	withEnvExtra(t, envExtra{log: &logBuf})
	imp := &fakeImporter{}
	ce, jobs := newRecipeChatEnv(t, prompts.ChatGPT, imp)
	cid := ce.warm("tok-7")
	before := len(ce.eng.requests())
	ce.eng.push(saveCall(`{"images": [2, 1]}`))
	ce.eng.say("Saving it now; you'll get a notification when it's in your recipes.")
	frames := ce.chat("tok-7", map[string]any{"message": "save this as a recipe", "conversation_id": cid,
		"images": []any{img("image/png", pngBytes), img("image/jpeg", jpegBytes)}})
	if f := lastFrame(t, frames); f["type"] != "done" || !strings.Contains(f["full_text"].(string), "Saving it now") {
		t.Fatalf("frames %v", frames)
	}
	// The importer got the chat user, the conversation's household and the photos' bytes, in
	// the order the model named them.
	got := imp.got()
	if len(got) != 1 || got[0].userID != 7 || got[0].hh != voiceHH || len(got[0].photos) != 2 ||
		string(got[0].photos[0]) != string(jpegBytes) || string(got[0].photos[1]) != string(pngBytes) {
		t.Fatalf("import calls %+v", got)
	}
	// The model got a short processing result, never the image data.
	reqs := ce.eng.requests()[before:]
	if len(reqs) != 2 {
		t.Fatalf("%d LLM requests", len(reqs))
	}
	replies := toolReplies(reqs[1])
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	if len(replies) != 1 || !strings.Contains(replies[0], `"status": "processing"`) || strings.Contains(replies[0], b64) ||
		strings.Contains(replies[0], "data:image") {
		t.Fatalf("tool reply %v", replies)
	}
	// The images stay attached for the turn's continue; the description is still queued.
	if len(imageURLs(reqs[1])) != 2 || len(jobs.of(chatDescribeJob)) != 1 {
		t.Fatal("images not attached for the continue, or no description job")
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "cc: recipe photo import queued") || strings.Contains(logs, b64) ||
		strings.Contains(logs, base64.StdEncoding.EncodeToString(jpegBytes)) || strings.Contains(logs, "data:image") {
		t.Fatalf("logs:\n%s", logs)
	}

	// A bad number is the model's to fix; the importer isn't called.
	ce.eng.push(saveCall(`{"images": [3]}`))
	ce.eng.say("Which photo?")
	ce.chat("tok-7", map[string]any{"message": "save it", "conversation_id": cid,
		"images": []any{img("image/png", pngBytes)}})
	if r := toolReplies(ce.eng.last()); len(r) == 0 || !strings.Contains(r[len(r)-1], "invalid_image") ||
		!strings.Contains(r[len(r)-1], "only image 1 is attached") || len(imp.got()) != 1 {
		t.Fatalf("bad index reply %v", r)
	}
}

// CI8: after the description replaced the photo for the model, a follow-up "yes" still saves
// it from the bytes the conversation keeps, and the model is not re-sent the image (CI3).
func TestSaveRecipeAfterDescription(t *testing.T) {
	imp := &fakeImporter{}
	ce, jobs := newRecipeChatEnv(t, prompts.ChatGPT, imp)
	cid := ce.warm("tok-7")
	j := chatWithImage(t, ce, jobs, cid, "", "A pancake recipe card. Want me to save it as a recipe?")
	ce.eng.say("A recipe card for pancakes.")
	ce.m.describeImages(context.Background(), j) // live describes (no background vision)
	if um := userMessages(ce.m.convs.get(cid)); len(um[len(um)-1].images) != 0 {
		t.Fatal("description did not replace the image")
	}

	before := len(ce.eng.requests())
	ce.eng.push(saveCall(`{"images": [1]}`))
	ce.eng.say("Saving it now.")
	frames := ce.chat("tok-7", map[string]any{"message": "yes", "conversation_id": cid})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatalf("frames %v", frames)
	}
	got := imp.got()
	if len(got) != 1 || len(got[0].photos) != 1 || string(got[0].photos[0]) != string(pngBytes) {
		t.Fatalf("import calls %+v", got)
	}
	r := toolReplies(ce.eng.last())
	if len(r) == 0 || !strings.Contains(r[len(r)-1], `"status": "processing"`) {
		t.Fatalf("tool reply %v", r)
	}
	for _, req := range ce.eng.requests()[before:] {
		if len(imageURLs(req)) != 0 || !strings.Contains(fmtMessages(req), "[image 1: A recipe card for pancakes.]") {
			t.Fatalf("follow-up request re-sent the image or lost the description: %s", fmtMessages(req))
		}
	}
}

// The bytes truly gone (only the description left) → image_expired; the conversation expired →
// a fresh conversation with no photo → no_image. Either way the importer is not called.
func TestSaveRecipeFromImageExpired(t *testing.T) {
	imp := &fakeImporter{}
	ce, jobs := newRecipeChatEnv(t, prompts.ChatGPT, imp)
	cid := ce.warm("tok-7")
	j := chatWithImage(t, ce, jobs, cid, "what is this?", "A pancake recipe card.")
	ce.eng.say("A recipe card for pancakes.")
	ce.m.describeImages(context.Background(), j)
	conv := ce.m.convs.get(cid)
	conv.mu.Lock()
	conv.photos = nil // as if the bytes were lost
	conv.mu.Unlock()

	ce.eng.push(saveCall(`{}`))
	ce.eng.say("Please send the photo again.")
	frames := ce.chat("tok-7", map[string]any{"message": "save that as a recipe", "conversation_id": cid})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatalf("frames %v", frames)
	}
	r := toolReplies(ce.eng.last())
	if len(r) == 0 || !strings.Contains(r[len(r)-1], `"error": "image_expired"`) ||
		!strings.Contains(r[len(r)-1], "attach the photo again") {
		t.Fatalf("expired reply %v", r)
	}

	// A conversation idle past its TTL takes its photos with it.
	j = chatWithImage(t, ce, jobs, cid, "and this?", "Another recipe card.")
	ce.eng.say("A recipe card for waffles.")
	ce.m.describeImages(context.Background(), j)
	ce.advance(convIdleTTL + time.Minute)
	ce.eng.push(saveCall(`{}`))
	ce.eng.say("Please attach the photo.")
	frames = ce.chat("tok-7", map[string]any{"message": "yes, save it", "conversation_id": cid})
	if f := lastFrame(t, frames); f["type"] != "done" {
		t.Fatalf("frames %v", frames)
	}
	r = toolReplies(ce.eng.last())
	if len(r) == 0 || !strings.Contains(r[len(r)-1], `"error": "no_image"`) {
		t.Fatalf("after expiry reply %v", r)
	}
	if len(imp.got()) != 0 {
		t.Fatal("importer called without the photo")
	}
}

// Only the latest photo message's bytes are kept, whatever order descriptions land in, and
// they survive the message being compacted away.
func TestKeptPhotosLatestOnly(t *testing.T) {
	imp := &fakeImporter{}
	ce, jobs := newRecipeChatEnv(t, prompts.ChatGPT, imp)
	cid := ce.warm("tok-7")
	j1 := chatWithImage(t, ce, jobs, cid, "what is this?", "A mug.")
	ce.eng.say("Here.")
	ce.chat("tok-7", map[string]any{"message": "and this?", "conversation_id": cid,
		"images": []any{img("image/jpeg", jpegBytes), img("image/png", pngBytes)}})
	conv := ce.m.convs.get(cid)
	conv.mu.Lock()
	kept := conv.photos
	conv.mu.Unlock()
	if kept == nil || len(kept.imgs) != 2 || string(kept.imgs[0].Data) != string(jpegBytes) || kept.msgID == j1.MessageID {
		t.Fatalf("kept %+v", kept)
	}
	// The older message's description lands late: the newer photos stay kept.
	ce.eng.say("A mug.")
	ce.m.describeImages(context.Background(), j1)
	conv.mu.Lock()
	if conv.photos != kept {
		t.Fatal("an older photo message replaced the kept photos")
	}
	conv.mu.Unlock()

	// A follow-up resolves the newer message's photos by their own numbering.
	ce.eng.push(saveCall(`{"images": [2]}`))
	ce.eng.say("Saving.")
	ce.chat("tok-7", map[string]any{"message": "save the second one as a recipe", "conversation_id": cid})
	if got := imp.got(); len(got) != 1 || string(got[0].photos[0]) != string(pngBytes) {
		t.Fatalf("import calls %+v", got)
	}

	// Compacted away (no photo message left in history): the kept bytes still resolve.
	sys := chatMsg{Role: "system", Content: "you are jarvis"}
	sum := chatMsg{Role: "system", Content: "Summary of the earlier conversation: the user sent two recipe photos.", summary: true}
	ti := turnImagesOf([]chatMsg{sys, sum, {Role: "user", Content: "save the first one"}}, kept)
	if im, err := ti.Image(1); err != nil || string(im.Data) != string(jpegBytes) || ti.Count() != 2 {
		t.Fatalf("compacted: %v", err)
	}
	// A described message the kept bytes don't belong to is honestly expired.
	ti = turnImagesOf([]chatMsg{sys, {Role: "user", Content: "[image 1: a mug]", id: kept.msgID + 1}}, kept)
	if _, err := ti.Image(1); !errors.Is(err, servertools.ErrImagesExpired) {
		t.Fatalf("mismatched kept: %v", err)
	}
	// The described message the kept bytes belong to resolves.
	ti = turnImagesOf([]chatMsg{sys, {Role: "user", Content: "[image 1: a mug]", id: kept.msgID}}, kept)
	if im, err := ti.Image(2); err != nil || string(im.Data) != string(pngBytes) {
		t.Fatalf("matching kept: %v", err)
	}
}

// CI8's prompt rule: a transient block while photos are available to a photo tool, never
// otherwise; the save guidance's "never claim a save" holds alongside it.
func TestPhotoActionsBlock(t *testing.T) {
	if prompts.PhotoActionsGate(true, map[string]bool{"web_search": true}) || prompts.PhotoActionsGate(false,
		map[string]bool{saveRecipeToolName: true}) || !prompts.PhotoActionsGate(true, map[string]bool{saveRecipeToolName: true}) {
		t.Fatal("gate")
	}
	ce, jobs := newRecipeChatEnv(t, prompts.Qwen3_5_9B, &fakeImporter{})
	cid := ce.warm("tok-7")
	ce.eng.say("Hi.")
	ce.chat("tok-7", map[string]any{"message": "hello", "conversation_id": cid})
	if strings.Contains(fmtMessages(ce.eng.last()), "PHOTOS:") {
		t.Fatal("block without photos")
	}
	j := chatWithImage(t, ce, jobs, cid, "", "A recipe card. Want me to save it as a recipe?")
	msgs := fmtMessages(ce.eng.last())
	if !strings.Contains(msgs, "PHOTOS:") || !strings.Contains(msgs, "offer the matching action") ||
		!strings.Contains(msgs, "Never say a recipe is saved or being saved unless the tool succeeded") {
		t.Fatalf("photo turn prompt %s", msgs)
	}
	ce.eng.say("A recipe card.")
	ce.m.describeImages(context.Background(), j)
	ce.eng.say("Sure.")
	ce.chat("tok-7", map[string]any{"message": "what's in it?", "conversation_id": cid})
	if !strings.Contains(fmtMessages(ce.eng.last()), "PHOTOS:") {
		t.Fatal("no block while the kept photo is available")
	}
	// The block is per turn: messages[0] stays byte-stable.
	conv := ce.m.convs.get(cid)
	conv.mu.Lock()
	first := conv.messages[0].Content
	conv.mu.Unlock()
	if strings.Contains(first, "PHOTOS:") {
		t.Fatal("block in the system prompt")
	}

	// No photo tool offered (no recipes module): no block, photos or not.
	ce2, _ := newImageChatEnv(t, prompts.ChatGPT)
	cid2 := ce2.warm("tok-7")
	ce2.eng.say("A mug.")
	ce2.chat("tok-7", map[string]any{"message": "", "conversation_id": cid2, "images": []any{img("image/png", pngBytes)}})
	if strings.Contains(fmtMessages(ce2.eng.last()), "PHOTOS:") {
		t.Fatal("block without a photo tool")
	}
}

func TestSaveRecipeFromImageRefusals(t *testing.T) {
	imp := &fakeImporter{err: photoErr("image 1 is not a readable photo")}
	ce, _ := newRecipeChatEnv(t, prompts.ChatGPT, imp)
	cid := ce.warm("tok-7")

	// No photo at all.
	ce.eng.push(saveCall(`{}`))
	ce.eng.say("Attach the photo, please.")
	ce.chat("tok-7", map[string]any{"message": "save my pancake recipe", "conversation_id": cid})
	if r := toolReplies(ce.eng.last()); len(r) == 0 || !strings.Contains(r[len(r)-1], `"error": "no_image"`) {
		t.Fatalf("no image reply %v", r)
	}
	// The import refuses the photo: its message reaches the model.
	ce.eng.push(saveCall(`{"images": [1]}`))
	ce.eng.say("That photo didn't work.")
	ce.chat("tok-7", map[string]any{"message": "save this", "conversation_id": cid, "images": []any{img("image/png", pngBytes)}})
	if r := toolReplies(ce.eng.last()); len(r) == 0 || !strings.Contains(r[len(r)-1], "image 1 is not a readable photo") {
		t.Fatalf("refusal reply %v", r)
	}
	// Garbage arguments.
	ce.eng.push(saveCall(`{"images": ["one"]}`))
	ce.eng.say("Hm.")
	ce.chat("tok-7", map[string]any{"message": "save this", "conversation_id": cid, "images": []any{img("image/png", pngBytes)}})
	if r := toolReplies(ce.eng.last()); len(r) == 0 || !strings.Contains(r[len(r)-1], "invalid_arguments") {
		t.Fatalf("bad args reply %v", r)
	}
	// An unknown speaker (a voice turn) is refused before any photo lookup.
	tool := &saveRecipeTool{m: ce.m, imp: imp}
	res, _ := tool.Execute(context.Background(), servertools.Call{}, servertools.Turn{HouseholdID: voiceHH})
	if !strings.Contains(fmtAny(res), "no_speaker") {
		t.Fatalf("unknown speaker %v", fmtAny(res))
	}
}

func fmtAny(v any) string { return pyjson.Dumps(v, true) }
