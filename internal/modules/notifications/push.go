package notifications

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Notification is one push to send. TargetType is "user" (TargetID is the user id) or
// "household" (TargetID is the household id). Priority is "default" (when empty) or "high".
type Notification struct {
	TargetType string
	TargetID   string
	Title      string
	Body       string
	// Data is delivered with the push. A push that backs an inbox item should carry
	// "inbox_item_id" and a stable "type" so mobile can deep-link it.
	Data     map[string]any
	Priority string
	Category string
}

// Delivery is the notification_log row a send produced, in the legacy NotifyResponse shape.
// DeliveryStatus is "pending" while the push job runs, then "delivered", "partial" or
// "failed"; "skipped" means nothing was sent (duplicate, no devices, or no relay).
type Delivery struct {
	ID             string `json:"id"`
	DeliveryStatus string `json:"delivery_status"`
	TokenCount     int    `json:"token_count"`
	SuccessCount   int    `json:"success_count"`
	FailureCount   int    `json:"failure_count"`
}

var (
	ErrTargetType = errors.New("target_type must be 'user' or 'household'")
	ErrPriority   = errors.New("priority must be 'default' or 'high'")
)

// DedupWindow is how long an identical notification (same source, target, title, body and
// category) is suppressed. In memory, as before: a restart forgets it.
const DedupWindow = 60 * time.Second

type dedupCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newDedupCache() *dedupCache { return &dedupCache{seen: map[string]time.Time{}} }

func dedupKey(source, targetID, title, body, category string) string {
	sum := md5.Sum([]byte(source + ":" + targetID + ":" + title + ":" + body + ":" + category))
	return hex.EncodeToString(sum[:])
}

// duplicate reports whether key was seen within the window, recording it if not.
func (d *dedupCache) duplicate(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, t := range d.seen {
		if now.Sub(t) > DedupWindow {
			delete(d.seen, k)
		}
	}
	if _, ok := d.seen[key]; ok {
		return true
	}
	d.seen[key] = now
	return false
}

// Notify sends a push to a target: it writes the notification_log row and, when there are
// devices and a relay, queues the delivery job. With tx non-nil both happen in the caller's
// transaction (docs/cc D31) and the job starts within the queue's poll interval after commit.
func (m *Module) Notify(ctx context.Context, tx *sql.Tx, source string, n Notification) (Delivery, error) {
	if n.TargetType != "user" && n.TargetType != "household" {
		return Delivery{}, ErrTargetType
	}
	if n.Priority == "" {
		n.Priority = "default"
	}
	if n.Priority != "default" && n.Priority != "high" {
		return Delivery{}, ErrPriority
	}
	var d Delivery
	var queued bool
	err := m.inTx(ctx, tx, func(tx *sql.Tx) error {
		var err error
		d, queued, err = m.send(ctx, tx, source, n)
		return err
	})
	if err != nil {
		return Delivery{}, err
	}
	if queued && tx == nil {
		m.deps.Queue.Notify(pushJobType)
	}
	return d, nil
}

