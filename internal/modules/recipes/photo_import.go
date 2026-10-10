package recipes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/extract"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/ocrq"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Photo import (§4.4, R8): POST /recipes/from-image/jobs (#19) prepares and stores the photos
// and queues one recipes.image job, which runs every available OCR engine in process (no
// fan-out, no join deadline), gates the best reading, structures it with P2 (and P3 when the
// draft needs it) and stores the RecipeDraft on the job. The originals stay in the blob store
// for 30 days (RD6, the hourly cleanup).

const imageJobType = "recipes.image"

const (
	maxPhotos     = 8
	p2MaxTokens   = 1100
	p2Timeout     = 60 * time.Second
	p3MaxTokens   = 1000
	p3Timeout     = 30 * time.Second
	ocrTimeout    = 5 * time.Minute
	gateFailedMsg = "Couldn't read enough text from this photo. Handwritten recipes, angled shots and low light " +
		"are the usual culprits — try a straight-on photo in good light, or enter it by hand."
)

// Recognizer is the OCR module's in-process API (ocr.Module.Recognize).
type Recognizer interface {
	Recognize(ctx context.Context, imgs []ocr.Image, o ocr.Options, engines []string) ([]ocr.Reading, error)
}

func ingestKey(uid, ingestionID string, idx int) string {
	return "recipes/ingest/" + uid + "/" + ingestionID + "/" + strconv.Itoa(idx) + ".jpg"
}

type upload struct {
	data     []byte
	tooLarge bool
}

// readPhotos reads the repeated "images" file field. status 422 means no images field; the
// count is checked before sizes, as legacy did.
func readPhotos(r *http.Request, maxBytes int64) ([]upload, int, string) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, http.StatusUnprocessableEntity, msgMissing
	}
	var out []upload
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return nil, http.StatusRequestEntityTooLarge, "Image too large"
			}
			return nil, http.StatusBadRequest, "Malformed multipart body"
		}
		if p.FormName() != "images" {
			_ = p.Close()
			continue
		}
		if p.FileName() == "" {
			_ = p.Close()
			return nil, http.StatusUnprocessableEntity, "Value error, Expected UploadFile, received: <class 'str'>"
		}
		if len(out) == maxPhotos {
			_ = p.Close()
			return nil, http.StatusBadRequest, "Too many images (max 8)"
		}
		data, err := io.ReadAll(io.LimitReader(p, maxBytes+1))
		_ = p.Close()
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return nil, http.StatusRequestEntityTooLarge, "Image too large"
			}
			return nil, http.StatusBadRequest, "Malformed multipart body"
		}
		out = append(out, upload{data: data, tooLarge: int64(len(data)) > maxBytes})
	}
	if len(out) == 0 {
		return nil, http.StatusUnprocessableEntity, msgMissing
	}
	for _, u := range out {
		if u.tooLarge {
			return nil, http.StatusRequestEntityTooLarge, "Image too large"
		}
	}
	return out, 0, ""
}

// handleFromImage is POST /recipes/from-image/jobs (#19): 1–8 photos, each size-gated, decoded
// (400 "Empty image upload" / "Unrecognized image file"), oriented, shrunk and stored as JPEG;
// then the ingestion and its parse job, stamped with the household. 202 {ingestion_id, job_id}.
func (m *Module) handleFromImage(w http.ResponseWriter, r *http.Request, c caller) {
	ctx := r.Context()
	qv := newQueryVals(r)
	var titleHint *string
	if vals, ok := qv.q["title_hint"]; ok && len(vals) > 0 {
		titleHint = &vals[len(vals)-1]
	}
	tierMax := int64(3)
	if vals, ok := qv.q["tier_max"]; ok && len(vals) > 0 {
		n, msg := parseIntString(vals[len(vals)-1])
		if msg != "" {
			qv.fail("tier_max", msg)
		}
		tierMax = n
	}
	maxBytes := m.settings.Int(ctx, SettingImageMaxBytes, settings.Scope{})
	r.Body = http.MaxBytesReader(w, r.Body, (maxBytes+multipartSlack)*(maxPhotos+1))
	photos, status, msg := readPhotos(r, maxBytes)
	if status == http.StatusUnprocessableEntity {
		qv.errs = append(qv.errs, fieldErr{loc: []any{"body", "images"}, msg: msg})
	}
	if !qv.done(w) {
		return
	}
	if status != 0 {
		httpx.Error(w, status, msg)
		return
	}
	prepared := make([][]byte, 0, len(photos))
	for _, p := range photos {
		if len(p.data) == 0 {
			httpx.Error(w, http.StatusBadRequest, "Empty image upload")
			return
		}
		out, ok := preparePhoto(p.data)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "Unrecognized image file")
			return
		}
		prepared = append(prepared, out)
	}
	ingestionID, jobID, err := m.queuePhotoImport(ctx, c, prepared, tierMax, titleHint, nil)
	if err != nil {
		var ue *uploadError
		if errors.As(err, &ue) {
			httpx.Error(w, http.StatusInternalServerError, "Failed to upload images: "+ue.err.Error())
			return
		}
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"ingestion_id": ingestionID, "job_id": jobID})
}

