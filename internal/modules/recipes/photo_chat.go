package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/ocrq"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The chat's "save this as a recipe" (docs/cc/chat-images.md §8): cc's save_recipe_from_image
// tool hands the photos a user attached in mobile chat to ImportRecipePhotos, in process. It is
// the photo import (#19) with one difference: the job saves the draft as a recipe itself
// (the user already asked to save it) instead of leaving it for review in the recipes app, and
// the outcome reaches the user as an inbox item plus a push, the way deep research reports.
//
// Same pipeline (preparePhoto, every OCR engine, the quality gate, P2/P3), same rows (the
// ingestion, the parse job, then the recipe), same ownership: the user is the author and the
// recipe lands in their write household, resolved from the chat's household exactly as a token
// naming it would be (resolve, RD7).

// Notifier delivers a chat import's outcome (the notifications module, in process).
type Notifier interface {
	CreateInboxItem(ctx context.Context, tx *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error)
	Notify(ctx context.Context, tx *sql.Tx, source string, n notifications.Notification) (notifications.Delivery, error)
}

const (
	// chatImportCategory is the inbox/push category of a chat import's outcome.
	chatImportCategory = "recipe"
	chatImportSource   = ServiceName
)

// PhotoError is a photo the import refuses (count, size, not an image). Its message is meant
// for the user.
type PhotoError struct{ Msg string }

func (e *PhotoError) Error() string { return e.Msg }

// UserMessage is the refusal as the user should hear it.
func (e *PhotoError) UserMessage() string { return e.Msg }

// ImportRecipePhotos queues a photo import of in-memory photos (the pages of one recipe, in
// order) for userID, writing to householdID when they belong to it, and saves the result as a
// recipe when the job finishes. It returns the parse job id. Errors: *PhotoError for photos
// the import refuses, anything else is the server's.
func (m *Module) ImportRecipePhotos(ctx context.Context, userID int64, householdID string, photos [][]byte) (string, error) {
	switch {
	case userID == 0:
		return "", errors.New("recipes: a photo import needs a user")
	case len(photos) == 0:
		return "", &PhotoError{"no photo to import"}
	case len(photos) > maxPhotos:
		return "", &PhotoError{fmt.Sprintf("too many photos: at most %d pages per recipe", maxPhotos)}
	}
	if m.deps.Queue == nil {
		return "", errors.New("recipes: no job queue")
	}
	c, err := m.resolve(ctx, authn.User{ID: userID, HouseholdID: householdID})
	if err != nil {
		return "", err
	}
	maxBytes := m.settings.Int(ctx, SettingImageMaxBytes, settings.Scope{})
	prepared := make([][]byte, 0, len(photos))
	for i, p := range photos {
		if int64(len(p)) > maxBytes {
			return "", &PhotoError{fmt.Sprintf("image %d is too large", i+1)}
		}
		out, ok := preparePhoto(p)
		if len(p) == 0 || !ok {
			return "", &PhotoError{fmt.Sprintf("image %d is not a readable photo", i+1)}
		}
		prepared = append(prepared, out)
	}
	_, jobID, err := m.queuePhotoImport(ctx, c, prepared, 3, nil, map[string]any{"save": true, "origin": "chat"})
	return jobID, err
}

// draftRecipe turns a photo import's draft into a RecipeCreate the way the recipes app's
// review screen does (mapRecipeDraftToParsed): an ingredient's text is its name, "— notes"
// appended; quantity and unit keep their columns; steps are numbered from 1; servings is the
// draft's leading number. The source is "image". Blank ingredients and steps are dropped.
func draftRecipe(d *ocrq.Draft) recipeIn {
	title := strings.TrimSpace(d.Title)
	src := "image"
	in := recipeIn{title: &title, sourceType: &src, description: d.Description, tags: d.Tags}
	for _, ing := range d.Ingredients {
		text := stripUnit(strings.TrimSpace(ing.Name), ing.Unit)
		if text == "" {
			continue
		}
		if ing.Notes != nil && strings.TrimSpace(*ing.Notes) != "" {
			text += " — " + strings.TrimSpace(*ing.Notes)
		}
		in.ingredients = append(in.ingredients, ingredientIn{text: text, quantityDisplay: nonBlank(ing.Quantity), unit: nonBlank(ing.Unit)})
	}
	for _, s := range d.Steps {
		if s = strings.TrimSpace(s); s != "" {
			in.steps = append(in.steps, stepIn{number: int64(len(in.steps) + 1), text: s})
		}
	}
	in.servings = leadingInt(d.Servings)
	in.prep, in.cook, in.total = positive(d.Prep), positive(d.Cook), positive(d.Total)
	return in
}

// stripUnit drops a unit the model repeated at the start of the name ("cloves garlic" with
// unit "cloves" → "garlic"), so the saved row doesn't read "6 cloves cloves garlic". The
// review screen lets a person fix that; the chat save has no review.
func stripUnit(name string, unit *string) string {
	if unit == nil {
		return name
	}
	u := strings.TrimSpace(*unit)
	if u == "" || len(name) <= len(u)+1 || !strings.EqualFold(name[:len(u)], u) || name[len(u)] != ' ' {
		return name
	}
	return strings.TrimSpace(name[len(u):])
}

