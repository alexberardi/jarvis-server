package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// signingKey is one row of auth_signing_keys.
type signingKey struct {
	kid    string
	priv   *rsa.PrivateKey
	pubPEM string
	active bool
}

// rsaBits for a generated key. Tests lower it.
var rsaBits = 2048

// loadKeys returns the RS256 keys, generating and storing one on first use (decision log:
// one key generated on first run, no env var, nothing to sync). The newest active key mints;
// every stored key verifies, so an imported legacy key keeps old sessions alive.
func (m *Module) loadKeys(ctx context.Context) ([]signingKey, error) {
	m.keyMu.Lock()
	defer m.keyMu.Unlock()
	if m.keys != nil {
		return m.keys, nil
	}
	keys, err := m.readKeys(ctx)
	if err != nil {
		return nil, err
	}
	if !hasActive(keys) {
		k, err := generateKey()
		if err != nil {
			return nil, err
		}
		privPEM, err := privateKeyPEM(k.priv)
		if err != nil {
			return nil, err
		}
		if _, err := m.deps.DB.Write.ExecContext(ctx,
			`INSERT INTO auth_signing_keys (kid, algorithm, private_pem, public_pem, is_active) VALUES (?, 'RS256', ?, ?, 1)`,
			k.kid, privPEM, k.pubPEM); err != nil {
			return nil, fmt.Errorf("auth: store signing key: %w", err)
		}
		m.deps.Log.Info("auth: generated RS256 signing key", "kid", k.kid)
		if keys, err = m.readKeys(ctx); err != nil {
			return nil, err
		}
	}
	m.keys = keys
	return keys, nil
}

func hasActive(keys []signingKey) bool {
	for _, k := range keys {
		if k.active {
			return true
		}
	}
	return false
}

