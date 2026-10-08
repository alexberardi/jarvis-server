package auth

import (
	"context"
	"database/sql"
)

// Directory is every account's email and every household with its members: what the recipes
// cutover import (`jarvisd import-recipes`) maps legacy users and households onto. It is read
// from the database directly, so the CLI needs no running jarvisd.
type Directory struct {
	Emails      map[int64]string  // user id → email
	Households  map[string]string // household id → name
	Memberships []DirectoryMember // oldest first
}

// DirectoryMember is one household membership.
type DirectoryMember struct {
	HouseholdID string
	UserID      int64
	Role        string
}

// ReadDirectory reads the Directory from the auth tables.
func ReadDirectory(ctx context.Context, q *sql.DB) (Directory, error) {
	d := Directory{Emails: map[int64]string{}, Households: map[string]string{}}
	rows, err := q.QueryContext(ctx, `SELECT id, email FROM auth_users WHERE is_active = 1`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var id int64
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			rows.Close()
			return d, err
		}
		d.Emails[id] = email
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}
	rows, err = q.QueryContext(ctx, `SELECT id, name FROM auth_households`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return d, err
		}
		d.Households[id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}
	rows, err = q.QueryContext(ctx, `SELECT m.household_id, m.user_id, m.role FROM auth_household_memberships m
		JOIN auth_users u ON u.id = m.user_id WHERE u.is_active = 1 ORDER BY m.id`)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var m DirectoryMember
		if err := rows.Scan(&m.HouseholdID, &m.UserID, &m.Role); err != nil {
			return d, err
		}
		d.Memberships = append(d.Memberships, m)
	}
	return d, rows.Err()
}
