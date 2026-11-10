package packages

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// EventSource reads a test package's events from the sidecar in service: the sidecar's
// test_events device method ({"package", "after"} -> {"events", "last"}).
type EventSource func(method string, params map[string]any) (map[string]any, error)

// ShipTestEvents sends what each Forge test install did (its log lines, scans and device
// controls, recorded by the sidecar) to jarvisd every `every` while the test is served, so Send to
// Forge can report them. It returns when ctx ends. Events that don't arrive are sent again next
// time: the cursor moves only on success.
func (q *Requests) ShipTestEvents(ctx context.Context, api API, events EventSource, every time.Duration) {
	sent := map[string]int{} // request id -> last event sent
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		q.shipTestEventsOnce(ctx, api, events, sent)
	}
}

func (q *Requests) shipTestEventsOnce(ctx context.Context, api API, events EventSource, sent map[string]int) {
	live := map[string]bool{}
	for _, tp := range q.Manager.tests() {
		if tp.rid == "" {
			continue
		}
		live[tp.rid] = true
		res, err := events("test_events", map[string]any{"package": testPrefix + tp.name, "after": sent[tp.rid]})
		if err != nil {
			continue // no sidecar in service right now
		}
		if head, _ := res["head"].(float64); int(head) < sent[tp.rid] {
			// A new sidecar (another package changed) counts from 0 again: read it from the start.
			sent[tp.rid] = 0
			if res, err = events("test_events", map[string]any{"package": testPrefix + tp.name, "after": 0}); err != nil {
				continue
			}
		}
		list, _ := res["events"].([]any)
		last, _ := res["last"].(float64)
		if len(list) == 0 {
			continue
		}
		path := "/api/v0/nodes/" + url.PathEscape(q.NodeID) + "/test-install/" + url.PathEscape(tp.rid) + "/events"
		if err := api.Do(ctx, http.MethodPost, path, map[string]any{"events": list}, nil); err != nil {
			q.Log.Warn("packages: test install events not delivered", "request_id", tp.rid, "err", err)
			continue
		}
		sent[tp.rid] = int(last)
	}
	for rid := range sent {
		if !live[rid] {
			delete(sent, rid)
		}
	}
}