// readKeys reads every stored key, newest first.
func (m *Module) readKeys(ctx context.Context) ([]signingKey, error) {
	rows, err := m.deps.DB.Write.QueryContext(ctx,
		`SELECT kid, private_pem, is_active FROM auth_signing_keys WHERE algorithm = 'RS256' ORDER BY is_active DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("auth: read signing keys: %w", err)
	}
	defer rows.Close()
	var out []signingKey
	for rows.Next() {
		var kid, privPEM string
		var active bool
		if err := rows.Scan(&kid, &privPEM, &active); err != nil {
			return nil, err
		}
		priv, err := parsePrivatePEM(privPEM)
		if err != nil {
			m.deps.Log.Error("auth: unusable signing key; skipped", "kid", kid, "err", err)
			continue
		}
		pub, err := authn.PublicKeyPEM(&priv.PublicKey)
		if err != nil {
			return nil, err
		}
		out = append(out, signingKey{kid: kid, priv: priv, pubPEM: pub, active: active})
	}
	return out, rows.Err()
}

func generateKey() (signingKey, error) {
	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return signingKey{}, fmt.Errorf("auth: generate RSA key: %w", err)
	}
	pub, err := authn.PublicKeyPEM(&priv.PublicKey)
	if err != nil {
		return signingKey{}, err
	}
	return signingKey{kid: keyID(&priv.PublicKey), priv: priv, pubPEM: pub, active: true}, nil
}

// keyID is a stable id for a public key: the first 8 bytes of SHA-256 over its DER, in hex.
func keyID(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:8])
}

func privateKeyPEM(k *rsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// parsePrivatePEM reads a stored private key: PEM, or base64 of PEM (the legacy
// AUTH_PRIVATE_KEY form, if an import copied it verbatim).
func parsePrivatePEM(s string) (*rsa.PrivateKey, error) {
	if !strings.Contains(s, "-----BEGIN") {
		return authn.ParsePrivateKey(strings.TrimSpace(s))
	}
	return authn.ParsePrivateKey(base64.StdEncoding.EncodeToString([]byte(s)))
}

// --- minting and verifying ---

// mintAlgorithm is what to mint with: the auth.algorithm setting (default RS256). HS256 is
// honoured only when AUTH_SECRET_KEY is configured; otherwise it degrades to RS256, as the
// legacy code degraded RS256 → HS256 when no private key was configured.
func (m *Module) mintAlgorithm(ctx context.Context) string {
	alg := strings.ToUpper(strings.TrimSpace(m.settings.String(ctx, settingAlgorithm, noScope)))
	switch alg {
	case authn.HS256:
		if m.HMACSecret == "" {
			m.deps.Log.Error("auth: auth.algorithm is HS256 but AUTH_SECRET_KEY is unset; minting RS256 instead")
			return authn.RS256
		}
		return authn.HS256
	case authn.RS256:
		return authn.RS256
	default:
		m.deps.Log.Error("auth: unsupported auth.algorithm; minting RS256 instead", "configured", alg)
		return authn.RS256
	}
}

// accessClaims is the claim set `_build_jwt_claims` produced. household_id is the user's
// first household (by membership id) unless given.
type accessClaims struct {
	userID      int64
	email       string
	isSuperuser bool
	householdID string
}

// mintAccess signs an access token: sub (string id), email, is_superuser, household_id (when
// set), jti, iat and exp.
func (m *Module) mintAccess(ctx context.Context, c accessClaims) (string, error) {
	ttl := time.Duration(m.settings.Int(ctx, settingAccessMinutes, noScope)) * time.Minute
	claims := authn.Claims{Email: c.email, IsSuperuser: c.isSuperuser, HouseholdID: c.householdID}
	claims.Subject = fmt.Sprint(c.userID)
	claims.ID = tokenURLSafe(8)
	t := now()
	claims.IssuedAt = jwt.NewNumericDate(t)
	claims.ExpiresAt = jwt.NewNumericDate(t.Add(ttl))

	switch m.mintAlgorithm(ctx) {
	case authn.HS256:
		return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(m.HMACSecret))
	default:
		keys, err := m.loadKeys(ctx)
		if err != nil {
			return "", err
		}
		k := keys[0] // newest active first
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = k.kid
		return tok.SignedString(k.priv)
	}
}

// verifyError mirrors python-jose's messages, which some details embed.
type verifyError struct{ msg string }

func (e verifyError) Error() string { return e.msg }

var (
	errMalformed = verifyError{"Not enough segments"}
	errExpired   = verifyError{"Signature has expired."}
	errSignature = verifyError{"Signature verification failed."}
)

// verify checks a token, choosing key material by algorithm family (authn.Keys.Verify): HS256
// only against AUTH_SECRET_KEY, RS256 only against our public keys. An HS256 token signed with
// the published public key as the HMAC secret is therefore rejected.
func (m *Module) verify(ctx context.Context, token string) (authn.Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return authn.Claims{}, errMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &hdr) != nil {
		return authn.Claims{}, errMalformed
	}
	keys := []authn.Keys{{HMAC: []byte(m.HMACSecret)}}
	if hdr.Alg == authn.RS256 {
		stored, err := m.loadKeys(ctx)
		if err != nil {
			return authn.Claims{}, err
		}
		keys = keys[:0]
		// The key named by kid first, then the rest (tokens minted before kids existed).
		for _, k := range stored {
			if k.kid == hdr.Kid {
				keys = append(keys, authn.Keys{Public: &k.priv.PublicKey})
			}
		}
		for _, k := range stored {
			if k.kid != hdr.Kid {
				keys = append(keys, authn.Keys{Public: &k.priv.PublicKey})
			}
		}
	}
	var last error = errSignature
	for _, k := range keys {
		c, err := k.Verify(token, now())
		if err == nil {
			return c, nil
		}
		if errors.Is(err, authn.ErrExpired) {
			return authn.Claims{}, errExpired
		}
		last = errSignature
	}
	return authn.Claims{}, last
}

// handlePublicKey serves GET /auth/public-key, unauthenticated: {public_key, algorithm, kid}.
func (m *Module) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	keys, err := m.loadKeys(r.Context())
	if err != nil || len(keys) == 0 {
		m.deps.Log.Error("auth: no signing key", "err", err)
		detail(w, http.StatusServiceUnavailable, "No RS256 public key configured")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"public_key": keys[0].pubPEM, "algorithm": authn.RS256, "kid": keys[0].kid,
	})
}
