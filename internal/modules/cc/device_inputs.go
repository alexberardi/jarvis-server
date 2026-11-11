package cc

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type deviceInputKey struct{ household, device, session string }
type deviceInput struct {
	node, action string
	user         int64
	expires      time.Time
}

// Protocol session IDs remain opaque. Bind them server-side and consume each answer once;
// the sidecar may return another prompt (including the same ID) for the next step.
type deviceInputStore struct {
	mu    sync.Mutex
	items map[deviceInputKey]deviceInput
}

func (s *deviceInputStore) remember(now time.Time, d *deviceRow, node string, user int64, prompt map[string]any) {
	id, action := getStr(prompt, "session_id", ""), getStr(prompt, "follow_up_action", "")
	if id == "" || action == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[deviceInputKey]deviceInput)
	}
	for k, v := range s.items {
		if !now.Before(v.expires) || (k.household == d.householdID && k.device == d.id && v.user == user) {
			delete(s.items, k)
		}
	}
	if len(s.items) >= 1024 {
		return
	} // a full store fails closed; restart pairing
	s.items[deviceInputKey{d.householdID, d.id, id}] = deviceInput{node, action, user, now.Add(5 * time.Minute)}
}

func (s *deviceInputStore) take(now time.Time, d *deviceRow, user int64, action, session string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := deviceInputKey{d.householdID, d.id, session}
	v, ok := s.items[k]
	if !ok || !now.Before(v.expires) || v.user != user || v.action != action {
		return "", false
	}
	delete(s.items, k)
	return v.node, true
}

// Pin when the node accepts pairing. Failed starts leave older devices unbound. Concurrent
// starts use the first binding; the caller refuses a prompt from a different node.
func (m *Module) pinDeviceNode(ctx context.Context, hh string, d *deviceRow, n *nodeRow) (*nodeRow, error) {
	if d.nodeID.String != "" || d.sourceOr() != "direct" {
		return n, nil
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_devices SET node_id = ? WHERE id = ? AND household_id = ? AND node_id IS NULL`, n.nodeID, d.id, hh); err != nil {
		return nil, err
	}
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT node_id FROM cc_devices WHERE id = ? AND household_id = ?`, d.id, hh).Scan(&d.nodeID); err != nil {
		return nil, err
	}
	return m.boundDeviceNode(ctx, hh, d.nodeID.String)
}

func (m *Module) deviceInputNode(ctx context.Context, hh string, d *deviceRow, user int64, action string, data map[string]any) (*nodeRow, error) {
	if raw, present := data["session_id"]; present {
		id, _ := raw.(string)
		node, ok := m.deviceInputs.take(m.now(), d, user, action, id)
		if !ok {
			return nil, fail(http.StatusConflict, "Pairing session expired or invalid; start pairing again")
		}
		return m.boundDeviceNode(ctx, hh, node)
	}
	if action == "pair_finish" {
		return nil, fail(http.StatusConflict, "Start pairing before submitting a code")
	}
	n, err := m.pickDeviceNode(ctx, hh, d)
	if err != nil {
		return nil, err
	}
	return n, nil
}
