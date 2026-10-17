package auth

import (
	"context"
	"testing"
)

// openRegistration sets auth.open_registration.
func (e *env) openRegistration(on bool) {
	e.t.Helper()
	if err := e.m.settings.Set(context.Background(), settingOpenRegistration, on, noScope); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) userCount(email string) int {
	e.t.Helper()
	n, err := count(context.Background(), e.db.Read, `SELECT COUNT(*) FROM auth_users WHERE email = ?`, email)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// Once a superuser exists, registering without an invite is closed by default.
func TestRegisterClosedOnceSuperuserExists(t *testing.T) {
	e := newEnv(t)
	e.superuser()
	households, _ := count(context.Background(), e.db.Read, `SELECT COUNT(*) FROM auth_households`)
	e.expect(403, registrationClosed, "POST", "/auth/register",
		map[string]any{"email": "stranger@example.com", "password": "password1"}, nil)
	e.expect(403, registrationClosed, "POST", "/auth/register",
		map[string]any{"email": "stranger@example.com", "password": "password1", "invite_code": ""}, nil)
	if e.userCount("stranger@example.com") != 0 {
		t.Fatal("user created")
	}
	if n, _ := count(context.Background(), e.db.Read, `SELECT COUNT(*) FROM auth_households`); n != households {
		t.Fatalf("households %d, want %d", n, households)
	}
}

func TestRegisterClosedIsTheDefault(t *testing.T) {
	e := newEnv(t)
	if e.m.settings.Bool(context.Background(), settingOpenRegistration, noScope) {
		t.Fatal("auth.open_registration defaults to on")
	}
}

// With auth.open_registration on, anyone may still register into their own "My Home".
func TestRegisterOpenSetting(t *testing.T) {
	e := newEnv(t)
	e.superuser()
	e.openRegistration(true)
	m := e.expect(201, "", "POST", "/auth/register", map[string]any{"email": "guest@example.com", "password": "password1"}, nil)
	hh := e.list("GET", "/households", bearerH(m["access_token"].(string)))
	if len(hh) != 1 || hh[0]["name"] != "My Home" || hh[0]["role"] != "admin" {
		t.Fatalf("households %v", hh)
	}
}

// An invite code is accepted whatever the setting; a bad one stays the 400.
func TestRegisterWithInviteWhileClosed(t *testing.T) {
	e := newEnv(t)
	owner := e.superuser()
	inv := e.expect(201, "", "POST", "/households/"+owner.household+"/invites", map[string]any{}, bearerH(owner.access))
	m := e.expect(201, "", "POST", "/auth/register",
		map[string]any{"email": "invited@example.com", "password": "password1", "invite_code": inv["code"]}, nil)
	if m["household_id"] != owner.household {
		t.Fatalf("household %v, want %s", m["household_id"], owner.household)
	}
	e.expect(400, "Invalid or expired invite code", "POST", "/auth/register",
		map[string]any{"email": "guess@example.com", "password": "password1", "invite_code": "NOPE1234"}, nil)
}

// First run: before any superuser exists, registration and setup work as before; after setup,
// registration without an invite is closed.
func TestRegisterFirstRun(t *testing.T) {
	e := newEnv(t)
	e.expect(201, "", "POST", "/auth/register", map[string]any{"email": "early@example.com", "password": "password1"}, nil)
	e.expect(201, "", "POST", "/auth/setup", map[string]any{"email": "root@example.com", "password": "password1"},
		hdrs{SetupTokenHeader: e.setupToken()})
	e.expect(403, registrationClosed, "POST", "/auth/register",
		map[string]any{"email": "late@example.com", "password": "password1"}, nil)
	if e.userCount("late@example.com") != 0 {
		t.Fatal("user created")
	}
}
