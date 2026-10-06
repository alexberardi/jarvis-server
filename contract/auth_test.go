//go:build contract

package contract

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// userOut is jarvis-auth's UserOut.
var userOut = Obj{
	"id":                   Int,
	"email":                NonEmptyString,
	"username":             NullOr(String),
	"is_superuser":         Bool,
	"must_change_password": Bool,
}

// tokenResponse is login/refresh's TokenResponse.
var tokenResponse = Obj{
	"access_token":         NonEmptyString,
	"refresh_token":        NonEmptyString,
	"token_type":           Eq("bearer"),
	"user":                 userOut,
	"must_change_password": Bool,
}

// registerResponse is RegisterResponse (register and first-run setup).
var registerResponse = Obj{
	"access_token":  NonEmptyString,
	"refresh_token": NonEmptyString,
	"token_type":    Eq("bearer"),
	"user":          userOut,
	"household_id":  UUID,
}

// validateNodeResponse is NodeValidateResponse. All keys are always present (null when unset).
var validateNodeInvalid = func(reason string) Matcher {
	return Obj{
		"valid":                Eq(false),
		"node_id":              Null,
		"household_id":         Null,
		"household_member_ids": Null,
		"reason":               Eq(reason),
	}
}

// --- app-to-app ---

func TestAuthAppPing(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)

	t.Run("valid", func(t *testing.T) {
		tg.Get(t, Auth, "/internal/app-ping", app.H()).
			Expect(http.StatusOK, Obj{"app_id": Eq(app.ID), "name": String})
	})
	t.Run("missing", func(t *testing.T) {
		tg.Get(t, Auth, "/internal/app-ping").ExpectError(http.StatusUnauthorized, "Missing app credentials")
		tg.Get(t, Auth, "/internal/app-ping", H{"X-Jarvis-App-Id": app.ID}).
			ExpectError(http.StatusUnauthorized, "Missing app credentials")
	})
	t.Run("wrong key", func(t *testing.T) {
		tg.Get(t, Auth, "/internal/app-ping", H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong"}).
			ExpectError(http.StatusUnauthorized, "Invalid app credentials")
	})
	t.Run("unknown app", func(t *testing.T) {
		tg.Get(t, Auth, "/internal/app-ping", H{"X-Jarvis-App-Id": "contract-no-such-app", "X-Jarvis-App-Key": "x"}).
			ExpectError(http.StatusUnauthorized, "Invalid app credentials")
	})
	t.Run("revoked", func(t *testing.T) {
		a := NewApp(t, "revoked")
		tg.Get(t, Auth, "/internal/app-ping", a.H()).ExpectStatus(http.StatusOK)
		tg.Post(t, Auth, "/admin/app-clients/"+a.ID+"/revoke", nil, tg.AdminH()).
			Expect(http.StatusOK, Obj{"app_id": Eq(a.ID), "is_active": Eq(false)})
		tg.Get(t, Auth, "/internal/app-ping", a.H()).ExpectError(http.StatusUnauthorized, "Invalid app credentials")
	})
}