// uploadError is a blob store failure while storing the photos (the route's 500 "Failed to
// upload images: …").
type uploadError struct{ err error }

func (e *uploadError) Error() string { return "upload images: " + e.err.Error() }
func (e *uploadError) Unwrap() error { return e.err }

// queuePhotoImport stores prepared photos and creates the ingestion and its recipes.image job
// for the caller (stamped with their write household), then wakes the queue. extra is merged
// into the job data (the chat import's "save"). Every photo import goes through here: the
// route and the chat tool (photo_chat.go).
func (m *Module) queuePhotoImport(ctx context.Context, c caller, prepared [][]byte, tierMax int64, titleHint *string,
	extra map[string]any) (ingestionID, jobID string, err error) {
	if m.deps.Blobs == nil {
		return "", "", errors.New("no blob store")
	}
	ingestionID = newUUID()
	keys := make([]string, 0, len(prepared))
	removeBlobs := func() {
		for _, k := range keys {
			_ = m.deps.Blobs.Delete(context.WithoutCancel(ctx), k)
		}
	}
	for i, data := range prepared {
		k := ingestKey(c.uid(), ingestionID, i)
		if _, err := m.deps.Blobs.Put(ctx, k, bytes.NewReader(data), "image/jpeg"); err != nil {
			removeBlobs()
			m.deps.Log.Error("recipes: from-image upload failed", "user_id", c.ID, "ingestion_id", ingestionID, "err", err)
			return "", "", &uploadError{err}
		}
		keys = append(keys, k)
	}
	rawKeys, _ := json.Marshal(keys)
	data := map[string]any{"ingestion_id": ingestionID, "tier_max": tierMax, "title_hint": titleHint}
	for k, v := range extra {
		data[k] = v
	}
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := ensureUser(ctx, tx, c.uid()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recipes_recipe_ingestions (id, user_id, household_id, status,
			image_s3_keys, tier_max, title_hint, created_at) VALUES (?, ?, ?, 'PENDING', ?, ?, ?, ?)`,
			ingestionID, c.uid(), c.hh(), string(rawKeys), tierMax, titleHint, ts(m.now())); err != nil {
			return err
		}
		var err error
		jobID, err = m.createJob(ctx, tx, c, jobTypeImage, imageJobType, data)
		return err
	})
	if err != nil {
		removeBlobs()
		return "", "", err
	}
	m.deps.Queue.Notify(imageJobType)
	m.deps.Log.Info("recipes: from-image job queued", "user_id", c.ID, "ingestion_id", ingestionID,
		"parse_job_id", jobID, "images", len(keys))
	return ingestionID, jobID, nil
}

// --- the recipes.image handler ---

func (m *Module) runImage(ctx context.Context, qj queue.Job) ([]byte, error) {
	j, run, err := m.claimJob(ctx, qj)
	if err != nil || !run {
		return nil, err
	}
	err = m.photoJob(ctx, j)
	if err != nil && !errors.Is(err, context.Canceled) {
		m.deps.Log.Error("recipes: image job failed", "parse_job_id", j.ID, "job_type", j.JobType, "err", err)
		err = m.markError(context.WithoutCancel(ctx), j.ID, "ocr_completion_error", err.Error())
	}
	if err == nil && photoJobData(j).Save {
		m.notifySavedImport(context.WithoutCancel(ctx), j.ID)
	}
	return nil, err
}

// photoJobInput is a recipes.image job's data.
type photoJobInput struct {
	IngestionID string `json:"ingestion_id"`
	// Save: commit the draft as a recipe when it is ready and tell the user (the chat's
	// "save this as a recipe", photo_chat.go). The app's import leaves the draft for review.
	Save bool `json:"save"`
}

func photoJobData(j parseJob) photoJobInput {
	var d photoJobInput
	_ = json.Unmarshal([]byte(j.JobData.String), &d)
	return d
}

// setIngestion records the ingestion's status (and pipeline_json / ocr_readings when given).
func (m *Module) setIngestion(ctx context.Context, id, status string, pipeline, readings any) error {
	var p, rd any
	if pipeline != nil {
		raw, _ := json.Marshal(pipeline)
		p = string(raw)
	}
	if readings != nil {
		raw, _ := json.Marshal(readings)
		rd = string(raw)
	}
	_, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_ingestions SET status = ?,
		pipeline_json = COALESCE(?, pipeline_json), ocr_readings = COALESCE(?, ocr_readings) WHERE id = ?`,
		status, p, rd, id)
	return err
}

