package auth

import (
	"context"
	"strings"
)

// UserNames is GET /internal/users/batch in process (command-center's speaker and household
// member name resolution, docs/cc/06 §3.7): id → username for the ids that exist. Unknown ids
// are simply absent.
func (m *Module) UserNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT id, username FROM auth_users WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