// pushJob is the queued delivery.
type pushJob struct {
	LogID       string         `json:"log_id"`
	Tokens      []string       `json:"tokens"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Data        map[string]any `json:"data,omitempty"`
	Priority    string         `json:"priority"`
	HouseholdID string         `json:"household_id"`
}

func (m *Module) send(ctx context.Context, tx *sql.Tx, source string, n Notification) (Delivery, bool, error) {
	logRow := func(status string, tokens int) (Delivery, error) {
		var data any
		if len(n.Data) > 0 {
			s, err := marshalJSON(n.Data)
			if err != nil {
				return Delivery{}, err
			}
			data = s
		}
		var category any
		if n.Category != "" {
			category = n.Category
		}
		d := Delivery{ID: newUUID(), DeliveryStatus: status, TokenCount: tokens}
		_, err := tx.ExecContext(ctx, `INSERT INTO notifications_notification_log
			(id, source_service, target_type, target_id, title, body, data, category, token_count, success_count, failure_count, delivery_status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)`,
			d.ID, source, n.TargetType, n.TargetID, n.Title, n.Body, data, category, tokens, status, ts(m.now()))
		return d, err
	}

	if m.dedup.duplicate(dedupKey(source, n.TargetID, n.Title, n.Body, n.Category), m.now()) {
		m.deps.Log.Info("notifications: duplicate suppressed", "source", source, "target", n.TargetID)
		d, err := logRow("skipped", 0)
		return d, false, err
	}

	var q string
	var arg any
	switch n.TargetType {
	case "user":
		uid, err := strconv.ParseInt(n.TargetID, 10, 64)
		if err != nil {
			// Legacy answered 500 (int() on the id). A non-numeric user has no devices.
			d, err := logRow("skipped", 0)
			return d, false, err
		}
		q, arg = `SELECT push_token, household_id FROM notifications_device_tokens WHERE is_active = 1 AND user_id = ? ORDER BY created_at`, uid
	default:
		q, arg = `SELECT push_token, household_id FROM notifications_device_tokens WHERE is_active = 1 AND household_id = ? ORDER BY created_at`, n.TargetID
	}
	rows, err := tx.QueryContext(ctx, q, arg)
	if err != nil {
		return Delivery{}, false, err
	}
	var tokens []string
	var household string
	for rows.Next() {
		var tok, hh string
		if err := rows.Scan(&tok, &hh); err != nil {
			rows.Close()
			return Delivery{}, false, err
		}
		if household == "" {
			household = hh // all of a target's tokens share a household
		}
		tokens = append(tokens, tok)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Delivery{}, false, err
	}
	if len(tokens) == 0 {
		d, err := logRow("skipped", 0)
		return d, false, err
	}
	if m.relayURL(ctx) == "" || m.deps.Queue == nil {
		// Legacy: with no RELAY_URL every result was "skipped" and so was the row.
		d, err := logRow("skipped", len(tokens))
		return d, false, err
	}
	d, err := logRow("pending", len(tokens))
	if err != nil {
		return Delivery{}, false, err
	}
	payload, err := json.Marshal(pushJob{LogID: d.ID, Tokens: tokens, Title: n.Title, Body: n.Body, Data: n.Data,
		Priority: n.Priority, HouseholdID: household})
	if err != nil {
		return Delivery{}, false, err
	}
	if _, err := m.deps.Queue.EnqueueTx(ctx, tx, pushJobType, payload, queue.Options{}); err != nil {
		return Delivery{}, false, err
	}
	return d, true, nil
}

// --- the push job ---

// relayResult is one per-token outcome from the relay's /v1/send.
type relayResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Token  string `json:"token,omitempty"`
}

// runPush delivers one queued push and records the outcome on its log row. Transient relay
// failures (unreachable, 5xx, no JWT) are returned for the queue to retry; the row shows the
// latest attempt, so it ends "failed" if every attempt fails.
func (m *Module) runPush(ctx context.Context, job queue.Job) ([]byte, error) {
	var p pushJob
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, queue.Permanent(fmt.Errorf("notifications: bad push payload: %w", err))
	}
	// Tokens deactivated or deleted since the send (logout, account deletion: D20) are dropped.
	tokens, err := m.stillActive(ctx, p.Tokens)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, m.recordOutcome(ctx, p.LogID, "skipped", 0, 0, nil)
	}
	results, transient := m.relay.deliver(ctx, tokens, p)

	success, failure := 0, 0
	var ok, dead []string
	allSkipped := true
	for _, res := range results {
		if res.Status != "skipped" {
			allSkipped = false
		}
		switch res.Status {
		case "ok":
			success++
			ok = append(ok, res.Token)
		case "skipped":
		default:
			failure++
			if res.Error == "DeviceNotRegistered" && res.Token != "" {
				dead = append(dead, res.Token)
			}
		}
	}
	status := "partial"
	switch {
	case allSkipped:
		status = "skipped"
	case failure == 0:
		status = "delivered"
	case success == 0:
		status = "failed"
	}
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := ts(m.now())
		for _, t := range dead {
			if _, err := tx.ExecContext(ctx, `UPDATE notifications_device_tokens SET is_active = 0, updated_at = ? WHERE push_token = ?`, now, t); err != nil {
				return err
			}
		}
		for _, t := range ok {
			if _, err := tx.ExecContext(ctx, `UPDATE notifications_device_tokens SET last_used_at = ? WHERE push_token = ?`, now, t); err != nil {
				return err
			}
		}
		return m.recordOutcome(ctx, p.LogID, status, success, failure, tx)
	})
	if err != nil {
		return nil, err
	}
	m.deps.Log.Info("notifications: push", "log_id", p.LogID, "status", status, "ok", success, "tokens", len(tokens), "attempt", job.Attempt)
	if transient != nil {
		return nil, transient
	}
	return []byte(status), nil
}

func (m *Module) recordOutcome(ctx context.Context, logID, status string, success, failure int, tx *sql.Tx) error {
	return m.inTx(ctx, tx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE notifications_notification_log
			SET delivery_status = ?, success_count = ?, failure_count = ? WHERE id = ?`, status, success, failure, logID)
		return err
	})
}