// TestAuthValidateNode freezes POST /internal/validate-node, which every node-facing service
// (command-center, logs, whisper, tts, notifications) calls on each uncached node request.
// Failures are 200 with valid=false and a reason, never an HTTP error.
func TestAuthValidateNode(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	user := SharedUser(t)
	node := SharedNode(t)

	body := func(nodeID, key, service string) map[string]string {
		return map[string]string{"node_id": nodeID, "node_key": key, "service_id": service}
	}

	t.Run("valid", func(t *testing.T) {
		r := tg.Post(t, Auth, "/internal/validate-node", body(node.ID, node.Key, app.ID), app.H()).
			Expect(http.StatusOK, Obj{
				"valid":                Eq(true),
				"node_id":              Eq(node.ID),
				"household_id":         Eq(node.HouseholdID),
				"household_member_ids": NonEmptyArrayOf(Int),
				"reason":               Null,
			})
		// The household's members are what speaker recognition is scoped to.
		ids := r.Object()["household_member_ids"].([]any)
		found := false
		for _, id := range ids {
			if mustFloat(id) == float64(user.ID) {
				found = true
			}
		}
		if !found {
			r.Fatalf("household_member_ids should contain the household's user %d", user.ID)
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		tg.Post(t, Auth, "/internal/validate-node", body(node.ID, "wrong", app.ID), app.H()).
			Expect(http.StatusOK, validateNodeInvalid("Invalid node credentials"))
	})
	t.Run("unknown node", func(t *testing.T) {
		tg.Post(t, Auth, "/internal/validate-node", body("contract-no-such-node", "x", app.ID), app.H()).
			Expect(http.StatusOK, validateNodeInvalid("Node not found"))
	})
	t.Run("service not granted", func(t *testing.T) {
		tg.Post(t, Auth, "/internal/validate-node", body(node.ID, node.Key, "contract-other-service"), app.H()).
			Expect(http.StatusOK, validateNodeInvalid("Node is not authorized to access service 'contract-other-service'"))
	})
	t.Run("inactive", func(t *testing.T) {
		n := NewNode(t, user.HouseholdID, app.ID)
		tg.Post(t, Auth, "/internal/validate-node", body(n.ID, n.Key, app.ID), app.H()).ExpectStatus(http.StatusOK)
		tg.Do(t, Auth, http.MethodDelete, "/admin/nodes/"+n.ID, nil, tg.AdminH()).
			Expect(http.StatusOK, Obj{"node_id": Eq(n.ID), "is_active": Eq(false)})
		tg.Post(t, Auth, "/internal/validate-node", body(n.ID, n.Key, app.ID), app.H()).
			Expect(http.StatusOK, validateNodeInvalid("Node is inactive"))
	})
	t.Run("no app credentials", func(t *testing.T) {
		tg.Post(t, Auth, "/internal/validate-node", body(node.ID, node.Key, app.ID)).
			ExpectError(http.StatusUnauthorized, "Missing app credentials")
	})
	t.Run("missing fields", func(t *testing.T) {
		tg.Post(t, Auth, "/internal/validate-node", map[string]string{"node_id": node.ID}, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).
			ExpectShape(ValidationError("body", "node_key"))
	})
}

// --- RS256 public key ---

// TestAuthPublicKey freezes GET /auth/public-key (unauthenticated). Verifiers read "public_key";
// jarvis-recipes-server does resp.json().get("public_key").
func TestAuthPublicKey(t *testing.T) {
	tg := T(t)
	tg.Need(t, Auth)
	r := tg.Get(t, Auth, "/auth/public-key").Expect(http.StatusOK, Obj{
		"public_key": Regexp(`^-----BEGIN PUBLIC KEY-----\n(?s:.+)\n-----END PUBLIC KEY-----\n?$`),
		"algorithm":  Eq("RS256"),
		"kid":        NonEmptyString,
	})
	if _, err := jwt.ParseRSAPublicKeyFromPEM([]byte(r.Object()["public_key"].(string))); err != nil {
		r.Fatalf("public_key is not an RSA public key: %v", err)
	}
}

// --- user tokens ---

// claimsShape is the access-token payload. command-center reads sub (cast to int), email,
// is_superuser and household_id; the RS256 migration (umbrella CLAUDE.md) keeps these.
var claimsShape = Obj{
	"sub":          Regexp(`^[0-9]+$`),
	"email":        NonEmptyString,
	"is_superuser": Bool,
	"household_id": Optional(UUID),
	"jti":          NonEmptyString,
	"exp":          Int,
	"iat":          Int,
}

// checkAccessToken verifies the token's header and claim shape. HS256 tokens can't be verified
// without the shared secret, so only their shape is checked; RS256 tokens are verified against
// the published public key.
func checkAccessToken(t *testing.T, tg *Target, tok string, u *User) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("access token is not a JWS: %q", tok)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	decodeSeg(t, parts[0], &hdr)
	switch hdr.Alg {
	case "HS256":
	case "RS256":
		pem := tg.Get(t, Auth, "/auth/public-key").ExpectStatus(http.StatusOK).Object()["public_key"].(string)
		key, err := jwt.ParseRSAPublicKeyFromPEM([]byte(pem))
		if err != nil {
			t.Fatalf("public key: %v", err)
		}
		if _, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return key, nil },
			jwt.WithValidMethods([]string{"RS256"})); err != nil {
			t.Fatalf("RS256 access token does not verify against /auth/public-key: %v", err)
		}
	default:
		t.Fatalf("access token alg: want HS256 or RS256, got %q", hdr.Alg)
	}
	if hdr.Typ != "JWT" {
		t.Fatalf("access token typ: want JWT, got %q", hdr.Typ)
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var claims any
	if err := dec.Decode(&claims); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if errs := claimsShape.Match("claims", claims); len(errs) > 0 {
		t.Fatalf("claims shape:\n  %s\nclaims: %s", strings.Join(errs, "\n  "), raw)
	}
	c := claims.(map[string]any)
	if u != nil {
		if errs := (Open{"email": Eq(u.Email), "is_superuser": Eq(u.Superuser)}).Match("claims", c); len(errs) > 0 {
			t.Fatalf("claims: %v", errs)
		}
		if u.HouseholdID != "" && c["household_id"] != u.HouseholdID {
			t.Fatalf("claims.household_id: want %s, got %v", u.HouseholdID, c["household_id"])
		}
	}
	exp, _ := c["exp"].(json.Number).Int64()
	if time.Until(time.Unix(exp, 0)) <= 0 {
		t.Fatalf("access token already expired (exp %d)", exp)
	}
}

