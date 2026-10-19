package sdkhost

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// maxAlerts caps the queue, as N:services/alert_queue_service.py's MAX_ALERTS.
const maxAlerts = 50

// AnnouncePriority is the lowest priority the node speaks unasked and lights the LED for
// (N:ALERT_ANNOUNCE_PRIORITY: reminders, urgent mail). Lower ones wait for whats_up or the button.
const AnnouncePriority = 3

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
//
// After every write (Add, Flush, Remove, Sweep) each watcher gets the announceable count
// (alerts at AnnouncePriority or above): a level, not an edge, so the same value repeats and
// the periodic Sweep re-syncs a watcher (N:on_change; Flush always reports 0). Reads don't
// report, so a watcher that reads the queue doesn't call itself again.
type Alerts struct {
	Now func() time.Time // nil: time.Now

	mu       sync.Mutex
	alerts   []Alert
	watchers []func(announceable int)
}

// Watch adds f, called outside the lock after every write.
func (q *Alerts) Watch(f func(announceable int)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.watchers = append(q.watchers, f)
}

// announceable counts the alerts at AnnouncePriority or above. Callers hold mu, after a sweep.
func (q *Alerts) announceable() int {
	n := 0
	for _, a := range q.alerts {
		if a.Priority >= AnnouncePriority {
			n++
		}
	}
	return n
}

// unlock releases mu, then reports n to the watchers.
func (q *Alerts) unlock(n int) {
	ws := q.watchers
	q.mu.Unlock()
	for _, w := range ws {
		w(n)
	}
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
	q.sweep()
	defer func() { q.unlock(q.announceable()) }()
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
	q.sweep()
	out := byUrgency(q.alerts)
	q.alerts = nil
	q.unlock(0)
	return out
}

// Remove drops the alerts with these ids and returns how many it dropped.
func (q *Alerts) Remove(ids []string) int {
	q.mu.Lock()
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
	q.unlock(q.announceable())
	return n
}

// Sweep drops the expired alerts and reports the count to the watchers (N:sweep_expired, run
// on every scheduler tick so a watcher re-syncs).
func (q *Alerts) Sweep() {
	q.mu.Lock()
	q.sweep()
	q.unlock(q.announceable())
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
