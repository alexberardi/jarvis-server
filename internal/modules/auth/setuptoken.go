package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The first-superuser setup token (admin AD2). While no superuser exists, POST /auth/setup
// needs a one-time token that only someone with access to the host can read, so whoever
// reaches the LAN port first doesn't own the install. jarvisd writes it to
// <home>/setup-token (0600) on start, keeps only its hash in memory, and deletes the file
// once setup succeeds. After that, /auth/setup is the legacy 409 and the token is ignored.

// SetupTokenFile is the token's file name under the data directory.
const SetupTokenFile = "setup-token"

// SetupTokenHeader carries the token; a `setup_token` body field works too.
const SetupTokenHeader = "X-Jarvis-Setup-Token"

// SetupTokenPath is where the token lives for a data directory.
func SetupTokenPath(home string) string { return filepath.Join(home, SetupTokenFile) }

// prepareSetupToken runs at Start. With a superuser it removes a stale token file; without
// one it reuses the file's token (so a printed link survives a restart) or writes a new one,
// and announces it.
func (m *Module) prepareSetupToken(ctx context.Context) error {
	n, err := count(ctx, m.deps.DB.Read, `SELECT COUNT(*) FROM auth_users WHERE is_superuser = 1`)
	if err != nil {
		return err
	}
	path := ""
	if m.deps.Config.Home != "" {
		path = SetupTokenPath(m.deps.Config.Home)
	}
	if n > 0 {
		if path != "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				m.deps.Log.Warn("auth: could not remove the stale setup token", "path", path, "err", err)
			}
		}
		return nil
	}
	tok := ""
	if path != "" {
		if b, err := os.ReadFile(path); err == nil && validSetupToken(strings.TrimSpace(string(b))) {
			tok = strings.TrimSpace(string(b))
		}
	}
	if tok == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		tok = base64.RawURLEncoding.EncodeToString(raw)
		if path != "" {
			if err := writeSetupToken(path, tok); err != nil {
				return err
			}
		}
	}
	// Re-assert the mode in case an existing file was loosened.
	if path != "" {
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	sum := sha256.Sum256([]byte(tok))
	m.setupMu.Lock()
	m.setupHash = sum[:]
	m.setupMu.Unlock()
	m.deps.Log.Info("auth: no superuser yet; first-run setup needs the setup token", "file", path)
	if m.OnSetupToken != nil {
		m.OnSetupToken(tok, path)
	}
	return nil
}

func validSetupToken(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

// writeSetupToken creates the file owner-only from the start (no window where it is readable).
func writeSetupToken(path, tok string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// checkSetupToken is called inside the setup transaction, after it found no superuser.
// It compares in constant time against the hash; with no token prepared it fails closed.
func (m *Module) checkSetupToken(tok string) error {
	if tok == "" {
		return fail(http.StatusUnauthorized, "Setup token required")
	}
	sum := sha256.Sum256([]byte(tok))
	m.setupMu.Lock()
	want := m.setupHash
	m.setupMu.Unlock()
	if want == nil || subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return fail(http.StatusForbidden, "Invalid setup token")
	}
	return nil
}

// consumeSetupToken forgets the token and deletes its file once a superuser exists.
func (m *Module) consumeSetupToken() {
	m.setupMu.Lock()
	m.setupHash = nil
	m.setupMu.Unlock()
	if m.deps.Config.Home == "" {
		return
	}
	path := SetupTokenPath(m.deps.Config.Home)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.deps.Log.Warn("auth: could not remove the used setup token", "path", path, "err", err)
	}
}
