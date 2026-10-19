package sdkhost

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// maxAlerts caps the queue, as N:services/alert_queue_service.py's MAX_ALERTS.
const maxAlerts = 50

// Alert is the SDK's Alert (jarvis_command_sdk/agent.py), as the queue keeps it.
type Alert struct {
	ID           string    `json:"id"`
	SourceAgent  string    `json:"source_agent"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary"`
	Priority     int       `json:"priority"` // 1 low, 2 medium, 3 high
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	DedupeKey    *string   `json:"dedupe_key"`
	Category     string    `json:"category"`
	TargetUserID *string   `json:"target_user_id"`
}

// Alerts is the node's in-memory alert queue (N:alert_queue_service): what background agents
// produced, read by whats_up through the SDK's AlertQueueBackend. Expired alerts are dropped
// on every read and write.
type Alerts struct {
	Now func() time.Time // nil: time.Now

	mu     sync.Mutex
	alerts []Alert
}

func (q *Alerts) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// sweep drops the expired alerts. Callers hold mu.
func (q *Alerts) sweep() {
	now := q.now()
	kept := q.alerts[:0]
	for _, a := range q.alerts {
		if a.ExpiresAt.After(now) {
			kept = append(kept, a)
		}
	}
	q.alerts = kept
}

// Add queues a; one already expired is refused. An alert with the title of a queued one (any
// case) replaces it only with a higher priority. Over the cap, the lowest priority, then the
// oldest, goes (the intent N:add_alert states; its sort key drops the newest instead).
func (q *Alerts) Add(a Alert) {
	if !a.ExpiresAt.After(q.now()) {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	title := strings.ToLower(strings.TrimSpace(a.Title))
	for i, e := range q.alerts {
		if strings.ToLower(strings.TrimSpace(e.Title)) == title {
			if a.Priority > e.Priority {
				q.alerts[i] = a
			}
			return
		}
	}
	q.alerts = append(q.alerts, a)
	if len(q.alerts) > maxAlerts {
		sort.SliceStable(q.alerts, func(i, j int) bool {
			x, y := q.alerts[i], q.alerts[j]
			if x.Priority != y.Priority {
				return x.Priority < y.Priority
			}
			return x.CreatedAt.Before(y.CreatedAt)
		})
		q.alerts = q.alerts[len(q.alerts)-maxAlerts:]
	}
}

// byUrgency is the queue sorted by priority (highest first), then age (oldest first).
func byUrgency(in []Alert) []Alert {
	out := append([]Alert(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Pending are the queued alerts, most urgent first, left in the queue.
func (q *Alerts) Pending() []Alert {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	return byUrgency(q.alerts)
}

// Flush returns the queued alerts, most urgent first, and empties the queue.
func (q *Alerts) Flush() []Alert {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	out := byUrgency(q.alerts)
	q.alerts = nil
	return out
}

// Remove drops the alerts with these ids and returns how many it dropped.
func (q *Alerts) Remove(ids []string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	kept := q.alerts[:0]
	for _, a := range q.alerts {
		if !drop[a.ID] {
			kept = append(kept, a)
		}
	}
	n := len(q.alerts) - len(kept)
	q.alerts = kept
	q.sweep()
	return n
}

// Count is how many alerts are queued.
func (q *Alerts) Count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	return len(q.alerts)
}

// wire is a as the child rebuilds it: Python isoformat times.
func (a Alert) wire() map[string]any {
	return map[string]any{"id": a.ID, "source_agent": a.SourceAgent, "title": a.Title, "summary": a.Summary,
		"priority": a.Priority, "created_at": isoformat(a.CreatedAt), "expires_at": isoformat(a.ExpiresAt),
		"dedupe_key": a.DedupeKey, "category": a.Category, "target_user_id": a.TargetUserID}
}

// isoformat is Python's datetime.isoformat() of t in UTC.
func isoformat(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 != 0 {
		return t.Format("2006-01-02T15:04:05.000000+00:00")
	}
	return t.Format("2006-01-02T15:04:05+00:00")
}
