package llm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// The durable job path (03 §11): legacy enqueue → Redis/RQ → worker → callback, on the
// platform queue. One llm.chat job type serves the external /internal/queue/enqueue route and
// in-process Enqueue; completions are delivered by their own retried jobs (llm.callback for
// an HTTP callback, llm.notify for an in-process handler), so a delivery failure never re-runs
// the model.

const (
	jobChat     = "llm.chat"
	jobCallback = "llm.callback"
	jobNotify   = "llm.notify"
	jobPurge    = "llm.purge"

	// chatAttempts: an engine or transport failure is retried once (10 s later); 4xx are not.
	chatAttempts = 2
)

// Callback is where an external caller wants the result envelope POSTed.
type Callback struct {
	URL      string `json:"url"`
	AuthType string `json:"auth_type,omitempty"`
	Token    string `json:"token,omitempty"`
}

// Job is one queued chat completion. The request always runs on the background label (the
// queue never uses live, 03 §7.1).
type Job struct {
	JobID          string          `json:"job_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	JobType        string          `json:"job_type"`
	TraceID        string          `json:"trace_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	TTL            time.Duration   `json:"ttl"`
	Request        ChatRequest     `json:"request"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	// Callback is for external callers; Notify names an in-process handler (OnComplete).
	Callback *Callback `json:"callback,omitempty"`
	Notify   string    `json:"notify,omitempty"`
}

// JobError is a failed job's error.
type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JobResult is a finished job, handed to the OnComplete handler (and, as the legacy envelope,
// to a callback URL).
type JobResult struct {
	JobID        string          `json:"job_id"`
	JobType      string          `json:"job_type"`
	FinishedAt   time.Time       `json:"finished_at"`
	Status       string          `json:"status"` // "succeeded" or "failed"
	Content      *string         `json:"content,omitempty"`
	Error        *JobError       `json:"error,omitempty"`
	ProcessingMS int64           `json:"processing_ms"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

// NotifyFunc receives an in-process job's result. An error retries the delivery.
type NotifyFunc func(ctx context.Context, r JobResult) error

// OnComplete registers an in-process completion handler; Job.Notify names it.
func (s *Service) OnComplete(name string, fn NotifyFunc) {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	s.handlers[name] = fn
}

func errExpired() *APIError {
	return &APIError{Status: 400, Type: "invalid_request_error", Message: "Job already expired", Code: "expired"}
}

// Enqueue queues a chat job. A repeat of (JobID, IdempotencyKey) within TTL of the first
// enqueue is not queued again and reports deduped (03 §7.2), even after the first finished.
func (s *Service) Enqueue(ctx context.Context, j Job) (id string, deduped bool, err error) {
	if s.queue == nil || s.db == nil {
		return "", false, errors.New("llm: queue not available")
	}
	now := s.now()
	if j.JobID == "" {
		j.JobID = newHex(32)
	}
	if j.IdempotencyKey == "" {
		j.IdempotencyKey = j.JobID
	}
	if j.JobType == "" {
		j.JobType = "chat"
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	if j.TTL <= 0 {
		j.TTL = 24 * time.Hour
	}
	if j.CreatedAt.Add(j.TTL).Before(now) {
		return "", false, errExpired()
	}
	j.Request.Label = LabelBackground
	payload, err := json.Marshal(j)
	if err != nil {
		return "", false, err
	}
	key := j.JobID + ":" + j.IdempotencyKey
	expires := now.Add(j.TTL).UnixMilli()
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM llm_dedupe WHERE key = ? AND expires_at <= ?`, key, now.UnixMilli()); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO llm_dedupe (key, expires_at) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`, key, expires)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			deduped = true
			return nil
		}
		_, err = s.queue.EnqueueTx(ctx, tx, jobChat, payload, queue.Options{MaxAttempts: chatAttempts})
		return err
	})
	if err != nil {
		return "", false, err
	}
	if !deduped {
		s.queue.Notify(jobChat)
	}
	s.log.Info("llm: job enqueued", "job_id", j.JobID, "deduped", deduped)
	return j.JobID, deduped, nil
}

// retryable: engine faults and transport errors are worth one more try; a request the engine
// refused (4xx) is not.
func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500
	}
	return true
}

// runChat is the llm.chat handler. A job past its TTL is not run; it completes with an
// "expired" failure (D8: the legacy worker crashed with a TypeError and never called back).
func (s *Service) runChat(ctx context.Context, qj queue.Job) ([]byte, error) {
	var j Job
	if err := json.Unmarshal(qj.Payload, &j); err != nil {
		return nil, queue.Permanent(err)
	}
	started := s.now()
	r := JobResult{JobID: j.JobID, JobType: j.JobType, Metadata: j.Metadata, Status: "failed"}
	if j.TTL > 0 && j.CreatedAt.Add(j.TTL).Before(started) {
		r.Error = &JobError{Code: "expired", Message: "Job expired before processing"}
		return nil, s.deliver(ctx, j, r, started)
	}
	j.Request.Label = LabelBackground
	resp, err := s.Chat(ctx, j.Request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err // shutting down or lease over: the queue picks it up again
		}
		if retryable(err) && qj.Attempt < chatAttempts {
			s.log.Warn("llm: job failed, will retry", "job_id", j.JobID, "attempt", qj.Attempt, "err", err)
			return nil, err
		}
		ae := asAPIError(err)
		r.Error = &JobError{Code: ae.Type, Message: ae.Message}
		s.log.Warn("llm: job failed", "job_id", j.JobID, "code", ae.Type)
		return nil, s.deliver(ctx, j, r, started)
	}
	r.Status = "succeeded"
	r.Content = &resp.Content
	s.log.Info("llm: job done", "job_id", j.JobID, "ms", s.now().Sub(started).Milliseconds())
	return nil, s.deliver(ctx, j, r, started)
}