func nonBlank(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

func positive(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

// leadingInt reads "4", "4 servings" or "4-6" as 4; nil when the text starts with no number.
func leadingInt(s *string) *int64 {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	end := strings.IndexFunc(t, func(r rune) bool { return !unicode.IsDigit(r) })
	if end == -1 {
		end = len(t)
	}
	n, err := strconv.ParseInt(t[:end], 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

// saveDraft is the chat import's last step: the draft becomes a recipe of the job's author in
// their write household, and the job is COMMITTED in the same transaction (as POST /recipes
// with parse_job_id does), its result gaining recipe_id. A job canceled meanwhile is left alone.
func (m *Module) saveDraft(ctx context.Context, j parseJob, d *ocrq.Draft, result map[string]any) error {
	c, err := m.jobCaller(ctx, j)
	if err != nil {
		return err
	}
	in := draftRecipe(d)
	if len(in.ingredients) == 0 || len(in.steps) == 0 {
		return m.markError(ctx, j.ID, "save_failed", "The photo was read, but no ingredients or steps could be saved.")
	}
	now := ts(m.now())
	var id int64
	saved := false
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM recipes_recipe_parse_jobs WHERE id = ?`, j.ID).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // purged meanwhile
			}
			return err
		}
		if status != statusComplete {
			return nil // canceled meanwhile
		}
		var err error
		if id, err = insertRecipe(ctx, tx, c, in, now); err != nil {
			return err
		}
		result["recipe_id"] = id
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'COMMITTED', committed_at = ?,
			result_json = ?, updated_at = ? WHERE id = ?`, now, string(raw), now, j.ID); err != nil {
			return err
		}
		saved = true
		return nil
	})
	if err != nil {
		return err
	}
	if saved {
		m.deps.Log.Info("recipes: chat photo import saved", "parse_job_id", j.ID, "recipe_id", id,
			"ingredients", len(in.ingredients), "steps", len(in.steps))
	}
	return nil
}

// notifySavedImport tells the job's author how their chat import ended: the saved recipe (an
// inbox item with the recipe, and a push), or why it failed. Canceled and still-running jobs
// get nothing. Delivery failures are logged, never retried.
func (m *Module) notifySavedImport(ctx context.Context, jobID string) {
	if m.Notify == nil {
		return
	}
	j, ok, err := loadJob(ctx, m.deps.DB.Read, jobID)
	if err != nil || !ok {
		return
	}
	uid, err := strconv.ParseInt(j.UserID, 10, 64)
	if err != nil {
		return
	}
	hh := j.HouseholdID.String
	var item notifications.NewInboxItem
	var push notifications.Notification
	data := map[string]any{"parse_job_id": j.ID}
	switch j.Status {
	case statusCommitted:
		var res struct {
			RecipeID int64 `json:"recipe_id"`
		}
		_ = json.Unmarshal([]byte(j.Result.String), &res)
		rs, err := loadRecipes(ctx, m.deps.DB.Read, `WHERE r.id = ?`, res.RecipeID)
		if err != nil || len(rs) == 0 {
			return // deleted already
		}
		r := rs[0]
		item = notifications.NewInboxItem{
			Title:   "Recipe saved: " + r.Title,
			Summary: fmt.Sprintf("%d ingredients, %d steps, saved from your photo. It's in Recipes now.", len(r.Ingredients), len(r.Steps)),
			Body:    recipeText(r),
		}
		push = notifications.Notification{Title: "Recipe saved", Body: r.Title}
		data["type"], data["recipe_id"] = "recipe_saved", r.ID
	case statusError:
		msg := strings.TrimSpace(j.ErrorMessage.String)
		if j.ErrorCode.String != "quality_gate_failed" && j.ErrorCode.String != "save_failed" {
			msg = "The recipe couldn't be read from the photo (" + j.ErrorCode.String + "). Try a straight-on photo in good light, or add it in the Recipes app."
		}
		item = notifications.NewInboxItem{Title: "Recipe not saved", Summary: msg, Body: msg}
		push = notifications.Notification{Title: "Recipe not saved", Body: msg}
		data["type"] = "recipe_import_failed"
	default:
		return
	}
	item.HouseholdID, item.UserID = hh, &uid
	item.Category, item.SourceService = chatImportCategory, chatImportSource
	item.Metadata = data
	it, err := m.Notify.CreateInboxItem(ctx, nil, item)
	if err != nil {
		m.deps.Log.Warn("recipes: chat import inbox item failed", "parse_job_id", j.ID, "err", err)
		return
	}
	data["inbox_item_id"] = it.ID
	push.TargetType, push.TargetID = "user", j.UserID
	push.Data, push.Priority, push.Category = data, "default", chatImportCategory
	if _, err := m.Notify.Notify(ctx, nil, chatImportSource, push); err != nil {
		m.deps.Log.Warn("recipes: chat import push failed", "parse_job_id", j.ID, "err", err)
	}
}

// recipeText is a saved recipe as plain text for the inbox item's body.
func recipeText(r recipeRead) string {
	var b strings.Builder
	b.WriteString(r.Title)
	b.WriteString("\n\nIngredients:\n")
	for _, ing := range r.Ingredients {
		b.WriteString("- ")
		for _, p := range []*string{ing.QuantityDisplay, ing.Unit} {
			if p != nil && *p != "" {
				b.WriteString(*p + " ")
			}
		}
		b.WriteString(ing.Text + "\n")
	}
	b.WriteString("\nSteps:\n")
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "%d. %s\n", s.StepNumber, s.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}
