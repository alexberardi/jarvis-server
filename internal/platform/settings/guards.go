package settings

import (
	"context"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// UserVerifier verifies a user access token (the auth module's VerifyUser).
type UserVerifier func(ctx context.Context, token string) (authn.User, error)

// AppValidator checks app-to-app credentials (authn.Authority.ValidateApp).
type AppValidator func(ctx context.Context, appID, key string) (authn.App, bool, error)

// SuperuserGuard is jarvis-settings-client's create_superuser_auth: a Bearer token for an
// active superuser, with its exact error details.
func SuperuserGuard(verify UserVerifier) Guard {
	return func(w http.ResponseWriter, r *http.Request) bool {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			httpx.Error(w, http.StatusUnauthorized, "Missing or invalid Authorization header")
			return false
		}
		return superuser(w, r, verify, strings.TrimPrefix(h, "Bearer "))
	}
}

func superuser(w http.ResponseWriter, r *http.Request, verify UserVerifier, tok string) bool {
	u, err := verify(r.Context(), tok)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "Invalid or expired token")
		return false
	}
	if !u.IsSuperuser {
		httpx.Error(w, http.StatusForbidden, "Superuser access required")
		return false
	}
	return true
}

// CombinedGuard is create_combined_auth: a superuser Bearer token, or else app credentials.
func CombinedGuard(verify UserVerifier, app AppValidator) Guard {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			return superuser(w, r, verify, strings.TrimPrefix(h, "Bearer "))
		}
		if id, key, ok := authn.AppCreds(r); ok {
			_, valid, err := app(r.Context(), id, key)
			if err != nil {
				httpx.Error(w, http.StatusBadGateway, "Unable to validate credentials: auth service unreachable")
				return false
			}
			if !valid {
				httpx.Error(w, http.StatusUnauthorized, "Invalid app credentials")
				return false
			}
			return true
		}
		httpx.Error(w, http.StatusUnauthorized, "Missing authentication. Provide either Bearer token or app credentials.")
		return false
	}
}
