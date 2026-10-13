package authn

import (
	"errors"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// ErrPasswordChangeRequired is VerifyUser's answer for a valid token whose account must
// change its password (auth_users.must_change_password, set by a superuser's temp-password
// reset; G8 in docs/webapp/01). The token is genuine, so it is a 403, not a 401: a client
// must not refresh-and-retry or sign the user out, but send them to change the password.
// Only the auth module's GET /auth/me and POST /auth/change-password (and the token-less
// login, refresh and logout) accept such a token.
var ErrPasswordChangeRequired = errors.New("authn: password change required")

// CodePasswordChangeRequired is the machine-readable code clients branch on.
const CodePasswordChangeRequired = "password_change_required"

// WritePasswordChangeRequired writes the 403 every module answers ErrPasswordChangeRequired
// with: {"detail": ..., "code": "password_change_required"}.
func WritePasswordChangeRequired(w http.ResponseWriter) {
	httpx.WriteJSON(w, http.StatusForbidden, map[string]any{
		"detail": "Password change required: this account signed in with a temporary password. Change it first.",
		"code":   CodePasswordChangeRequired,
	})
}
