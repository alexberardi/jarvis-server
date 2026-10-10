// Package node is the reusable core of a Jarvis node: enrollment, credentials, discovery, the
// jarvisd HTTP client, the MQTT command loop, heartbeat and tool dispatch. It knows nothing
// about desktops; the desktop agent plugs its tools in through Toolset. A future Go Pi node
// would reuse this package as is.
package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Creds is what enrollment produces and every run needs. NodeKey is a secret: it is the node's
// HTTP credential and its MQTT password. It is never logged or printed (see LogValue).
type Creds struct {
	ConfigURL  string    `json:"config_url"` // jarvisd's config service, the one bootstrap address
	NodeID     string    `json:"node_id"`
	NodeKey    string    `json:"node_key"`
	Room       string    `json:"room,omitempty"`
	Name       string    `json:"name,omitempty"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// LogValue keeps the key out of every slog line, whatever the caller logs.
func (c Creds) LogValue() slog.Value {
	return slog.GroupValue(slog.String("node_id", c.NodeID), slog.String("config_url", c.ConfigURL), slog.String("room", c.Room))
}

// String also redacts, for %v.
func (c Creds) String() string {
	return fmt.Sprintf("node %s at %s (room %q)", c.NodeID, c.ConfigURL, c.Room)
}

// APIKey is the X-API-Key header value.
func (c Creds) APIKey() string { return c.NodeID + ":" + c.NodeKey }

// DefaultCredsPath is <user config dir>/jarvis-agent/node.json (~/.config on Linux,
// ~/Library/Application Support on macOS, %AppData% on Windows).
func DefaultCredsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jarvis-agent", "node.json"), nil
}

// ErrNotEnrolled: no credentials file yet.
var ErrNotEnrolled = errors.New("this machine is not enrolled; run `jarvis-agent enroll` first")

// LoadCreds reads the credentials file, refusing one other users can read (on Unix).
func LoadCreds(path string) (Creds, error) {
	var c Creds
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotEnrolled
	}
	if err != nil {
		return c, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return c, fmt.Errorf("%s is readable by other users (mode %o); run chmod 600 on it", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.NodeID == "" || c.NodeKey == "" || c.ConfigURL == "" {
		return c, fmt.Errorf("%s is incomplete; re-enroll", path)
	}
	return c, nil
}

// SaveCreds writes the credentials 0600 in a 0700 directory, atomically. On Windows the file
// lands in the user's profile, whose ACL already excludes other users.
func SaveCreds(path string, c Creds) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".node-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
