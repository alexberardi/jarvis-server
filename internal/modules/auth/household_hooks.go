package auth

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// Household lifecycle hooks (D20, D49). Like UserDeletedHook they run inside auth's write
// transaction, so a failing hook rolls the membership change back.

// MemberRemovedHook erases one module's data for a user in a household they left or were
// removed from. Their data in their other households stays.
type MemberRemovedHook func(ctx context.Context, tx *sql.Tx, userID int64, householdID string) error

// HouseholdDeletedHook erases everything one module holds for a deleted household (its last
// member left, the last member's account was deleted, or an admin deleted it).
type HouseholdDeletedHook func(ctx context.Context, tx *sql.Tx, householdID string) error

// OnMemberRemoved registers a hook run when a membership ends. Call it before serving.
func (m *Module) OnMemberRemoved(h MemberRemovedHook) {
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	m.memberHooks = append(m.memberHooks, h)
}

// OnHouseholdDeleted registers a hook run when a household is deleted. Call it before serving.
func (m *Module) OnHouseholdDeleted(h HouseholdDeletedHook) {
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	m.householdHooks = append(m.householdHooks, h)
}

func (m *Module) memberRemoved(ctx context.Context, tx *sql.Tx, userID int64, hh string) error {
	m.hookMu.Lock()
	hooks := slices.Clone(m.memberHooks)
	m.hookMu.Unlock()
	for _, h := range hooks {
		if err := h(ctx, tx, userID, hh); err != nil {
			return fmt.Errorf("member-removed hook: %w", err)
		}
	}
	return nil
}

func (m *Module) householdDeleted(ctx context.Context, tx *sql.Tx, hh string) error {
	m.hookMu.Lock()
	hooks := slices.Clone(m.householdHooks)
	m.hookMu.Unlock()
	for _, h := range hooks {
		if err := h(ctx, tx, hh); err != nil {
			return fmt.Errorf("household-deleted hook: %w", err)
		}
	}
	return nil
}
