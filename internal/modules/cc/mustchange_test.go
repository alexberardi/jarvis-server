package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// G8: a must-change-password token is 403 password_change_required on user routes and on the
// provisioning (user-or-admin-key) routes, never a 500.
func TestMustChangePasswordToken(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v0/admin/nodes"},
		{"GET", "/api/v0/households/h1/smart-home/config"},
		{"POST", "/api/v0/mobile/chat"},
	} {
		r := e.do(c.method, c.path, map[string]any{}, bearer("must-change"))
		r.want(403)
		if r.json()["code"] != authn.CodePasswordChangeRequired {
			t.Errorf("%s %s: %s", c.method, c.path, r.body)
		}
	}
}
