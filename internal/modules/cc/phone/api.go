package phone

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Mobile routes: the household phonebook (api/mobile_phone_contacts.py) and the per-user
// call context (api/mobile_call_context.py). Contacts are household facts any member may read
// and write; a foreign id is 404, never 403 (existence doesn't leak). Call context is scoped
// to the JWT's user only.

func detail(w http.ResponseWriter, status int, d any) { httpx.Error(w, status, d) }

// validationError is CC's RequestValidationError handler: 400 with flattened details.
func validationError(w http.ResponseWriter, details ...string) {
	httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error":   "validation_error",
		"message": "Request validation failed. Please correct the highlighted fields.",
		"details": details,
	})
}

// requireMember is verify_household_role(required_role="member").
func (s *Service) requireMember(w http.ResponseWriter, r *http.Request, u authn.User, hh string) bool {
	if s.Roles == nil {
		detail(w, http.StatusServiceUnavailable, "Household role verification unavailable")
		return false
	}
	_, member, err := s.Roles.HouseholdRole(r.Context(), u.ID, hh)
	if err != nil {
		s.log().Error("phone: role check failed", "err", err)
		detail(w, http.StatusBadGateway, "Auth service unavailable")
		return false
	}
	if !member {
		detail(w, http.StatusForbidden, "User is not a member of this household")
		return false
	}
	return true
}

type contactJSON struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Number     string  `json:"number"`
	Address    *string `json:"address"`
	Source     string  `json:"source"`
	LineType   *string `json:"line_type"`
	DoNotCall  bool    `json:"do_not_call"`
	Notes      *string `json:"notes"`
	VerifiedAt *string `json:"verified_at"`
	CreatedAt  string  `json:"created_at"`
}

func contactOut(c *Contact) contactJSON {
	out := contactJSON{ID: c.ID, Name: c.Name, Number: c.Number, Address: c.Address, Source: c.Source,
		LineType: c.LineType, DoNotCall: c.DoNotCall, Notes: c.Notes, CreatedAt: pyNaive(c.CreatedAt)}
	if !c.VerifiedAt.IsZero() {
		v := pyNaive(c.VerifiedAt)
		out.VerifiedAt = &v
	}
	return out
}

func (s *Service) handleListContacts(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh := r.PathValue("household_id")
	if !s.requireMember(w, r, u, hh) {
		return
	}
	cs, err := s.contacts(r.Context(), hh)
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]contactJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, contactOut(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"contacts": out})
}

// contactBody validates a create/update body with pydantic's messages.
type contactBody struct {
	raw  map[string]any
	errs []string
}

func readContactBody(w http.ResponseWriter, r *http.Request) (*contactBody, bool) {
	var raw any
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&raw); err != nil {
		validationError(w, "body: Input should be a valid dictionary or object to extract fields from")
		return nil, false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		validationError(w, "body: Input should be a valid dictionary or object to extract fields from")
		return nil, false
	}
	return &contactBody{raw: m}, true
}

// str reads an optional or required string with length bounds (Field(min_length, max_length)).
func (b *contactBody) str(name string, required bool, minLen, maxLen int) (*string, bool) {
	v, present := b.raw[name]
	if !present || v == nil {
		// An explicit null for an optional field (str | None) is "unset".
		if required {
			b.errs = append(b.errs, "body -> "+name+": Field required")
			return nil, false
		}
		return nil, true
	}
	str, ok := v.(string)
	if !ok {
		b.errs = append(b.errs, "body -> "+name+": Input should be a valid string")
		return nil, false
	}
	n := utf8.RuneCountInString(str)
	if minLen > 0 && n < minLen {
		b.errs = append(b.errs, fmt.Sprintf("body -> %s: String should have at least %d character", name, minLen))
		return nil, false
	}
	if maxLen > 0 && n > maxLen {
		b.errs = append(b.errs, fmt.Sprintf("body -> %s: String should have at most %d characters", name, maxLen))
		return nil, false
	}
	return &str, true
}

