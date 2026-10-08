package update

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestProjectKeyVerifiesRealRelease checks the embedded key against a real signature made by
// the minisign CLI with the project's secret key (jarvis-node-setup v0.3.1's checksums.txt,
// prehashed "ED", default trusted comment).
func TestProjectKeyVerifiesRealRelease(t *testing.T) {
	k, err := ParsePublicKey(ProjectPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := k.KeyID(); got != "C9A24FB502A25B72" {
		t.Fatalf("key id %s", got)
	}
	msg := readTestdata(t, "node-setup-v0.3.1-checksums.txt")
	sig := readTestdata(t, "node-setup-v0.3.1-checksums.txt.minisig")
	comment, err := VerifyFile(msg, sig, k)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(comment, "file:checksums.txt") {
		t.Fatalf("trusted comment %q", comment)
	}
	// One flipped byte in the message fails.
	bad := bytes.Clone(msg)
	bad[0] ^= 1
	if _, err := VerifyFile(bad, sig, k); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered message: %v", err)
	}
}

// TestIndependentVectors verifies signatures made by another implementation
// (aead.dev/minisign) with a throwaway key: legacy "Ed" and prehashed "ED".
func TestIndependentVectors(t *testing.T) {
	k, err := ParsePublicKey(string(readTestdata(t, "vector.pub")))
	if err != nil {
		t.Fatal(err)
	}
	if k.KeyID() != "CF0EA783FE220CB3" {
		t.Fatalf("key id %s", k.KeyID())
	}
	msg := readTestdata(t, "vector.msg")
	for _, name := range []string{"vector-legacy.minisig", "vector-prehashed.minisig"} {
		t.Run(name, func(t *testing.T) {
			sigFile := readTestdata(t, name)
			sig, err := ParseSignature(sigFile)
			if err != nil {
				t.Fatal(err)
			}
			want := algLegacy
			if strings.Contains(name, "prehashed") {
				want = algPrehashed
			}
			if sig.Algorithm != want {
				t.Fatalf("algorithm %q", sig.Algorithm[:])
			}
			comment, err := k.Verify(msg, sig)
			if err != nil {
				t.Fatal(err)
			}
			if comment != "jarvisd v1.2.3 SHA256SUMS" {
				t.Fatalf("comment %q", comment)
			}
			// A swapped trusted comment fails the global signature.
			forged := bytes.Replace(sigFile, []byte("jarvisd v1.2.3"), []byte("jarvisd v9.9.9"), 1)
			if _, err := VerifyFile(msg, forged, k); !errors.Is(err, ErrSignature) || !strings.Contains(err.Error(), "trusted comment") {
				t.Fatalf("forged comment: %v", err)
			}
			// The wrong key fails on the key id.
			project, _ := ParsePublicKey(ProjectPublicKey)
			if _, err := VerifyFile(msg, sigFile, project); !errors.Is(err, ErrSignature) {
				t.Fatalf("wrong key: %v", err)
			}
			// Several keys: any one may verify.
			if _, err := VerifyFile(msg, sigFile, project, k); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "not base64!", "RWQ=", "untrusted comment: x\nRWSzDCL+g6cOz4AS6+vow4qZ0uS+LtPAWaV8XjfUV6koJHBCJIlcC+K"} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) accepted", bad)
		}
	}
	good := string(readTestdata(t, "vector-legacy.minisig"))
	lines := strings.Split(good, "\n")
	for name, s := range map[string]string{
		"short":        lines[0] + "\n" + lines[1],
		"no untrusted": "x\n" + strings.Join(lines[1:], "\n"),
		"no trusted":   lines[0] + "\n" + lines[1] + "\nx\n" + lines[3],
		"bad sig":      lines[0] + "\nAAAA\n" + lines[2] + "\n" + lines[3],
		"bad global":   lines[0] + "\n" + lines[1] + "\n" + lines[2] + "\nAAAA",
	} {
		if _, err := ParseSignature([]byte(s)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// CRLF line endings are fine.
	if _, err := ParseSignature([]byte(strings.ReplaceAll(good, "\n", "\r\n"))); err != nil {
		t.Fatal(err)
	}
}

func TestTestSignerRoundTrip(t *testing.T) {
	s := newTestSigner(t)
	msg := []byte("hello\n")
	for _, prehash := range []bool{false, true} {
		if _, err := VerifyFile(msg, s.sign(msg, "jarvisd v1.0.0 SHA256SUMS", prehash), s.pub); err != nil {
			t.Fatal(prehash, err)
		}
	}
}

// testSigner makes minisign signatures with a throwaway key (tests only).
type testSigner struct {
	pub  PublicKey
	priv ed25519.PrivateKey
}

func newTestSigner(t testing.TB) testSigner {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	k := PublicKey{ID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}, Key: pub}
	return testSigner{pub: k, priv: priv}
}

func (s testSigner) sign(msg []byte, trusted string, prehash bool) []byte {
	return signMinisign(s.priv, s.pub.ID, msg, trusted, prehash)
}

func TestVersionOrder(t *testing.T) {
	ordered := []string{"v0.9.9", "v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta.2", "v1.0.0-beta.11",
		"v1.0.0-rc.1", "v1.0.0", "1.0.1", "v1.10.0", "v2.0.0+build.5"}
	for i := 0; i+1 < len(ordered); i++ {
		a, okA := ParseVersion(ordered[i])
		b, okB := ParseVersion(ordered[i+1])
		if !okA || !okB || a.Compare(b) >= 0 || b.Compare(a) <= 0 {
			t.Fatalf("%s < %s", ordered[i], ordered[i+1])
		}
	}
	for _, bad := range []string{"dev", "", "v1.2", "v01.2.3", "1.2.x", "abc123"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
