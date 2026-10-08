package recipes

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Scoping (docs/recipes/00-inventory.md §4.1, decision RD7). This file is the one place that
// decides who sees a row; handlers never write their own user_id/household_id filters (a test
// greps for them).
//
//   - visible: the row's household is one the caller belongs to, any of them (RD7: the union,
//     whichever household the token names), or it has no household and the caller wrote it
//     (pre-household rows stay their author's).
//   - mutable = visible: members edit and delete each other's rows in every household they
//     belong to (the self-hosted stance: never restrict members). Non-members get 404.
//   - author-only reads (parse jobs, stage recipes, GET /recipes/user/{id}) use authorOnly.
//   - new rows are stamped with the caller as author and writeHousehold as household.

// caller is the authenticated user with their memberships resolved.
type caller struct {
	ID         int64
	Email      string
	households []string // every household the user belongs to now
	// writeHousehold is stamped on new rows: the token's household while the user is still a
	// member of it, else their first membership (what a fresh token would name), else none.
	writeHousehold string
}

// uid is the user id as the tables store it (legacy: a string).
func (c caller) uid() string { return strconv.FormatInt(c.ID, 10) }

// hh is writeHousehold as a nullable SQL argument.
func (c caller) hh() any {
	if c.writeHousehold == "" {
		return nil
	}
	return c.writeHousehold
}

// visible returns the SQL predicate (and its arguments) for rows of the table aliased col the
// caller may read and change. col is a table alias ("r") or "" for unqualified columns.
func (c caller) visible(col string) (string, []any) {
	p := ""
	if col != "" {
		p = col + "."
	}
	own := "(" + p + "household_id IS NULL AND " + p + "user_id = ?)"
	if len(c.households) == 0 {
		return own, []any{c.uid()}
	}
	args := make([]any, 0, len(c.households)+1)
	for _, h := range c.households {
		args = append(args, h)
	}
	args = append(args, c.uid())
	return "(" + p + "household_id IN (?" + strings.Repeat(",?", len(c.households)-1) + ") OR " + own + ")", args
}

// authorOnly is the predicate for rows only their author may see.
func (c caller) authorOnly(col string) (string, []any) {
	p := ""
	if col != "" {
		p = col + "."
	}
	return p + "user_id = ?", []any{c.uid()}
}

// resolve builds the caller from a verified token.
func (m *Module) resolve(ctx context.Context, u authn.User) (caller, error) {
	c := caller{ID: u.ID, Email: u.Email}
	if m.Households != nil {
		hhs, err := m.Households.UserHouseholds(ctx, u.ID)
		if err != nil {
			return caller{}, err
		}
		c.households = hhs
	} else if u.HouseholdID != "" {
		c.households = []string{u.HouseholdID} // no membership source (tests): trust the claim
	}
	switch {
	case u.HouseholdID != "" && slices.Contains(c.households, u.HouseholdID):
		c.writeHousehold = u.HouseholdID
	case len(c.households) > 0:
		c.writeHousehold = c.households[0]
	}
	return c, nil
}

// user authenticates "Authorization: Bearer <jwt>" like FastAPI's HTTPBearer + the legacy
// get_current_user: no or non-bearer credentials are 401 "Not authenticated" (with
// WWW-Authenticate), a bad token is 401 "Invalid or expired token". It runs before any
// request validation, as legacy's dependency did.
func (m *Module) user(h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheme, tok, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		tok = strings.TrimSpace(tok)
		if !strings.EqualFold(scheme, "Bearer") || tok == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpx.Error(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		u, err := m.Users.VerifyUser(r.Context(), tok)
		if err != nil {
			if errors.Is(err, authn.ErrInvalid) || errors.Is(err, authn.ErrExpired) || errors.Is(err, authn.ErrNoSub) {
				httpx.Error(w, http.StatusUnauthorized, "Invalid or expired token")
				return
			}
			m.internalError(w, err)
			return
		}
		c, err := m.resolve(r.Context(), u)
		if err != nil {
			m.internalError(w, err)
			return
		}
		h(w, r, c)
	}
}

func (m *Module) internalError(w http.ResponseWriter, err error) {
	m.deps.Log.Error("recipes: internal error", "err", err)
	httpx.Error(w, http.StatusInternalServerError, "Internal Server Error")
}