func (b *contactBody) boolean(name string) (*bool, bool) {
	v, present := b.raw[name]
	if !present || v == nil {
		return nil, true
	}
	switch x := v.(type) {
	case bool:
		return &x, true
	case string:
		switch strings.ToLower(x) {
		case "true", "1", "yes", "on", "t", "y":
			t := true
			return &t, true
		case "false", "0", "no", "off", "f", "n":
			f := false
			return &f, true
		}
	case float64:
		if x == 0 || x == 1 {
			t := x == 1
			return &t, true
		}
	}
	b.errs = append(b.errs, "body -> "+name+": Input should be a valid boolean")
	return nil, false
}

func (b *contactBody) done(w http.ResponseWriter) bool {
	if len(b.errs) > 0 {
		validationError(w, b.errs...)
		return false
	}
	return true
}

func (s *Service) nameTaken(ctx context.Context, hh, norm, exclude string) (bool, error) {
	var n int
	err := s.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_phone_contacts
		WHERE household_id = ? AND normalized_name = ? AND id != ?`, hh, norm, exclude).Scan(&n)
	return n > 0, err
}

func (s *Service) handleCreateContact(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh := r.PathValue("household_id")
	b, ok := readContactBody(w, r)
	if !ok {
		return
	}
	name, _ := b.str("name", true, 1, 255)
	number, _ := b.str("number", true, 1, 32)
	address, _ := b.str("address", false, 0, 0)
	notes, _ := b.str("notes", false, 0, 0)
	if !b.done(w) {
		return
	}
	if !s.requireMember(w, r, u, hh) {
		return
	}
	num, err := NormalizeUS(*number)
	if err != nil {
		detail(w, http.StatusBadRequest, err.Error())
		return
	}
	nm := strings.TrimSpace(*name)
	norm := NormalizeName(nm)
	if norm == "" {
		detail(w, http.StatusBadRequest, "Name must contain letters or digits")
		return
	}
	ctx := r.Context()
	if taken, err := s.nameTaken(ctx, hh, norm, ""); err != nil {
		s.internalError(w, err)
		return
	} else if taken {
		detail(w, http.StatusConflict, fmt.Sprintf("'%s' is already in this household's phonebook", nm))
		return
	}
	id, now := uuid4(), dbTime(s.now())
	if _, err := s.DB.Write.ExecContext(ctx, `INSERT INTO cc_phone_contacts
		(id, household_id, name, normalized_name, number, address, source, do_not_call, notes, verified_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'manual', 0, ?, ?, ?, ?)`,
		id, hh, nm, norm, num, ptrArg(address), ptrArg(notes), now, now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			detail(w, http.StatusConflict, fmt.Sprintf("'%s' is already in this household's phonebook", nm))
			return
		}
		s.internalError(w, err)
		return
	}
	c, err := s.contact(ctx, s.DB.Write, hh, id)
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.log().Info("phone: contact created", "household", hh, "by", u.ID)
	httpx.WriteJSON(w, http.StatusCreated, contactOut(c))
}

func ptrArg(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func (s *Service) handleUpdateContact(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, id := r.PathValue("household_id"), r.PathValue("contact_id")
	b, ok := readContactBody(w, r)
	if !ok {
		return
	}
	name, _ := b.str("name", false, 1, 255)
	number, _ := b.str("number", false, 1, 32)
	address, _ := b.str("address", false, 0, 0)
	notes, _ := b.str("notes", false, 0, 0)
	dnc, _ := b.boolean("do_not_call")
	if !b.done(w) {
		return
	}
	if !s.requireMember(w, r, u, hh) {
		return
	}
	ctx := r.Context()
	c, err := s.contact(ctx, s.DB.Read, hh, id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Contact not found")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	now := dbTime(s.now())
	set := []string{"updated_at = ?"}
	args := []any{now}
	if number != nil {
		num, err := NormalizeUS(*number)
		if err != nil {
			detail(w, http.StatusBadRequest, err.Error())
			return
		}
		// A hand-entered number is a fresh assertion about this business.
		set = append(set, "number = ?", "verified_at = ?", "source = 'manual'")
		args = append(args, num, now)
	}
	if name != nil {
		nm := strings.TrimSpace(*name)
		norm := NormalizeName(nm)
		if norm == "" {
			detail(w, http.StatusBadRequest, "Name must contain letters or digits")
			return
		}
		if taken, err := s.nameTaken(ctx, hh, norm, c.ID); err != nil {
			s.internalError(w, err)
			return
		} else if taken {
			detail(w, http.StatusConflict, fmt.Sprintf("'%s' is already in this household's phonebook", nm))
			return
		}
		set = append(set, "name = ?", "normalized_name = ?")
		args = append(args, nm, norm)
	}
	if address != nil {
		set = append(set, "address = ?")
		args = append(args, *address)
	}
	if notes != nil {
		set = append(set, "notes = ?")
		args = append(args, *notes)
	}
	if dnc != nil {
		set = append(set, "do_not_call = ?")
		args = append(args, boolInt(*dnc))
	}
	args = append(args, c.ID, hh)
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_phone_contacts SET `+strings.Join(set, ", ")+
		` WHERE id = ? AND household_id = ?`, args...); err != nil {
		s.internalError(w, err)
		return
	}
	c, err = s.contact(ctx, s.DB.Write, hh, id)
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.log().Info("phone: contact updated", "household", hh, "by", u.ID, "dnc", c.DoNotCall)
	httpx.WriteJSON(w, http.StatusOK, contactOut(c))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) handleDeleteContact(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, id := r.PathValue("household_id"), r.PathValue("contact_id")
	if !s.requireMember(w, r, u, hh) {
		return
	}
	res, err := s.DB.Write.ExecContext(r.Context(), `DELETE FROM cc_phone_contacts WHERE id = ? AND household_id = ?`, id, hh)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusNotFound, "Contact not found")
		return
	}
	s.log().Info("phone: contact deleted", "household", hh, "by", u.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) internalError(w http.ResponseWriter, err error) {
	s.log().Error("phone: internal error", "err", err)
	detail(w, http.StatusInternalServerError, "Internal Server Error")
}

