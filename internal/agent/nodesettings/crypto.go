// Package nodesettings makes the desktop agent a full participant in the node settings
// protocol a Pi speaks (docs/agent/DESIGN.md §16, docs/cc/05-nodes.md §3.2–3.3): it receives
// K2 through command-center's relay, answers settings requests with a K2-encrypted snapshot,
// and applies K2-encrypted config pushes (command secrets, command enable/disable). Command-
// center only ever relays ciphertext; the mobile app and this agent hold K2.
//
// The wire crypto is byte-compatible with the Python node (services/settings_snapshot_service.py
// encrypt_snapshot, services/config_push_service.py _decrypt_config) and the mobile app
// (src/services/configPushService.ts, settingsDecryptService.ts): AES-256-GCM, a 12-byte nonce,
// AAD "{node_id}:{config_type}" ("settings:snapshot" for snapshots), the raw JSON as plaintext,
// and ciphertext, nonce and tag as unpadded base64url. testdata/k2_golden.json, written by the
// real Python functions (tools/golden/export_k2.py), pins it.
package nodesettings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// SnapshotConfigType is the AAD config type of a settings snapshot.
const SnapshotConfigType = "settings:snapshot"

const (
	keyLen   = 32
	nonceLen = 12
	tagLen   = 16
)

// Sealed is one AES-256-GCM message on the wire: unpadded base64url fields.
type Sealed struct {
	Ciphertext string `json:"ciphertext"`
	Nonce      string `json:"nonce"`
	Tag        string `json:"tag"`
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// unb64 decodes base64url with or without padding (mobile sends it unpadded, Python tolerates
// both).
func unb64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// DecodeKey decodes a base64url K2 and insists on 32 bytes (AES-256), as save_k2 does.
func DecodeKey(s string) ([]byte, error) {
	k, err := unb64(s)
	if err != nil {
		return nil, fmt.Errorf("invalid base64url encoding: %w", err)
	}
	if len(k) != keyLen {
		return nil, fmt.Errorf("K2 must be exactly 32 bytes, got %d", len(k))
	}
	return k, nil
}

func aad(nodeID, configType string) []byte { return []byte(nodeID + ":" + configType) }

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("K2 must be exactly 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext for (nodeID, configType). A nil nonce draws a fresh random one; tests
// pass a fixed one to compare bytes with the Python node.
func Seal(key []byte, nodeID, configType string, plaintext, nonce []byte) (Sealed, error) {
	g, err := gcm(key)
	if err != nil {
		return Sealed{}, err
	}
	if nonce == nil {
		nonce = make([]byte, nonceLen)
		if _, err := rand.Read(nonce); err != nil {
			return Sealed{}, err
		}
	}
	if len(nonce) != nonceLen {
		return Sealed{}, fmt.Errorf("nonce must be %d bytes", nonceLen)
	}
	ct := g.Seal(nil, nonce, plaintext, aad(nodeID, configType))
	return Sealed{Ciphertext: b64(ct[:len(ct)-tagLen]), Nonce: b64(nonce), Tag: b64(ct[len(ct)-tagLen:])}, nil
}

// ErrDecrypt: the message isn't for this node and type, or K2 differs from the sender's.
var ErrDecrypt = errors.New("decryption failed: wrong K2, node or config type, or a tampered message")

// Open decrypts a message sealed for (nodeID, configType).
func Open(key []byte, nodeID, configType string, s Sealed) ([]byte, error) {
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	ct, err1 := unb64(s.Ciphertext)
	nonce, err2 := unb64(s.Nonce)
	tag, err3 := unb64(s.Tag)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, fmt.Errorf("bad base64url: %w", err)
	}
	if len(nonce) != nonceLen || len(tag) != tagLen {
		return nil, ErrDecrypt
	}
	pt, err := g.Open(nil, nonce, append(ct, tag...), aad(nodeID, configType))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
