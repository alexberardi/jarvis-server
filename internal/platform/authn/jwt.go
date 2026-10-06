// Package authn holds the authentication primitives every module shares: JWT minting and
// verification, credential header parsing, and the principals handlers receive.
//
// JWTs are in the HS256 → RS256 migration (root CLAUDE.md). The rule that matters: key
// material is chosen by the token's algorithm FAMILY, never from one shared "the key". The
// RS256 public key is published, so a verifier that used one key for both families could be
// fooled by an HS256 token signed with the public key as the HMAC secret. TestForgedHS256
// guards exactly that.
package authn

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Algorithms jarvisd mints and accepts.
const (
	HS256 = "HS256"
	RS256 = "RS256"
)

var (
	ErrExpired = errors.New("authn: token has expired")
	ErrInvalid = errors.New("authn: invalid token")
	ErrNoSub   = errors.New("authn: token has no user id")
)

// Keys is the key material for both families. Either side may be absent: HMAC-only before
// RS256 is configured, RSA-only once HS256 is retired.
type Keys struct {
	HMAC    []byte
	Private *rsa.PrivateKey // only the auth module mints RS256
	Public  *rsa.PublicKey
}

// ParsePrivateKey decodes AUTH_PRIVATE_KEY: base64 of a PKCS#8 (or PKCS#1) RSA PEM.
func ParsePrivateKey(b64 string) (*rsa.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("authn: private key is not base64: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("authn: private key is not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("authn: private key is not RSA")
		}
		return rk, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// PublicKeyPEM renders the public key as served by /auth/public-key ({"public_key": <PEM>}).
func PublicKeyPEM(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// Claims are the access-token claims jarvis-auth mints (`_build_jwt_claims`): sub is the
// user id as a string; household_id is the user's current household, when they have one.
type Claims struct {
	Email       string `json:"email,omitempty"`
	IsSuperuser bool   `json:"is_superuser"`
	HouseholdID string `json:"household_id,omitempty"`
	jwt.RegisteredClaims
}

// UserID parses sub as the integer user id.
func (c Claims) UserID() (int64, error) {
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || c.Subject == "" {
		return 0, ErrNoSub
	}
	return id, nil
}

// Mint signs claims with alg, setting iat and exp (ttl from now).
func (k Keys) Mint(c Claims, alg string, ttl time.Duration, now time.Time) (string, error) {
	c.IssuedAt = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	switch alg {
	case HS256:
		if len(k.HMAC) == 0 {
			return "", errors.New("authn: no HMAC secret to mint HS256")
		}
		return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(k.HMAC)
	case RS256:
		if k.Private == nil {
			return "", errors.New("authn: no private key to mint RS256")
		}
		return jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(k.Private)
	default:
		return "", fmt.Errorf("authn: unsupported algorithm %q", alg)
	}
}

// Verify checks a token's signature and expiry, choosing the key by algorithm family.
func (k Keys) Verify(token string, now time.Time) (Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		switch t.Method.(type) {
		case *jwt.SigningMethodHMAC:
			if t.Method.Alg() != HS256 || len(k.HMAC) == 0 {
				return nil, ErrInvalid
			}
			return k.HMAC, nil
		case *jwt.SigningMethodRSA:
			if t.Method.Alg() != RS256 {
				return nil, ErrInvalid
			}
			pub := k.Public
			if pub == nil && k.Private != nil {
				pub = &k.Private.PublicKey
			}
			if pub == nil {
				return nil, ErrInvalid // fail closed: an RS256 token we can't check is rejected
			}
			return pub, nil
		default:
			return nil, ErrInvalid
		}
	}, jwt.WithValidMethods([]string{HS256, RS256}), jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }))
	switch {
	case err == nil:
		return c, nil
	case errors.Is(err, jwt.ErrTokenExpired):
		return Claims{}, ErrExpired
	default:
		return Claims{}, ErrInvalid
	}
}
