package notifications

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

type mustChangeUsers struct{}

func (mustChangeUsers) VerifyUser(context.Context, string) (authn.User, error) {
	return authn.User{}, authn.ErrPasswordChangeRequired
}

// G8: a must-change-password token is 403 password_change_required, not a 500.
func TestMustChangePasswordToken(t *testing.T) {
	e := setup(t, func(m *Module) { m.Users = mustChangeUsers{} })
	for _, path := range []string{"/api/v0/inbox", "/api/v0/inbox/unread-count"} {
		code, body := e.do(t, "GET", path, "", "Authorization", "Bearer x")
		if code != 403 || !strings.Contains(body, `"code":"password_change_required"`) {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}