// callbackJob is an llm.callback payload.
type callbackJob struct {
	Callback Callback        `json:"callback"`
	TraceID  string          `json:"trace_id,omitempty"`
	JobID    string          `json:"job_id"`
	Envelope json.RawMessage `json:"envelope"`
}

type notifyJob struct {
	Name   string    `json:"name"`
	Result JobResult `json:"result"`
}

// envelope is the legacy callback body (_build_callback_envelope), keys in its order.
type envelope struct {
	JobID      string          `json:"job_id"`
	JobType    string          `json:"job_type"`
	FinishedAt string          `json:"finished_at"`
	Status     string          `json:"status"`
	Result     any             `json:"result"`
	Error      *JobError       `json:"error"`
	Timing     map[string]any  `json:"timing"`
	Metadata   json.RawMessage `json:"metadata"`
}

func buildEnvelope(r JobResult) envelope {
	var result any
	if r.Content != nil {
		result = map[string]string{"content": *r.Content}
	}
	meta := r.Metadata
	if len(meta) == 0 || string(meta) == "null" {
		meta = json.RawMessage("{}")
	}
	return envelope{JobID: r.JobID, JobType: r.JobType, FinishedAt: r.FinishedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Status: r.Status, Result: result, Error: r.Error, Timing: map[string]any{"processing_ms": r.ProcessingMS}, Metadata: meta}
}

// deliver queues the result's delivery jobs.
func (s *Service) deliver(ctx context.Context, j Job, r JobResult, started time.Time) error {
	r.FinishedAt = s.now()
	r.ProcessingMS = max(0, r.FinishedAt.Sub(started).Milliseconds())
	if cb := j.Callback; cb != nil {
		typ := strings.ToLower(cb.AuthType)
		switch {
		case cb.URL == "":
			s.log.Warn("llm: callback skipped: missing URL", "job_id", j.JobID)
		case typ != "" && typ != "internal" && typ != "bearer":
			s.log.Warn("llm: callback skipped: unsupported auth type", "job_id", j.JobID, "auth_type", typ)
		default:
			env, err := json.Marshal(buildEnvelope(r))
			if err != nil {
				return queue.Permanent(err)
			}
			p, _ := json.Marshal(callbackJob{Callback: *cb, TraceID: j.TraceID, JobID: j.JobID, Envelope: env})
			if _, err := s.queue.Enqueue(ctx, jobCallback, p, queue.Options{}); err != nil {
				return err
			}
		}
	}
	if j.Notify != "" {
		p, _ := json.Marshal(notifyJob{Name: j.Notify, Result: r})
		if _, err := s.queue.Enqueue(ctx, jobNotify, p, queue.Options{}); err != nil {
			return err
		}
	}
	return nil
}

// runCallback POSTs the envelope (legacy headers: X-Trace-Id, the bearer token, jarvisd's
// own app credentials) until the receiver answers 2xx. A 4xx other than 408/429 won't
// change on retry.
func (m *Module) runCallback(ctx context.Context, qj queue.Job) ([]byte, error) {
	var c callbackJob
	if err := json.Unmarshal(qj.Payload, &c); err != nil {
		return nil, queue.Permanent(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, c.Callback.URL, bytes.NewReader(c.Envelope))
	if err != nil {
		return nil, queue.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.TraceID != "" {
		req.Header.Set("X-Trace-Id", c.TraceID)
	}
	if c.Callback.AuthType == "bearer" && c.Callback.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Callback.Token)
	}
	id, key, err := m.appCreds(ctx)
	if err != nil {
		return nil, fmt.Errorf("llm: callback %s: %w", c.JobID, err)
	}
	if id != "" && key != "" {
		req.Header.Set("X-Jarvis-App-Id", id)
		req.Header.Set("X-Jarvis-App-Key", key)
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: callback %s: %w", c.JobID, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return nil, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429:
		return nil, queue.Permanent(fmt.Errorf("llm: callback %s: receiver answered %d", c.JobID, resp.StatusCode))
	}
	return nil, fmt.Errorf("llm: callback %s: receiver answered %d", c.JobID, resp.StatusCode)
}

func (s *Service) runNotify(ctx context.Context, qj queue.Job) ([]byte, error) {
	var n notifyJob
	if err := json.Unmarshal(qj.Payload, &n); err != nil {
		return nil, queue.Permanent(err)
	}
	s.nmu.RLock()
	fn := s.handlers[n.Name]
	s.nmu.RUnlock()
	if fn == nil {
		return nil, queue.Permanent(fmt.Errorf("llm: no completion handler %q", n.Name))
	}
	return nil, fn(ctx, n.Result)
}

// runPurge drops dedupe keys past their window.
func (s *Service) runPurge(ctx context.Context, _ queue.Job) ([]byte, error) {
	_, err := s.db.Write.ExecContext(ctx, `DELETE FROM llm_dedupe WHERE expires_at <= ?`, s.now().UnixMilli())
	return nil, err
}

// appCreds are the credentials callbacks are signed with: AppID/AppKey when set (the legacy
// JARVIS_APP_ID/JARVIS_APP_KEY env), else jarvisd's own app client via AppCreds.
func (m *Module) appCreds(ctx context.Context) (id, key string, err error) {
	if m.AppID != "" || m.AppCreds == nil {
		return m.AppID, m.AppKey, nil
	}
	return m.AppCreds(ctx)
}
