package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// minisign (https://jedisct1.github.io/minisign/) verification: the project signs SHA256SUMS
// with the same key as the node and admin releases, and the *running* jarvisd checks the
// signature before it trusts any hash in that file (00-installers §4.1).
//
// Formats:
//
//	public key  base64("Ed" | key id (8) | ed25519 public key (32))
//	signature   untrusted comment: <text>
//	            base64(alg (2) | key id (8) | ed25519 signature (64))
//	            trusted comment: <text>
//	            base64(global signature (64))
//
// alg "Ed" signs the message itself (legacy); "ED" signs its BLAKE2b-512 hash (the default
// since minisign 0.8). The global signature covers signature || trusted comment, so the
// trusted comment can't be swapped.

var (
	algLegacy    = [2]byte{'E', 'd'}
	algPrehashed = [2]byte{'E', 'D'}
)

// ErrSignature is wrapped by every verification failure (as opposed to a malformed input).
var ErrSignature = errors.New("minisign: signature verification failed")

// PublicKey is a minisign public key.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// KeyID is the key id as minisign prints it (hex of the little-endian 64-bit id).
func (k PublicKey) KeyID() string { return fmt.Sprintf("%016X", binary.LittleEndian.Uint64(k.ID[:])) }

// ParsePublicKey reads a key in minisign's base64 form, or a whole .pub file (comment line
// then the key).
func ParsePublicKey(s string) (PublicKey, error) {
	s = strings.TrimSpace(s)
	if lines := strings.Split(s, "\n"); len(lines) > 1 {
		s = strings.TrimSpace(lines[len(lines)-1])
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return PublicKey{}, fmt.Errorf("minisign: public key: %w", err)
	}
	if len(raw) != 2+8+ed25519.PublicKeySize || !bytes.Equal(raw[:2], algLegacy[:]) {
		return PublicKey{}, errors.New("minisign: public key: not an Ed25519 minisign key")
	}
	var k PublicKey
	copy(k.ID[:], raw[2:10])
	k.Key = ed25519.PublicKey(bytes.Clone(raw[10:]))
	return k, nil
}

// Signature is a parsed .minisig file.
type Signature struct {
	Algorithm        [2]byte
	KeyID            [8]byte
	Signature        []byte
	UntrustedComment string
	TrustedComment   string
	GlobalSignature  []byte
}

// ParseSignature reads a .minisig file.
func ParseSignature(data []byte) (Signature, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 4 {
		return Signature{}, errors.New("minisign: signature: expected 4 lines")
	}
	var sig Signature
	var ok bool
	if sig.UntrustedComment, ok = strings.CutPrefix(lines[0], "untrusted comment: "); !ok {
		return Signature{}, errors.New("minisign: signature: missing untrusted comment")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(raw) != 2+8+ed25519.SignatureSize {
		return Signature{}, errors.New("minisign: signature: malformed signature line")
	}
	copy(sig.Algorithm[:], raw[:2])
	copy(sig.KeyID[:], raw[2:10])
	sig.Signature = raw[10:]
	if sig.Algorithm != algLegacy && sig.Algorithm != algPrehashed {
		return Signature{}, fmt.Errorf("minisign: signature: unknown algorithm %q", sig.Algorithm[:])
	}
	if sig.TrustedComment, ok = strings.CutPrefix(lines[2], "trusted comment: "); !ok {
		return Signature{}, errors.New("minisign: signature: missing trusted comment")
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return Signature{}, errors.New("minisign: signature: malformed global signature")
	}
	sig.GlobalSignature = global
	return sig, nil
}

// Verify checks sig over message with key, including the trusted comment, and returns the
// trusted comment.
func (k PublicKey) Verify(message []byte, sig Signature) (string, error) {
	if sig.KeyID != k.ID {
		return "", fmt.Errorf("%w: signed by key %016X, expected %s", ErrSignature,
			binary.LittleEndian.Uint64(sig.KeyID[:]), k.KeyID())
	}
	signed := message
	if sig.Algorithm == algPrehashed {
		h := blake2b.Sum512(message)
		signed = h[:]
	}
	if !ed25519.Verify(k.Key, signed, sig.Signature) {
		return "", fmt.Errorf("%w: the file does not match its signature", ErrSignature)
	}
	if !ed25519.Verify(k.Key, append(bytes.Clone(sig.Signature), sig.TrustedComment...), sig.GlobalSignature) {
		return "", fmt.Errorf("%w: the trusted comment does not match its signature", ErrSignature)
	}
	return sig.TrustedComment, nil
}

// VerifyFile parses a .minisig and verifies message against any of keys.
func VerifyFile(message, minisig []byte, keys ...PublicKey) (string, error) {
	sig, err := ParseSignature(minisig)
	if err != nil {
		return "", err
	}
	var errs []error
	for _, k := range keys {
		comment, err := k.Verify(message, sig)
		if err == nil {
			return comment, nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("%w: no trusted keys", ErrSignature)
	}
	return "", errors.Join(errs...)
}
