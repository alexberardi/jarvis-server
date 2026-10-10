package cc

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Photo → action (docs/cc/chat-images.md §8). A server tool receives the chat photos the model
// sees by number ("image 1"), resolved from the working history here, never as base64 in the
// model's arguments. First tool: save_recipe_from_image, the recipes photo import.

// RecipeImporter is the recipes module's photo import, in process (recipes.ImportRecipePhotos):
// the photos are the pages of one recipe; the job saves it for userID in their household and
// tells them when it's done. Errors with a user-facing message implement userMessager.
type RecipeImporter interface {
	ImportRecipePhotos(ctx context.Context, userID int64, householdID string, photos [][]byte) (jobID string, err error)
}

// describedImageRE matches a user message whose photos were already replaced: by their
// description ("[image 1: …]", chat_images.go) or by visionGuard's bare "[image]".
var describedImageRE = regexp.MustCompile(`^\[image(?: \d+:|\])`)

// keptPhotos are the latest photo message's decoded bytes, kept on the conversation for tools
// after the description replaced them in history (CI8: the follow-up "yes" to "want me to save
// it?" still has the photo). The model never sees them again (CI3). Memory only, one message
// per conversation (at most chatMaxImages × chatMaxImageBytes); they die with the conversation
// (idle TTL, eviction, restart).
type keptPhotos struct {
	msgID uint64
	imgs  []servertools.Image
}

// keepPhotos records the newest message in msgs that carries photos, unless an equal or newer
// one is already kept. Called on every commit, so a photo message compacted or trimmed away
// before its description landed keeps its bytes too. Callers hold c.mu.
func (c *conversation) keepPhotos(msgs []chatMsg) {
	for i := len(msgs) - 1; i >= 0; i-- {
		mm := msgs[i]
		if len(mm.images) == 0 {
			continue
		}
		if c.photos != nil && c.photos.msgID >= mm.id {
			return
		}
		imgs, err := decodeChatImages(mm.images)
		if err != nil {
			return // never happens for validated images; tools then report the photo expired
		}
		c.photos = &keptPhotos{msgID: mm.id, imgs: imgs}
		return
	}
}

// hasToolPhotos reports whether a tool could get photos this turn: attached now or kept.
func (c *conversation) hasToolPhotos(attached int) bool {
	return attached > 0 || c.photos != nil
}

// decodeChatImages turns data URLs back into bytes.
func decodeChatImages(in []chatImage) ([]servertools.Image, error) {
	out := make([]servertools.Image, 0, len(in))
	for _, im := range in {
		_, b64, ok := strings.Cut(im.dataURL, ",")
		if !ok {
			return nil, errors.New("malformed image")
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, errors.New("malformed image")
		}
		out = append(out, servertools.Image{MIME: im.mime, Data: data})
	}
	return out, nil
}

// historyImages is servertools.TurnImages over a working history.
type historyImages struct {
	attached []chatImage         // photos still on the message (decoded on use)
	kept     []servertools.Image // the conversation's kept bytes for a described message
	expired  bool
}

// turnImagesOf resolves the photos the model currently sees: those of the latest user message
// that ever had photos, numbered 1-based within it. Still attached → those; replaced by their
// description → the conversation's kept bytes for that message; kept bytes for an older
// message, or none → expired. When no photo message is left in history (compacted or trimmed
// away), the kept bytes are still the latest photos of the conversation.
func turnImagesOf(msgs []chatMsg, kept *keptPhotos) *historyImages {
	for i := len(msgs) - 1; i >= 0; i-- {
		mm := msgs[i]
		if mm.Role != "user" {
			continue
		}
		if len(mm.images) > 0 {
			return &historyImages{attached: mm.images}
		}
		if describedImageRE.MatchString(mm.Content) {
			if kept != nil && kept.msgID == mm.id && mm.id != 0 {
				return &historyImages{kept: kept.imgs}
			}
			return &historyImages{expired: true}
		}
	}
	if kept != nil {
		return &historyImages{kept: kept.imgs}
	}
	return &historyImages{}
}

func (h *historyImages) Count() int { return len(h.attached) + len(h.kept) }

func (h *historyImages) Image(n int) (servertools.Image, error) {
	count := h.Count()
	if count == 0 {
		if h.expired {
			return servertools.Image{}, servertools.ErrImagesExpired
		}
		return servertools.Image{}, servertools.ErrNoImages
	}
	if n < 1 || n > count {
		return servertools.Image{}, &servertools.ImageIndexError{N: n, Count: count}
	}
	if h.kept != nil {
		return h.kept[n-1], nil
	}
	imgs, err := decodeChatImages(h.attached[n-1 : n])
	if err != nil {
		return servertools.Image{}, err
	}
	return imgs[0], nil
}