func decodeSeg(t *testing.T, seg string, v any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode JWS segment: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode JWS segment JSON: %v", err)
	}
}

func TestAuthRegisterLoginRefresh(t *testing.T) {
	tg := T(t)
	tg.NeedAdmin(t)

	// Register by hand (not NewUser) so the raw response can be frozen; still cleaned up.
	email := "contract-" + tg.RunID + "-reg@example.com"
	password := "contract-" + randHex(8)
	r := tg.Post(t, Auth, "/auth/register", map[string]string{"email": email, "password": password}).
		Expect(http.StatusCreated, registerResponse)
	var reg struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		HouseholdID  string `json:"household_id"`
		User         struct {
			ID       int    `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	r.Decode(&reg)
	u := &User{ID: reg.User.ID, Email: email, Password: password, HouseholdID: reg.HouseholdID,
		AccessToken: reg.AccessToken, RefreshToken: reg.RefreshToken}
	t.Cleanup(func() {
		if err := tg.deleteUser(u); err != nil {
			t.Errorf("cleanup user: %v", err)
		}
	})
	// The username defaults to the email's local part.
	if want := strings.SplitN(email, "@", 2)[0]; reg.User.Username != want {
		r.Fatalf("user.username: want %q (email local part), got %q", want, reg.User.Username)
	}
	checkAccessToken(t, tg, reg.AccessToken, u)

	t.Run("register duplicate email", func(t *testing.T) {
		tg.Post(t, Auth, "/auth/register", map[string]string{"email": email, "password": password}).
			ExpectError(http.StatusBadRequest, "Email already registered")
	})
	t.Run("register short password", func(t *testing.T) {
		tg.Post(t, Auth, "/auth/register", map[string]string{"email": "contract-" + tg.RunID + "-short@example.com", "password": "short"}).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "password"))
	})

	t.Run("login", func(t *testing.T) {
		r := tg.Post(t, Auth, "/auth/login", map[string]string{"email": email, "password": password}).
			Expect(http.StatusOK, tokenResponse)
		var out struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		r.Decode(&out)
		checkAccessToken(t, tg, out.AccessToken, u)
	})
	t.Run("login wrong password", func(t *testing.T) {
		tg.Post(t, Auth, "/auth/login", map[string]string{"email": email, "password": "wrong-password"}).
			ExpectError(http.StatusUnauthorized, "Invalid email or password")
	})
	t.Run("login unknown email", func(t *testing.T) {
		// Same detail as a wrong password, so accounts can't be probed.
		tg.Post(t, Auth, "/auth/login", map[string]string{"email": "contract-" + tg.RunID + "-nobody@example.com", "password": "whatever-password"}).
			ExpectError(http.StatusUnauthorized, "Invalid email or password")
	})

	t.Run("refresh rotates", func(t *testing.T) {
		r := tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": reg.RefreshToken}).
			Expect(http.StatusOK, tokenResponse)
		var out struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		r.Decode(&out)
		if out.RefreshToken == reg.RefreshToken {
			r.Fatalf("refresh must rotate the refresh token")
		}
		checkAccessToken(t, tg, out.AccessToken, u)

		// Grace window (auth.token.refresh_grace_seconds, default 10s): replaying the parent
		// right away returns the successor already minted, not a 401. Mobile depends on this
		// when two refreshes race or a response is lost.
		r2 := tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": reg.RefreshToken}).
			Expect(http.StatusOK, tokenResponse)
		var again struct {
			RefreshToken string `json:"refresh_token"`
		}
		r2.Decode(&again)
		if again.RefreshToken != out.RefreshToken {
			r2.Fatalf("replay within the grace window should return the same successor")
		}
	})
	t.Run("refresh invalid", func(t *testing.T) {
		tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": "contract-not-a-token"}).
			ExpectError(http.StatusUnauthorized, "Invalid refresh token")
	})
}

func TestAuthMe(t *testing.T) {
	tg := T(t)
	u := SharedUser(t)

	r := tg.Get(t, Auth, "/auth/me", u.H()).Expect(http.StatusOK, userOut)
	if errs := (Open{"id": Eq(u.ID), "email": Eq(u.Email), "is_superuser": Eq(false)}).Match("$", r.JSON()); len(errs) > 0 {
		r.Fatalf("%v", errs)
	}

	t.Run("no token", func(t *testing.T) {
		r := tg.Get(t, Auth, "/auth/me").ExpectError(http.StatusUnauthorized, "Not authenticated")
		if got := r.Header["Www-Authenticate"]; len(got) != 1 || got[0] != "Bearer" {
			r.Fatalf("WWW-Authenticate: want Bearer, got %v", got)
		}
	})
	t.Run("garbage token", func(t *testing.T) {
		tg.Get(t, Auth, "/auth/me", Bearer("not-a-jwt")).
			ExpectError(http.StatusUnauthorized, "Could not validate credentials")
	})
}

func TestAuthSetupStatus(t *testing.T) {
	tg := T(t)
	tg.Need(t, Auth)
	// jarvis-admin's first-run wizard polls this. The value depends on the target; freeze the type.
	tg.Get(t, Auth, "/auth/setup-status").Expect(http.StatusOK, Obj{"needs_setup": Bool})
}

func TestAuthAdminToken(t *testing.T) {
	tg := T(t)
	tg.NeedAdmin(t)
	tg.Get(t, Auth, "/admin/app-clients", H{"X-Jarvis-Admin-Token": "contract-wrong-token"}).
		ExpectError(http.StatusUnauthorized, "Unauthorized")
	tg.Get(t, Auth, "/admin/app-clients").ExpectError(http.StatusUnauthorized, "Unauthorized")
	tg.Get(t, Auth, "/admin/app-clients", tg.AdminH()).Expect(http.StatusOK, NonEmptyArrayOf(Obj{
		"app_id":          NonEmptyString,
		"name":            String,
		"is_active":       Bool,
		"created_at":      TimestampUTC,
		"last_rotated_at": NullOr(TimestampUTC),
	}))
}