// canceled reports whether the job left RUNNING (canceled, or purged) while it worked.
func (m *Module) canceled(ctx context.Context, id string) bool {
	j, ok, err := loadJob(ctx, m.deps.DB.Read, id)
	return err == nil && (!ok || finalStatus(j.Status))
}

func (m *Module) photoJob(ctx context.Context, j parseJob) error {
	data := photoJobData(j)
	if data.IngestionID == "" {
		return m.markError(ctx, j.ID, "invalid_job_data", "Missing ingestion_id in job data")
	}
	var rawKeys string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT image_s3_keys FROM recipes_recipe_ingestions WHERE id = ?`,
		data.IngestionID).Scan(&rawKeys)
	if errors.Is(err, sql.ErrNoRows) {
		return m.markError(ctx, j.ID, "invalid_ingestion", "Ingestion not found")
	}
	if err != nil {
		return err
	}
	var keys []string
	_ = json.Unmarshal([]byte(rawKeys), &keys)
	imgs := make([]ocr.Image, 0, len(keys))
	for _, k := range keys {
		rc, _, err := m.deps.Blobs.Get(ctx, k)
		if err != nil {
			return fmt.Errorf("load %s: %w", k, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		imgs = append(imgs, ocr.Image{Data: b, ContentType: "image/jpeg"})
	}
	if err := m.setIngestion(ctx, data.IngestionID, "RUNNING", nil, nil); err != nil {
		return err
	}
	fail := func(code, msg string, pipeline any) error {
		m.deps.Log.Info("recipes: image job done", "parse_job_id", j.ID, "job_type", j.JobType, "outcome", code)
		if err := m.setIngestion(ctx, data.IngestionID, "FAILED", pipeline, nil); err != nil {
			return err
		}
		return m.markError(ctx, j.ID, code, msg)
	}
	if m.OCR == nil {
		return fail("ocr_unavailable", "OCR is not available on this server", nil)
	}
	octx, cancel := context.WithTimeout(ctx, ocrTimeout)
	raw, err := m.OCR.Recognize(octx, imgs, ocr.Options{LanguageHints: []string{"en"}, Mode: "document"}, nil)
	cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fail("ocr_unavailable", err.Error(), nil)
	}
	readings := ocrq.Sort(ocrq.FromOCR(raw))
	if err := m.setIngestion(ctx, data.IngestionID, "RUNNING", nil, readings); err != nil {
		return err
	}
	texts := ocrq.Texts(readings)
	if len(texts) == 0 {
		// B8: the job fails only when every engine read nothing.
		return fail("ocr_no_text", "No OCR text extracted from any image", nil)
	}
	quality := ocrq.ScoreQuality(ocrq.GateText(texts), ocrq.MeanConfidence(readings))
	if quality.HardFail || !quality.PassGate {
		m.deps.Log.Warn("recipes: photo quality gate failed", "parse_job_id", j.ID, "char_count", quality.CharCount,
			"line_count", quality.LineCount, "token_count", quality.TokenCount, "gibberish", quality.Gibberish)
		return fail("quality_gate_failed", gateFailedMsg, map[string]any{
			"attempts": []any{map[string]any{"tier": 1, "status": "failed_quality", "metrics": quality}},
			"error":    gateFailedMsg,
		})
	}
	if m.canceled(ctx, j.ID) {
		return nil
	}

	label := m.label(ctx, SettingLightweightModel)
	if m.settings.String(ctx, SettingLightweightModel, settings.Scope{}) == "" {
		label = m.label(ctx, SettingFullModel) // P2 with no model name used the full model
	}
	draft, err := m.structure(ctx, label, texts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fail("text_structuring_exception", err.Error(), map[string]any{
			"attempts": []any{map[string]any{"tier": 1, "status": "failed_text_structuring", "error": err.Error()}},
		})
	}
	if ocrq.NeedsCleanup(draft) {
		if m.canceled(ctx, j.ID) {
			return nil
		}
		draft = m.cleanDraft(ctx, label, draft)
	}
	if err := draft.ValidateMinimums(); err != nil {
		return fail("draft_validation_failed", err.Error(), map[string]any{
			"attempts": []any{map[string]any{"tier": 1, "status": "failed_validation", "metrics": quality, "error": err.Error()}},
		})
	}
	attempts := []any{}
	providers := []string{}
	for _, r := range readings {
		var failed []any
		for _, ir := range r.Results {
			if ir.Error != "" {
				failed = append(failed, map[string]any{"index": ir.Index, "code": "ocr_failed", "message": ir.Error})
			}
		}
		var perImage any
		if len(failed) > 0 {
			perImage = failed
		}
		attempts = append(attempts, map[string]any{"tier": 1, "status": "success", "provider": r.Provider,
			"image_count": len(r.Results), "failed_images": len(failed), "per_image_errors": perImage})
	}
	for _, t := range texts {
		providers = append(providers, t.Provider)
	}
	pipeline := map[string]any{"attempts": attempts, "metrics": quality, "providers_used": providers, "selected_tier": 1}
	if err := m.setIngestion(ctx, data.IngestionID, "SUCCEEDED", pipeline, nil); err != nil {
		return err
	}
	m.deps.Log.Info("recipes: image job done", "parse_job_id", j.ID, "job_type", j.JobType, "outcome", "complete",
		"providers", providers, "ingredients", len(draft.Ingredients), "steps", len(draft.Steps))
	// B7 fixed: completed_at is set (markComplete), so photo jobs reach the list and the cleanup.
	result := map[string]any{"recipe_draft": draft, "pipeline": pipeline}
	if err := m.markComplete(ctx, j.ID, result); err != nil {
		return err
	}
	if data.Save {
		return m.saveDraft(ctx, j, draft, result)
	}
	return nil
}

// structure is call_text_structuring + _parse_with_repair: P2 on the readings, the reply
// coerced, else repaired locally, else repaired by the full model (P1r with the draft hint).
func (m *Module) structure(ctx context.Context, label string, texts []ocrq.Text) (*ocrq.Draft, error) {
	reply, err := m.chatJSON(ctx, label, 0, p2MaxTokens, p2Timeout, ocrq.P2System(len(texts)), ocrq.P2User(texts))
	if err != nil {
		return nil, err
	}
	if reply == "" {
		return nil, errors.New("LLM text structuring missing content")
	}
	reply = extract.StripControlChars(reply)
	d, err := ocrq.Coerce(reply, "ocr")
	if err == nil {
		return d, nil
	}
	if fixed, ok := extract.LocalRepair(reply); ok {
		if d, err := ocrq.Coerce(fixed, "ocr"); err == nil {
			return d, nil
		}
	}
	fixed, ferr := m.chatJSON(ctx, m.label(ctx, SettingFullModel), 0, p1MaxToken, p2Timeout, extract.P1rSystem,
		extract.P1rUser(ocrq.DraftSchemaHint, reply))
	if ferr == nil {
		if good, ok := extract.RepairReply(fixed); ok {
			return ocrq.Coerce(good, "ocr")
		}
	}
	return nil, err
}

// cleanDraft is clean_and_validate_draft (P3): any failure keeps the draft as it was.
func (m *Module) cleanDraft(ctx context.Context, label string, d *ocrq.Draft) *ocrq.Draft {
	reply, err := m.chatJSON(ctx, label, 0, p3MaxTokens, p3Timeout, ocrq.P3System, ocrq.P3User(d))
	if err != nil || reply == "" {
		m.deps.Log.Warn("recipes: draft cleanup failed; keeping the draft", "err", err)
		return d
	}
	src := d.Source.Type
	if src == "" {
		src = "ocr"
	}
	cleaned, err := ocrq.Coerce(ocrq.StripFence(trimSpace(reply)), src)
	if err != nil {
		m.deps.Log.Warn("recipes: cleaned draft unusable; keeping the draft", "err", err)
		return d
	}
	return cleaned
}

func trimSpace(s string) string { return string(bytes.TrimSpace([]byte(s))) }