// --- save_recipe_from_image ---

const saveRecipeToolName = "save_recipe_from_image"

// saveRecipeGuidance goes into the system prompt with the tool.
const saveRecipeGuidance = "save_recipe_from_image: when the user wants a recipe from a photo saved (added to their " +
	"recipes, kept, imported) — a photo attached now or earlier in this chat — call save_recipe_from_image, every time " +
	"they ask; it tells you when the photo is no longer available. Never say a recipe is saved or being saved unless the " +
	"tool succeeded in this turn. Don't transcribe the recipe and don't use a list or note tool for it. Photos are " +
	"referenced by number (image 1, image 2)."

// saveRecipeTool hands chat photos to the recipes photo import.
type saveRecipeTool struct {
	m   *Module
	imp RecipeImporter
}

func (*saveRecipeTool) Name() string { return saveRecipeToolName }

func (*saveRecipeTool) Definition() *pyjson.Object {
	params := servertools.Obj("type", "object",
		"properties", servertools.Obj(
			"images", servertools.Obj("type", "array", "items", servertools.Obj("type", "integer"),
				"description", "Which attached photos hold the recipe, by number (1 = the first photo of the message). "+
					"Several numbers = several pages of the same recipe, in order. Omit to use every attached photo."),
		),
		"required", []any{})
	fn := servertools.Obj("name", saveRecipeToolName,
		"description", "Save a recipe from a photo the user attached in chat (a recipe card, a cookbook page, a screenshot) "+
			"to the household's recipes. Use when the user attaches a recipe photo and says 'save this recipe', 'add this "+
			"to my recipes', 'keep this one', 'import this recipe' or similar. The photo is read (OCR) and structured in "+
			"the background; the user is notified when the recipe is saved. Needs a photo attached to this conversation.",
		"parameters", params)
	return servertools.Obj("type", "function", "function", fn, "included_system_prompt_text", saveRecipeGuidance)
}

// toolErr is a refusal the model relays.
func toolErr(code, msg string) *pyjson.Object {
	return servertools.Obj("success", false, "error", code, "message", msg)
}

func (t *saveRecipeTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if !turn.Speaker.Known() {
		return toolErr("no_speaker", "Saving a recipe needs to know who you are; use the mobile app's chat."), nil
	}
	if turn.HouseholdID == "" {
		return toolErr("no_household", "No household context available"), nil
	}
	ns, err := servertools.ImageNumbers(call, "images")
	if err != nil {
		return toolErr("invalid_arguments", err.Error()), nil
	}
	imgs, err := servertools.ResolveImages(turn.Images, ns)
	var idx *servertools.ImageIndexError
	switch {
	case errors.Is(err, servertools.ErrImagesExpired):
		return toolErr("image_expired", "That photo is no longer available: photos are kept only while the chat "+
			"is active. Ask the user to attach the photo again with their request."), nil
	case errors.Is(err, servertools.ErrNoImages):
		return toolErr("no_image", "No photo is attached. Ask the user to attach a photo of the recipe (camera or "+
			"gallery) with their message."), nil
	case errors.As(err, &idx):
		return toolErr("invalid_image", idx.Error()), nil
	case err != nil:
		return nil, err
	}
	photos := make([][]byte, len(imgs))
	for i, im := range imgs {
		photos[i] = im.Data
	}
	jobID, err := t.imp.ImportRecipePhotos(ctx, turn.Speaker.UserID, turn.HouseholdID, photos)
	if err != nil {
		var um interface{ UserMessage() string }
		if errors.As(err, &um) {
			return toolErr("invalid_image", um.UserMessage()), nil
		}
		t.m.deps.Log.Error("cc: recipe photo import failed", "conversation_id", turn.ConversationID, "err", err)
		return toolErr("import_failed", "The recipe couldn't be queued for import; try again in a moment."), nil
	}
	t.m.deps.Log.Info("cc: recipe photo import queued", "conversation_id", turn.ConversationID,
		"user_id", turn.Speaker.UserID, "images", len(photos), "parse_job_id", jobID)
	return servertools.Obj("success", true, "status", "processing", "images", len(photos),
		"message", "Reading the recipe from the photo now. It is saved to the household's recipes in a minute or two, "+
			"and the user gets a notification when it's saved (or if the photo can't be read). Tell the user that in "+
			"one short sentence; don't list the recipe."), nil
}
