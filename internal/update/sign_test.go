package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"

	"golang.org/x/crypto/blake2b"
)

// signMinisign writes a .minisig for msg (tests only; releases are signed by the minisign CLI
// in the release workflow).
func signMinisign(priv ed25519.PrivateKey, id [8]byte, msg []byte, trusted string, prehash bool) []byte {
	alg, signed := algLegacy, msg
	if prehash {
		h := blake2b.Sum512(msg)
		alg, signed = algPrehashed, h[:]
	}
	sig := ed25519.Sign(priv, signed)
	global := ed25519.Sign(priv, append(bytes.Clone(sig), trusted...))
	line := append(append(append([]byte{}, alg[:]...), id[:]...), sig...)
	var b bytes.Buffer
	b.WriteString("untrusted comment: test signature\n")
	b.WriteString(base64.StdEncoding.EncodeToString(line) + "\n")
	b.WriteString("trusted comment: " + trusted + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
	return b.Bytes()
}