func (m *Module) stillActive(ctx context.Context, tokens []string) ([]string, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	args := make([]any, len(tokens))
	for i, t := range tokens {
		args[i] = t
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT push_token FROM notifications_device_tokens
		WHERE is_active = 1 AND push_token IN (?`+strings.Repeat(", ?", len(tokens)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	active := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		active[t] = true
	}
	out := []string{}
	for _, t := range tokens { // keep the send's order
		if active[t] {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// --- the relay ---

// relayClient talks to the push relay, which holds the Expo credentials. Protocol (unchanged
// from the legacy service):
//
//	POST {url}/v1/register {"household_id"} → {"jwt"}                       (household JWT)
//	POST {url}/v1/send  Authorization: Bearer <jwt>, X-Household-Id
//	     {"tokens", "title", "body", "data", "priority"} → {"results": [{status, error?, token?}]}
type relayClient struct {
	url       func(context.Context) string // read per push, so a settings change applies at once
	pinnedJWT string
	http      *http.Client

	mu      sync.Mutex
	cache   map[string]string // household id → JWT (process-local, like before)
	cacheOf string            // the relay the cached JWTs came from
}

// base returns the relay in effect, dropping cached JWTs when it changed (they were issued by
// another relay).
func (c *relayClient) base(ctx context.Context) string {
	u := c.url(ctx)
	c.mu.Lock()
	if u != c.cacheOf {
		c.cache, c.cacheOf = map[string]string{}, u
	}
	c.mu.Unlock()
	return u
}

func (c *relayClient) jwt(ctx context.Context, base, household string, refresh bool) (string, error) {
	if !refresh {
		if c.pinnedJWT != "" {
			return c.pinnedJWT, nil
		}
		c.mu.Lock()
		j := c.cache[household]
		c.mu.Unlock()
		if j != "" {
			return j, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"household_id": household})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/register", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("relay register: HTTP %d", resp.StatusCode)
	}
	var out struct {
		JWT string `json:"jwt"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, httpx.MaxBody)).Decode(&out); err != nil || out.JWT == "" {
		return "", fmt.Errorf("relay register: bad response: %v", err)
	}
	c.mu.Lock()
	c.cache[household] = out.JWT
	c.mu.Unlock()
	return out.JWT, nil
}

func (c *relayClient) post(ctx context.Context, base, jwt, household string, payload []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/send", bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Household-Id", household)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, httpx.MaxBody))
	return resp.StatusCode, b, err
}

