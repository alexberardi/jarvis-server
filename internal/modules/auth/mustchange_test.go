package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// TestMustChangePasswordIsEnforced is G8 (docs/webapp/01 §2.3): a temp-password session's
// token works only for reading itself and changing the password, and every other route says
// so with 403 password_change_required (not 401: the token is genuine).
func TestMustChangePasswordIsEnforced(t *testing.T) {
	e := newEnv(t)
	su := e.superuser()
	for _, target := range []*testUser{e.register(), e.superuser()} {
		old := target.access // issued before the reset: the flag applies to it too
		temp := e.expect(200, "", "POST", fmt.Sprintf("/superuser/users/%d/temp-password", target.id), nil,
			bearerH(e.relogin(su)))["temp_password"].(string)
		lm := e.expect(200, "", "POST", "/auth/login", map[string]any{"email": target.email, "password": temp}, nil)
		if lm["must_change_password"] != true {
			t.Fatal(lm)
		}
		pending := lm["access_token"].(string)

		for _, tok := range []string{pending, old} {
			if m := e.expect(200, "", "GET", "/auth/me", nil, bearerH(tok)); m["must_change_password"] != true {
				t.Fatalf("me: %v", m)
			}
			for _, c := range []struct{ method, path string }{
				{"GET", "/households"}, {"PATCH", "/auth/me"}, {"DELETE", "/auth/me"},
				{"POST", "/auth/switch-household"}, {"GET", "/households/" + target.household + "/members"},
				{"GET", "/superuser/users"}, {"PUT", "/settings/auth.token.access_expire_minutes"},
			} {
				code, m := e.do(c.method, c.path, map[string]any{"display_name": "x", "value": 1}, bearerH(tok))
				if code != 403 || m["code"] != authn.CodePasswordChangeRequired {
					t.Errorf("%s %s with a must-change token: %d %v", c.method, c.path, code, m)
				}
			}
			if _, err := e.m.VerifyUser(context.Background(), tok); !errors.Is(err, authn.ErrPasswordChangeRequired) {
				t.Errorf("VerifyUser: %v", err)
			}
		}
		// The refresh token still works (it is how the client keeps the session to change it).
		rm := e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": lm["refresh_token"]}, nil)
		if code, _ := e.do("GET", "/households", nil, bearerH(rm["access_token"].(string))); code != 403 {
			t.Errorf("a refreshed must-change token reached /households: %d", code)
		}

		cm := e.expect(200, "", "POST", "/auth/change-password",
			map[string]any{"current_password": temp, "new_password": "brand-new-pass"}, bearerH(pending))
		fresh := cm["access_token"].(string)
		e.expect(200, "", "GET", "/households", nil, bearerH(fresh))
		if u, err := e.m.VerifyUser(context.Background(), fresh); err != nil || u.ID != target.id {
			t.Errorf("VerifyUser after the change: %v %v", u, err)
		}
		// The pre-change token now belongs to an account in good standing again; it was
		// never revoked (access tokens are stateless) and still expires on its own.
		target.password = "brand-new-pass"
	}
}
