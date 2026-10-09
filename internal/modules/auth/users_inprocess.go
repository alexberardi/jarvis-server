package auth

import (
	"context"
	"strings"
)

// SetupCounts is the admin setup state's view of the accounts: whether a superuser exists (no
// superuser means first-run setup is open, behind the setup token, AD2), and how many
// households and active nodes there are.
type SetupCounts struct {
	Superusers int `json:"superusers"`
	Households int `json:"households"`
	Nodes      int `json:"nodes"`
}

// SetupCounts counts superusers, households and active nodes.
func (m *Module) SetupCounts(ctx context.Context) (SetupCounts, error) {
	var c SetupCounts
	for _, q := range []struct {
		dst   *int
		query string
	}{
		{&c.Superusers, `SELECT COUNT(*) FROM auth_users WHERE is_superuser = 1`},
		{&c.Households, `SELECT COUNT(*) FROM auth_households`},
		{&c.Nodes, `SELECT COUNT(*) FROM auth_node_registrations WHERE is_active = 1`},
	} {
		n, err := count(ctx, m.deps.DB.Read, q.query)
		if err != nil {
			return SetupCounts{}, err
		}
		*q.dst = n
	}
	return c, nil
}

// UserHouseholds lists the households userID belongs to, oldest membership first (the first
// is the one a fresh token names). Recipes reads the union of them (docs/recipes RD7).
func (m *Module) UserHouseholds(ctx context.Context, userID int64) ([]string, error) {
	return userHouseholds(ctx, m.deps.DB.Read, userID)
}

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

// HouseholdNames maps every household's id to its name (the admin's per-household setting
// values name the household).
func (m *Module) HouseholdNames(ctx context.Context) (map[string]string, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id, name FROM auth_households`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
