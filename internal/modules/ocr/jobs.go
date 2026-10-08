package ocr

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Queued OCR work. A job's state lives in the blob store next to its images, under
// ocr/jobs/<job_id>/ (job.json plus image-<index>), so the module needs no table: the legacy
// state was a Redis key with a 24 h TTL, and the purge job keeps that retention. The work runs
// as durable queue jobs: ocr.job (the OCR itself), ocr.callback (the completion POST) and
// ocr.purge (retention).

const (
	jobType      = "ocr.job"
	callbackType = "ocr.callback"
	purgeType    = "ocr.purge"
	jobPrefix    = "ocr/jobs/"
)

// Job kinds: a POST /v1/ocr job (one image, OCRResponse result) or a POST /v1/ocr/jobs job
// (1-8 images, the queue-flow completion payload as its result).
const (
	kindSingle = "single"
	kindFlow   = "flow"
)

// Visible job states (legacy OCRJobStatusResponse.status).
const (
	statusPending    = "pending"
	statusProcessing = "processing"
	statusCompleted  = "completed"
	statusFailed     = "failed"
)

type storedImage struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

type jobRecord struct {
	JobID     string          `json:"job_id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt *string         `json:"updated_at"`
	Result    json.RawMessage `json:"result"`
	Error     *string         `json:"error"`

	Images        []storedImage `json:"images"`
	Provider      string        `json:"provider"`
	Options       Options       `json:"options"`
	Language      string        `json:"language"`
	DocumentID    *string       `json:"document_id"`
	CorrelationID string        `json:"correlation_id"`
	MaxAttempts   int           `json:"max_attempts"`

	// Flow jobs: who to tell, and the ids the completion message carries.
	CallbackURL  string `json:"callback_url,omitempty"`
	WorkflowID   string `json:"workflow_id,omitempty"`
	ParentJobID  string `json:"parent_job_id,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	Source       string `json:"source,omitempty"`
	CompletionID string `json:"completion_id,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
}

// statusView is GET /v1/ocr/jobs/{id} (legacy OCRJobStatusResponse).
func (j *jobRecord) statusView() map[string]any {
	var result any
	if len(j.Result) > 0 {
		result = j.Result
	}
	return map[string]any{
		"job_id": j.JobID, "status": j.Status, "created_at": j.CreatedAt,
		"updated_at": j.UpdatedAt, "result": result, "error": j.Error,
	}
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// pyUTC formats like the legacy datetime.utcnow().isoformat() + "Z".
func pyUTC(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000") + "Z" }

func jobKey(id string) string { return jobPrefix + id + "/job.json" }

func validJobID(id string) bool {
	return id != "" && blob.ValidateKey(jobPrefix+id+"/job.json") == nil && !strings.Contains(id, "/")
}

func (m *Module) loadJob(ctx context.Context, id string) (*jobRecord, error) {
	rc, _, err := m.deps.Blobs.Get(ctx, jobKey(id))
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var j jobRecord
	if err := json.NewDecoder(rc).Decode(&j); err != nil {
		return nil, fmt.Errorf("ocr: decode job %s: %w", id, err)
	}
	return &j, nil
}

func (m *Module) saveJob(ctx context.Context, j *jobRecord) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = m.deps.Blobs.Put(ctx, jobKey(j.JobID), bytes.NewReader(b), "application/json")
	return err
}

func (m *Module) touch(j *jobRecord, status string) {
	j.Status = status
	j.UpdatedAt = strp(pyUTC(m.now()))
}

// errQueueUnavailable means the module runs without a queue or blob store.
var errQueueUnavailable = errors.New("queue or blob store not configured")

// createJob stores the images and the record, then enqueues the work.
func (m *Module) createJob(ctx context.Context, j *jobRecord, imgs []Image) error {
	if m.deps.Queue == nil || m.deps.Blobs == nil {
		return errQueueUnavailable
	}
	j.JobID = newUUID()
	j.Status = statusPending
	j.CreatedAt = pyUTC(m.now())
	j.MaxAttempts = max(1, int(m.settings.Int(ctx, "ocr.max_attempts", settings.Scope{})))
	for i, img := range imgs {
		key := fmt.Sprintf("%s%s/image-%d", jobPrefix, j.JobID, i)
		if _, err := m.deps.Blobs.Put(ctx, key, bytes.NewReader(img.Data), img.ContentType); err != nil {
			return err
		}
		j.Images = append(j.Images, storedImage{Key: key, ContentType: img.ContentType})
	}
	if err := m.saveJob(ctx, j); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"job_id": j.JobID})
	_, err := m.deps.Queue.Enqueue(ctx, jobType, payload, queue.Options{MaxAttempts: j.MaxAttempts})
	return err
}

func jobIDFrom(job queue.Job) string {
	var p struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(job.Payload, &p)
	return p.JobID
}

func (m *Module) readImages(ctx context.Context, j *jobRecord) ([]Image, error) {
	out := make([]Image, 0, len(j.Images))
	for _, si := range j.Images {
		rc, _, err := m.deps.Blobs.Get(ctx, si.Key)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, Image{Data: data, ContentType: si.ContentType})
	}
	return out, nil
}

// runJob is the ocr.job handler.
func (m *Module) runJob(ctx context.Context, job queue.Job) ([]byte, error) {
	id := jobIDFrom(job)
	j, err := m.loadJob(ctx, id)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, queue.Permanent(fmt.Errorf("ocr: job %s is gone", id))
		}
		return nil, err
	}
	if j.Status == statusCompleted || j.Status == statusFailed {
		return nil, nil
	}
	m.touch(j, statusProcessing)
	if err := m.saveJob(ctx, j); err != nil {
		return nil, err
	}

	imgs, err := m.readImages(ctx, j)
	if err == nil {
		switch j.Kind {
		case kindFlow:
			err = m.processFlow(ctx, j, imgs)
		default:
			err = m.processSingle(ctx, j, imgs[0])
		}
	}
	if err != nil {
		if job.Attempt < j.MaxAttempts {
			m.touch(j, statusPending) // the queue retries it
			m.saveJob(ctx, j)
			return nil, err
		}
		m.touch(j, statusFailed)
		j.Error = strp(truncRunes(err.Error(), 500))
		if j.Kind == kindFlow {
			payload := completionPayload("failed", nil, &codedError{Code: "internal_error", Message: truncRunes(err.Error(), 200)})
			j.Result, _ = json.Marshal(payload)
		}
	}
	return nil, m.finish(ctx, j)
}

// finish persists a terminal job, drops its images and queues the callback.
func (m *Module) finish(ctx context.Context, j *jobRecord) error {
	j.FinishedAt = pyUTC(m.now())
	if j.CallbackURL != "" {
		j.CompletionID = newUUID()
	}
	if err := m.saveJob(ctx, j); err != nil {
		return err
	}
	for _, si := range j.Images {
		m.deps.Blobs.Delete(ctx, si.Key)
	}
	if j.CallbackURL != "" {
		payload, _ := json.Marshal(map[string]string{"job_id": j.JobID})
		if _, err := m.deps.Queue.Enqueue(ctx, callbackType, payload, queue.Options{}); err != nil {
			m.deps.Log.Error("ocr: enqueue callback", "job_id", j.JobID, "err", err)
		}
	}
	return nil
}

// ocrResponse is the legacy OCRResponse.
type ocrResponse struct {
	ProviderUsed string         `json:"provider_used"`
	Text         string         `json:"text"`
	Blocks       []Block        `json:"blocks"`
	Meta         map[string]any `json:"meta"`
}

// processSingle runs a POST /v1/ocr job: "auto" walks the tier chain (normalized, validated,
// truncated text, as the legacy worker did); a named provider is used as is.
func (m *Module) processSingle(ctx context.Context, j *jobRecord, img Image) error {
	var resp ocrResponse
	if j.Provider == "auto" {
		res, tr := m.tierChain(ctx, 0, img, j.Language, j.Options.ReturnBoxes)
		if res.Error != nil {
			m.touch(j, statusFailed)
			j.Error = strp(res.Error.Message)
			return nil
		}
		blocks := tr.Blocks
		if blocks == nil {
			blocks = []Block{}
		}
		resp = ocrResponse{ProviderUsed: tierToEngine[res.Meta.Tier], Text: res.OCRText, Blocks: blocks, Meta: map[string]any{
			"duration_ms": round2(ms(tr.Duration)), "tier": res.Meta.Tier, "is_valid": true,
			"confidence": res.Meta.Confidence, "validation_reason": res.Meta.ValidationReason,
			"truncated": res.Truncated, "language": res.Meta.Language,
		}}
	} else {
		results, used, err := m.batch(ctx, []Image{img}, j.Provider, j.Options)
		if err != nil {
			var ue unavailableError
			var pe processingError
			if errors.As(err, &ue) || errors.As(err, &pe) {
				m.touch(j, statusFailed)
				j.Error = strp(err.Error())
				return nil
			}
			return err
		}
		r := results[0]
		resp = ocrResponse{ProviderUsed: used, Text: r.Text, Blocks: r.Blocks, Meta: map[string]any{"duration_ms": round2(ms(r.Duration))}}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	m.touch(j, statusCompleted)
	j.Result = b
	j.Error = nil
	return nil
}

// completionPayload is the legacy ocr.completed payload.
func completionPayload(status string, results []imageResult, e *codedError) map[string]any {
	if results == nil {
		results = []imageResult{}
	}
	var errObj any = map[string]any{"message": nil, "code": nil}
	if status == "failed" && e != nil {
		errObj = e
	}
	return map[string]any{"status": status, "results": results, "artifact_ref": nil, "error": errObj}
}

// processFlow runs a POST /v1/ocr/jobs job: every image through the tier chain.
func (m *Module) processFlow(ctx context.Context, j *jobRecord, imgs []Image) error {
	results := make([]imageResult, 0, len(imgs))
	status := "failed"
	for i, img := range imgs {
		r, _ := m.tierChain(ctx, i, img, j.Language, false)
		if r.Meta.IsValid {
			status = "success"
		}
		results = append(results, r)
	}
	sort.Slice(results, func(a, b int) bool { return results[a].Index < results[b].Index })
	var e *codedError
	if status == "failed" {
		e = &codedError{Code: "ocr_no_valid_output", Message: "No image produced valid text"}
	}
	b, err := json.Marshal(completionPayload(status, results, e))
	if err != nil {
		return err
	}
	m.touch(j, statusCompleted)
	j.Result = b
	return nil
}

// completionMessage is the legacy queue-flow ocr.completed envelope, which the callback
// receiver can hand to its own queue unchanged.
func (j *jobRecord) completionMessage() map[string]any {
	var payload any
	json.Unmarshal(j.Result, &payload)
	workflow := j.WorkflowID
	if workflow == "" {
		workflow = j.JobID
	}
	parent := j.ParentJobID
	if parent == "" {
		parent = j.JobID
	}
	var reqID any
	if j.RequestID != "" {
		reqID = j.RequestID
	}
	target := j.Source
	if target == "" {
		target = "unknown"
	}
	return map[string]any{
		"schema_version": 1,
		"job_id":         j.CompletionID,
		"workflow_id":    workflow,
		"job_type":       "ocr.completed",
		"source":         "jarvis-ocr-service",
		"target":         target,
		"created_at":     j.FinishedAt,
		"attempt":        1,
		"reply_to":       nil,
		"payload":        payload,
		"trace":          map[string]any{"request_id": reqID, "parent_job_id": parent},
		"ocr_job_id":     j.JobID,
	}
}

// runCallback is the ocr.callback handler: POST the completion message with jarvisd's app
// credentials until the receiver answers 2xx.
func (m *Module) runCallback(ctx context.Context, job queue.Job) ([]byte, error) {
	j, err := m.loadJob(ctx, jobIDFrom(job))
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, queue.Permanent(err)
		}
		return nil, err
	}
	body, err := json.Marshal(j.completionMessage())
	if err != nil {
		return nil, queue.Permanent(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, j.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return nil, queue.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	id, key, err := m.appCreds(ctx)
	if err != nil {
		return nil, fmt.Errorf("ocr: callback %s: %w", j.JobID, err)
	}
	if id != "" {
		req.Header.Set("X-Jarvis-App-Id", id)
		req.Header.Set("X-Jarvis-App-Key", key)
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("ocr: callback %s: %w", j.JobID, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ocr: callback %s: receiver answered %d", j.JobID, resp.StatusCode)
	}
	return nil, nil
}

// jobRetention matches the legacy Redis TTL (24 h after the last update).
const jobRetention = 24 * time.Hour

// runPurge drops job records (and any images) last written more than jobRetention ago.
func (m *Module) runPurge(ctx context.Context, _ queue.Job) ([]byte, error) {
	infos, err := m.deps.Blobs.List(ctx, jobPrefix)
	if err != nil {
		return nil, err
	}
	cutoff := m.now().Add(-jobRetention)
	byJob := map[string][]blob.Info{}
	for _, in := range infos {
		rest := strings.TrimPrefix(in.Key, jobPrefix)
		id, _, _ := strings.Cut(rest, "/")
		byJob[id] = append(byJob[id], in)
	}
	for _, files := range byJob {
		newest := time.Time{}
		for _, f := range files {
			if f.ModTime.After(newest) {
				newest = f.ModTime
			}
		}
		if newest.After(cutoff) {
			continue
		}
		for _, f := range files {
			if err := m.deps.Blobs.Delete(ctx, f.Key); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

// queueCounts is the pending/processing tally for /v1/queue/status.
func (m *Module) queueCounts(ctx context.Context) (pending, processing int, err error) {
	if m.deps.Blobs == nil {
		return 0, 0, nil
	}
	infos, err := m.deps.Blobs.List(ctx, jobPrefix)
	if err != nil {
		return 0, 0, err
	}
	for _, in := range infos {
		if !strings.HasSuffix(in.Key, "/job.json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(in.Key, jobPrefix), "/job.json")
		j, err := m.loadJob(ctx, id)
		if err != nil {
			continue
		}
		switch j.Status {
		case statusPending:
			pending++
		case statusProcessing:
			processing++
		}
	}
	return pending, processing, nil
}

// appCreds are the credentials callbacks are signed with: AppID/AppKey when set (the legacy
// JARVIS_APP_ID/JARVIS_APP_KEY env), else jarvisd's own app client via AppCreds.
func (m *Module) appCreds(ctx context.Context) (id, key string, err error) {
	if m.AppID != "" || m.AppCreds == nil {
		return m.AppID, m.AppKey, nil
	}
	return m.AppCreds(ctx)
}
