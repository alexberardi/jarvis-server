package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Rooms (smart_home.py:523-638): hierarchical, normalized_name unique per household.

const roomCols = `r.id, r.household_id, r.name, r.normalized_name, r.icon, r.ha_area_id, r.parent_room_id,
	r.created_at, r.updated_at,
	(SELECT COUNT(*) FROM cc_devices d WHERE d.room_id = r.id AND d.is_active = 1),
	(SELECT COUNT(*) FROM cc_nodes n WHERE n.room_id = r.id)`

// listRooms returns RoomResponse rows, with the counts in the same query (legacy ran N+1).
func (m *Module) listRooms(ctx context.Context, where string, args ...any) ([]map[string]any, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+roomCols+` FROM cc_rooms r WHERE `+where+` ORDER BY r.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, hh, name, norm string
		var icon, area, parent, created, updated sql.NullString
		var devices, nodes int64
		if err := rows.Scan(&id, &hh, &name, &norm, &icon, &area, &parent, &created, &updated, &devices, &nodes); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "household_id": hh, "name": name, "normalized_name": norm, "icon": nullable(icon),
			"ha_area_id": nullable(area), "parent_room_id": nullable(parent), "device_count": devices, "node_count": nodes,
			"created_at": naiveTS(created.String), "updated_at": naiveTS(updated.String),
		})
	}
	return out, rows.Err()
}

func (m *Module) room(ctx context.Context, hh, id string) (map[string]any, error) {
	rooms, err := m.listRooms(ctx, `r.household_id = ? AND r.id = ?`, hh, id)
	if err != nil {
		return nil, err
	}
	if len(rooms) == 0 {
		return nil, fail(http.StatusNotFound, "Room not found")
	}
	return rooms[0], nil
}

// handleListRooms takes provisioning auth, or (Q7) node auth scoped to the node's own
// household: the HA package fetches rooms with its node key, which legacy always 401'd.
func (m *Module) handleListRooms(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if key := r.Header.Get("X-API-Key"); key != "" && !authn.Equal(key, m.AdminKey) && strings.Contains(key, ":") {
		n, ok := m.authNode(w, r)
		if !ok {
			return
		}
		if n.HouseholdID == "" || n.HouseholdID != hh {
			detail(w, http.StatusForbidden, "Not authorized")
			return
		}
	} else {
		a, ok := m.authProvisioning(w, r)
		if !ok {
			return
		}
		if err := m.householdAccess(ctx, a, hh); err != nil {
			m.writeErr(w, err)
			return
		}
	}
	rooms, err := m.listRooms(ctx, `r.household_id = ?`, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rooms)
}

// roomInHousehold reports whether id is one of hh's rooms.
func (m *Module) roomInHousehold(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, hh, id string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_rooms WHERE id = ? AND household_id = ?`, id, hh).Scan(&n)
	return n > 0, err
}

