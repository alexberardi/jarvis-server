package auth

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// jarvisd's own app client (installers §3.1, I3). Job-completion callbacks (OCR, LLM) are
// POSTed with jarvisd's app credentials so the receiver (jarvis-recipes-server's
// /internal/ocr/callback) can authenticate them with /internal/app-ping. The legacy stack got
// those from JARVIS_APP_ID/JARVIS_APP_KEY, which a fresh jarvisd install never sets, so its
// callbacks went out unsigned and were refused. jarvisd now mints the client itself: an
// auth_app_clients row "jarvisd" whose key (only its hash is in the DB, like every client)
// lives in <home>/app-key, owner-only, next to the database it unlocks.

// SelfAppID is the app id jarvisd signs its own outbound calls with.
const SelfAppID = "jarvisd"

// SelfAppKeyFile is the key's file name under the data directory.
const SelfAppKeyFile = "app-key"

// SelfAppKeyPath is where the key lives for a data directory.
func SelfAppKeyPath(home string) string { return filepath.Join(home, SelfAppKeyFile) }

// SelfAppCreds returns jarvisd's own app credentials, creating the client on first use. A key
// file that no longer validates (deleted row, revoked, a restored database) is replaced by a
// rotation, which also reactivates the client. Safe for concurrent use; cached once valid.
func (m *Module) SelfAppCreds(ctx context.Context) (id, key string, err error) {
	m.selfMu.Lock()
	defer m.selfMu.Unlock()
	if m.selfKey != "" {
		return SelfAppID, m.selfKey, nil
	}
	if m.deps.Config.Home == "" {
		return "", "", errors.New("auth: no data directory for jarvisd's app key")
	}
	path := SelfAppKeyPath(m.deps.Config.Home)
	if b, err := os.ReadFile(path); err == nil {
		k := strings.TrimSpace(string(b))
		if _, ok, err := m.ValidateApp(ctx, SelfAppID, k); err != nil {
			return "", "", err
		} else if ok {
			m.selfKey = k
			return SelfAppID, k, nil
		}
		m.deps.Log.Warn("auth: jarvisd's app key no longer validates; issuing a new one", "path", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", fmt.Errorf("auth: read %s: %w", path, err)
	}

	_, k, err := m.CreateAppClient(ctx, SelfAppID, "jarvisd (job callbacks)")
	if errors.Is(err, ErrAppExists) {
		k, _, err = m.rotateAppClient(ctx, SelfAppID) // selfMu is held: not the public one
	}
	if err != nil {
		return "", "", fmt.Errorf("auth: issue jarvisd's app client: %w", err)
	}
	if err := writeSetupToken(path, k); err != nil { // same owner-only atomic write
		return "", "", fmt.Errorf("auth: write %s: %w", path, err)
	}
	m.deps.Log.Info("auth: issued jarvisd's own app client", "app_id", SelfAppID, "key_file", path)
	m.selfKey = k
	return SelfAppID, k, nil
}

// ForgetSelfAppKey drops the cached key (after a rotate or revoke of the "jarvisd" client),
// so the next SelfAppCreds re-reads and, if needed, reissues it.
func (m *Module) ForgetSelfAppKey() {
	m.selfMu.Lock()
	m.selfKey = ""
	m.selfMu.Unlock()
}
