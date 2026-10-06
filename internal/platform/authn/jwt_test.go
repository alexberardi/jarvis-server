package authn

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func claims() Claims {
	c := Claims{Email: "a@b.c", IsSuperuser: true, HouseholdID: "hh-1"}
	c.Subject = "42"
	return c
}

func TestMintVerifyBothFamilies(t *testing.T) {
	k := Keys{HMAC: []byte("secret"), Private: rsaKey(t)}
	for _, alg := range []string{HS256, RS256} {
		tok, err := k.Mint(claims(), alg, time.Hour, now)
		if err != nil {
			t.Fatal(err)
		}
		got, err := k.Verify(tok, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		id, _ := got.UserID()
		if id != 42 || got.Email != "a@b.c" || !got.IsSuperuser || got.HouseholdID != "hh-1" {
			t.Fatalf("%s: claims %+v", alg, got)
		}
	}
}

// A verifier holding only the public key (every module but auth) accepts RS256.
func TestVerifyWithPublicKeyOnly(t *testing.T) {
	priv := rsaKey(t)
	tok, _ := Keys{Private: priv}.Mint(claims(), RS256, time.Hour, now)
	if _, err := (Keys{Public: &priv.PublicKey}).Verify(tok, now); err != nil {
		t.Fatal(err)
	}
}

// The attack the algorithm-family rule exists for: an HS256 token whose HMAC secret is the
// published RSA public key PEM must be rejected.
func TestForgedHS256WithPublicKeyIsRejected(t *testing.T) {
	priv := rsaKey(t)
	pubPEM, err := PublicKeyPEM(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims()).SignedString([]byte(pubPEM))
	if err != nil {
		t.Fatal(err)
	}
	// Verifier with a different real HMAC secret and the RSA key.
	k := Keys{HMAC: []byte("the-real-secret"), Public: &priv.PublicKey}
	if _, err := k.Verify(forged, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged token accepted: %v", err)
	}
	// Verifier with no HMAC secret at all (HS256 retired).
	if _, err := (Keys{Public: &priv.PublicKey}).Verify(forged, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged token accepted with HS256 retired: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	priv := rsaKey(t)
	k := Keys{HMAC: []byte("secret"), Private: priv}
	hs, _ := k.Mint(claims(), HS256, time.Hour, now)
	rs, _ := k.Mint(claims(), RS256, time.Hour, now)
	other, _ := Keys{HMAC: []byte("other")}.Mint(claims(), HS256, time.Hour, now)
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, claims()).SignedString(jwt.UnsafeAllowNoneSignatureType)
	hs512, _ := jwt.NewWithClaims(jwt.SigningMethodHS512, claims()).SignedString([]byte("secret"))
	noExp := func() string {
		c := claims()
		s, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte("secret"))
		return s
	}()

	cases := []struct {
		name string
		keys Keys
		tok  string
		at   time.Time
		want error
	}{
		{"expired", k, hs, now.Add(2 * time.Hour), ErrExpired},
		{"wrong secret", k, other, now, ErrInvalid},
		{"alg none", k, none, now, ErrInvalid},
		{"HS512", k, hs512, now, ErrInvalid},
		{"no exp", k, noExp, now, ErrInvalid},
		{"garbage", k, "a.b.c", now, ErrInvalid},
		{"RS256 without key", Keys{HMAC: []byte("secret")}, rs, now, ErrInvalid},
		{"HS256 without secret", Keys{Public: &priv.PublicKey}, hs, now, ErrInvalid},
	}
	for _, c := range cases {
		if _, err := c.keys.Verify(c.tok, c.at); !errors.Is(err, c.want) {
			t.Errorf("%s: err=%v, want %v", c.name, err, c.want)
		}
	}
}

func TestUserIDRequiresNumericSub(t *testing.T) {
	for _, sub := range []string{"", "abc"} {
		c := Claims{}
		c.Subject = sub
		if _, err := c.UserID(); !errors.Is(err, ErrNoSub) {
			t.Errorf("sub %q: %v", sub, err)
		}
	}
}

func TestParsePrivateKey(t *testing.T) {
	priv := rsaKey(t)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	b64 := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	got, err := ParsePrivateKey(b64)
	if err != nil || !got.Equal(priv) {
		t.Fatalf("pkcs8: %v", err)
	}
	pk1 := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}))
	if got, err := ParsePrivateKey(pk1); err != nil || !got.Equal(priv) {
		t.Fatalf("pkcs1: %v", err)
	}
	for _, bad := range []string{"!!", base64.StdEncoding.EncodeToString([]byte("not pem"))} {
		if _, err := ParsePrivateKey(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}