// --- call context ---

func fieldsOut(fs []ContextField) []ContextField {
	if fs == nil {
		return []ContextField{}
	}
	return fs
}

// handleGetCallContext returns the caller's fields plus the grid's catalog. Reads degrade to
// an empty list, never a 500.
func (s *Service) handleGetCallContext(w http.ResponseWriter, r *http.Request, u authn.User) {
	uid := u.ID
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"fields":  fieldsOut(s.loadCallContext(r.Context(), &uid)),
		"catalog": Catalog(),
	})
}

// handlePutCallContext replaces the caller's fields with the grid's list, canonicalised the
// way a read sees them; stored as a JSON string. A failed write is a 500, never silent.
func (s *Service) handlePutCallContext(w http.ResponseWriter, r *http.Request, u authn.User) {
	var raw any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		validationError(w, "body: Input should be a valid dictionary or object to extract fields from")
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		validationError(w, "body: Input should be a valid dictionary or object to extract fields from")
		return
	}
	var rows []any
	if v, present := m["fields"]; present && v != nil {
		list, ok := v.([]any)
		if !ok {
			validationError(w, "body -> fields: Input should be a valid list")
			return
		}
		var errs []string
		for i, row := range list {
			rm, ok := row.(map[string]any)
			if !ok {
				errs = append(errs, fmt.Sprintf("body -> fields -> %d: Input should be a valid dictionary or object to extract fields from", i))
				continue
			}
			for _, k := range []string{"key", "label", "value", "category", "tier"} {
				if v, ok := rm[k]; ok && v != nil {
					if _, isStr := v.(string); !isStr {
						errs = append(errs, fmt.Sprintf("body -> fields -> %d -> %s: Input should be a valid string", i, k))
					}
				}
			}
			rows = append(rows, rm)
		}
		if len(errs) > 0 {
			validationError(w, errs...)
			return
		}
	}
	fields := PrepareForStorage(rows)
	if s.Settings == nil {
		detail(w, http.StatusInternalServerError, "Could not save call context")
		return
	}
	if err := s.Settings.Set(r.Context(), SettingCallContext, SerializeFields(fields), settings.Scope{UserID: u.ID}); err != nil {
		s.log().Error("phone: call context write failed", "user", u.ID, "err", err)
		detail(w, http.StatusInternalServerError, "Could not save call context")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"fields": fieldsOut(fields), "catalog": Catalog()})
}