func roomNameTaken(ctx context.Context, tx *sql.Tx, hh, normalized, except string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_rooms WHERE household_id = ? AND normalized_name = ? AND id != ?`,
		hh, normalized, except).Scan(&n)
	return n > 0, err
}

func (m *Module) handleCreateRoom(w http.ResponseWriter, r *http.Request, a provAuth) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, _ := b.str("name", true)
	icon, _ := b.optStrPtr("icon")
	area, _ := b.optStrPtr("ha_area_id")
	parent, _ := b.optStrPtr("parent_room_id")
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	id, now := uuid4(), dbTime(m.now())
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if taken, err := roomNameTaken(ctx, tx, hh, normalized, ""); err != nil {
			return err
		} else if taken {
			return fail(http.StatusConflict, fmt.Sprintf("Room '%s' already exists", name))
		}
		if parent != nil && *parent != "" {
			if ok, err := m.roomInHousehold(ctx, tx, hh, *parent); err != nil {
				return err
			} else if !ok {
				return fail(http.StatusBadRequest, "Parent room not found in this household")
			}
		} else {
			parent = nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO cc_rooms (id, household_id, name, normalized_name, icon, ha_area_id,
			parent_room_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, hh, strings.TrimSpace(name), normalized, nullStr(icon), nullStr(area), nullStr(parent), now, now)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.deps.Log.Info("cc: room created", "room", strings.TrimSpace(name), "household", hh)
	room, err := m.room(ctx, hh, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, room)
}

func (m *Module) handleUpdateRoom(w http.ResponseWriter, r *http.Request, a provAuth) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, hasName := b.optStrPtr("name")
	icon, _ := b.optStrPtr("icon")
	// parent_room_id is exclude_unset: absent leaves it, an explicit null clears it.
	parent, hasParent := b.optStrPtr("parent_room_id")
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh, id := r.PathValue("household_id"), r.PathValue("room_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if ok, err := m.roomInHousehold(ctx, tx, hh, id); err != nil {
			return err
		} else if !ok {
			return fail(http.StatusNotFound, "Room not found")
		}
		sets, args := []string{"updated_at = ?"}, []any{dbTime(m.now())}
		if hasName && name != nil {
			normalized := strings.ToLower(strings.TrimSpace(*name))
			if taken, err := roomNameTaken(ctx, tx, hh, normalized, id); err != nil {
				return err
			} else if taken {
				return fail(http.StatusConflict, fmt.Sprintf("Room '%s' already exists", *name))
			}
			sets, args = append(sets, "name = ?", "normalized_name = ?"), append(args, strings.TrimSpace(*name), normalized)
		}
		if icon != nil {
			sets, args = append(sets, "icon = ?"), append(args, *icon)
		}
		if hasParent {
			if parent != nil {
				if ok, err := m.roomInHousehold(ctx, tx, hh, *parent); err != nil {
					return err
				} else if !ok {
					return fail(http.StatusBadRequest, "Parent room not found in this household")
				}
				if cycle, err := wouldCreateCycle(ctx, tx, hh, id, *parent); err != nil {
					return err
				} else if cycle {
					return fail(http.StatusBadRequest, "Cannot set parent: would create a cycle")
				}
			}
			sets, args = append(sets, "parent_room_id = ?"), append(args, nullStr(parent))
		}
		_, err := tx.ExecContext(ctx, `UPDATE cc_rooms SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	room, err := m.room(ctx, hh, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, room)
}

// wouldCreateCycle is _would_create_cycle: walk up from the proposed parent; reaching the
// room itself is a cycle. The walk is bounded so a corrupt (already cyclic) tree terminates.
func wouldCreateCycle(ctx context.Context, tx *sql.Tx, hh, roomID, parentID string) (bool, error) {
	current := sql.NullString{String: parentID, Valid: true}
	for seen := map[string]bool{}; current.Valid; {
		if current.String == roomID || seen[current.String] {
			return true, nil
		}
		seen[current.String] = true
		err := tx.QueryRowContext(ctx, `SELECT parent_room_id FROM cc_rooms WHERE id = ? AND household_id = ?`,
			current.String, hh).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

// descendantRooms is get_descendant_room_ids: the room and every descendant (BFS).
func descendantRooms(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, hh, roomID string) ([]string, error) {
	out, queue := []string{roomID}, []string{roomID}
	seen := map[string]bool{roomID: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		rows, err := q.QueryContext(ctx, `SELECT id FROM cc_rooms WHERE parent_room_id = ? AND household_id = ? ORDER BY rowid`, parent, hh)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
				queue = append(queue, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// handleDeleteRoom hard-deletes the room. Legacy's Room.children relationship is
// cascade="all", so descendant rooms are deleted with it; devices and nodes in any of them
// keep existing with room_id NULL (FK ON DELETE SET NULL).
func (m *Module) handleDeleteRoom(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	hh, id := r.PathValue("household_id"), r.PathValue("room_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if ok, err := m.roomInHousehold(ctx, tx, hh, id); err != nil {
			return err
		} else if !ok {
			return fail(http.StatusNotFound, "Room not found")
		}
		ids, err := descendantRooms(ctx, tx, hh, id)
		if err != nil {
			return err
		}
		for i := len(ids) - 1; i >= 0; i-- {
			if _, err := tx.ExecContext(ctx, `DELETE FROM cc_rooms WHERE id = ?`, ids[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.deps.Log.Info("cc: room deleted", "room", id, "household", hh)
	w.WriteHeader(http.StatusNoContent)
}
