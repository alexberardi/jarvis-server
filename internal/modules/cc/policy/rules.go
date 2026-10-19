package policy

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// PutRule upserts one of the household's Custom Rules (docs/autonomy/PLAN.md §1, A1, A7): one
// row per (household, scope, tool, target); the effect, note and who changed it are updated, the
// id is returned. The target is "" (every target of the tool), site:<host>, app:<id> or
// action:<category>; a site's host is lowercased. UpdatedAt is filled from Now (default time.Now).
func (s *Store) PutRule(ctx context.Context, r Rule) (Rule, error) {
	r.Tool = strings.TrimSpace(r.Tool)
	if r.Tool == "" {
		return Rule{}, errors.New("policy: invalid rule: tool")
	}
	var err error
	if r.Target, err = normalizeTarget(r.Target); err != nil {
		return Rule{}, err
	}
	switch r.Effect {
	case Allow, Ask, Block:
	default:
		return Rule{}, errors.New("policy: invalid rule: effect")
	}
	switch r.ScopeKind {
	case ScopeHousehold:
		if r.ScopeID != "" {
			return Rule{}, errors.New("policy: invalid rule: scope_id")
		}
	case ScopeGoal, ScopeWatch:
		if r.ScopeID == "" {
			return Rule{}, errors.New("policy: invalid rule: scope_id")
		}
	default:
		return Rule{}, errors.New("policy: invalid rule: scope_kind")
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	r.UpdatedAt = now()
	var id int64
	err = s.DB.Write.QueryRowContext(ctx, `INSERT INTO cc_tool_rules
		(household_id, scope_kind, scope_id, tool, target, effect, note, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (household_id, scope_kind, scope_id, tool, target)
		DO UPDATE SET effect = excluded.effect, note = excluded.note,
			updated_by = excluded.updated_by, updated_at = excluded.updated_at
		RETURNING id`,
		r.HouseholdID, r.ScopeKind, r.ScopeID, r.Tool, r.Target, string(r.Effect), r.Note, r.UpdatedBy,
		r.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z")).Scan(&id)
	if err != nil {
		return Rule{}, err
	}
	r.ID = id
	return r, nil
}

// Rule returns the household's rule for (scope, tool, target), or nil, nil when there is none.
func (s *Store) Rule(ctx context.Context, hh, scopeKind, scopeID, tool, target string) (*Rule, error) {
	var (
		r  Rule
		ub sql.NullInt64
		ua string
	)
	err := s.DB.Read.QueryRowContext(ctx, `SELECT id, household_id, scope_kind, scope_id, tool, target, effect, note, updated_by, updated_at
		FROM cc_tool_rules WHERE household_id = ? AND scope_kind = ? AND scope_id = ? AND tool = ? AND target = ?`,
		hh, scopeKind, scopeID, tool, target).Scan(&r.ID, &r.HouseholdID, &r.ScopeKind, &r.ScopeID, &r.Tool, &r.Target, &r.Effect,
		&r.Note, &ub, &ua)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.UpdatedBy = nullInt64(ub)
	if r.UpdatedAt, err = time.Parse("2006-01-02T15:04:05.000Z", ua); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRules returns the household's rules ordered by scope_kind, scope_id, tool, target.
func (s *Store) ListRules(ctx context.Context, hh string) ([]Rule, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT id, household_id, scope_kind, scope_id, tool, target, effect, note, updated_by, updated_at
		FROM cc_tool_rules WHERE household_id = ? ORDER BY scope_kind, scope_id, tool, target`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var (
			r  Rule
			ub sql.NullInt64
			ua string
		)
		if err := rows.Scan(&r.ID, &r.HouseholdID, &r.ScopeKind, &r.ScopeID, &r.Tool, &r.Target, &r.Effect, &r.Note, &ub, &ua); err != nil {
			return nil, err
		}
		r.UpdatedBy = nullInt64(ub)
		if r.UpdatedAt, err = time.Parse("2006-01-02T15:04:05.000Z", ua); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRule removes the rule with the given id from the household and reports whether it
// existed.
func (s *Store) DeleteRule(ctx context.Context, hh string, id int64) (bool, error) {
	res, err := s.DB.Write.ExecContext(ctx, `DELETE FROM cc_tool_rules WHERE household_id = ? AND id = ?`, hh, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