// deliver sends one push. It always returns one result per token; the error is non-nil when
// the failure is worth retrying.
func (c *relayClient) deliver(ctx context.Context, tokens []string, p pushJob) ([]relayResult, error) {
	all := func(status, code string) []relayResult {
		out := make([]relayResult, len(tokens))
		for i, t := range tokens {
			out[i] = relayResult{Status: status, Error: code, Token: t}
		}
		return out
	}
	base := c.base(ctx)
	if base == "" {
		return all("skipped", ""), nil
	}
	jwt, err := c.jwt(ctx, base, p.HouseholdID, false)
	if err != nil {
		// Legacy reported "skipped" and dropped the push; here it is a retried failure.
		return all("error", "relay_register_failed"), fmt.Errorf("relay: no JWT for household %s: %w", p.HouseholdID, err)
	}
	data := p.Data
	if data == nil {
		data = map[string]any{}
	}
	payload, _ := json.Marshal(map[string]any{"tokens": tokens, "title": p.Title, "body": p.Body, "data": data, "priority": p.Priority})

	status, body, err := c.post(ctx, base, jwt, p.HouseholdID, payload)
	if err == nil && status == http.StatusUnauthorized {
		// The cached or pinned JWT went stale: register again and retry once.
		if jwt, err = c.jwt(ctx, base, p.HouseholdID, true); err != nil {
			return all("error", "relay_http_401"), nil
		}
		status, body, err = c.post(ctx, base, jwt, p.HouseholdID, payload)
	}
	if err != nil {
		return all("error", "relay_unreachable"), fmt.Errorf("relay unreachable: %w", err)
	}
	if status/100 != 2 {
		code := "relay_http_" + strconv.Itoa(status)
		if status >= 500 {
			return all("error", code), fmt.Errorf("relay: HTTP %d", status)
		}
		return all("error", code), nil
	}
	var out struct {
		Results []relayResult `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return all("error", "relay_bad_response"), nil
	}
	for i := range out.Results {
		if out.Results[i].Token == "" && i < len(tokens) {
			out.Results[i].Token = tokens[i]
		}
	}
	return out.Results, nil
}

// --- HTTP ---

// readNotification validates one NotifyRequest.
func readNotification(o *obj) Notification {
	var n Notification
	n.TargetType, _ = o.str("target_type", true)
	n.TargetID, _ = o.str("target_id", true)
	n.Title, _ = o.str("title", true)
	n.Body, _ = o.str("body", true)
	n.Data, _ = o.optDict("data")
	n.Priority = "default"
	if p, ok := o.str("priority", false); ok {
		n.Priority = p
	} else if v, present := o.m["priority"]; present && v == nil {
		o.fail("priority", "string_type", "Input should be a valid string", nil) // not Optional
	}
	n.Category, _ = o.str("category", false)
	return n
}

func (m *Module) handleNotify(w http.ResponseWriter, r *http.Request) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	n := readNotification(o)
	if !o.done(w) {
		return
	}
	d, err := m.Notify(r.Context(), nil, sourceService(r), n)
	switch {
	case errors.Is(err, ErrTargetType), errors.Is(err, ErrPriority):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case err != nil:
		m.internalError(w, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, d)
	}
}

func (m *Module) handleNotifyBatch(w http.ResponseWriter, r *http.Request) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	items, _ := o.list("notifications")
	ns := make([]Notification, 0, len(items))
	for i, raw := range items {
		if c, ok := o.child(raw, "notifications", i); ok {
			ns = append(ns, readNotification(c))
		}
	}
	if !o.done(w) {
		return
	}
	if len(ns) > 100 {
		httpx.Error(w, http.StatusBadRequest, "Maximum 100 notifications per batch")
		return
	}
	// Sent one by one, as before: a bad target_type stops the batch, but the ones before it
	// are already sent. The batch path never validated priority; an unknown one is sent as is.
	source := sourceService(r)
	out := []Delivery{}
	for _, n := range ns {
		if n.TargetType != "user" && n.TargetType != "household" {
			httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("target_type must be 'user' or 'household', got '%s'", n.TargetType))
			return
		}
		var d Delivery
		var queued bool
		err := m.deps.DB.Tx(r.Context(), func(tx *sql.Tx) error {
			var err error
			d, queued, err = m.send(r.Context(), tx, source, n)
			return err
		})
		if err != nil {
			m.internalError(w, err)
			return
		}
		if queued {
			m.deps.Queue.Notify(pushJobType)
		}
		out = append(out, d)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
