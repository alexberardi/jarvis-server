package nodesettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

// Files in the agent's config dir, next to node.json. Both carry the node id they belong to: a
// new sign-in (another node id) starts without the previous node's key, secrets and data.
const (
	K2File       = "k2.json"
	SettingsFile = "node-settings.json"
)

// K2 is this node's settings key as the relay delivered it.
type K2 struct {
	NodeID    string `json:"node_id"`
	K2        string `json:"k2"` // base64url, 32 bytes
	Kid       string `json:"kid"`
	CreatedAt string `json:"created_at"`
}

// Store holds the node's K2 and its settings: command secrets (keyed by key, scope and, for
// scope "user", the user), the disabled commands and the commands' stored data. Everything is
// in two 0600 JSON files. Safe for concurrent use.
type Store struct {
	dir    string
	nodeID string

	mu  sync.Mutex
	k2  *K2
	key []byte
	s   settingsFile
}

type secret struct {
	Key       string `json:"key"`
	Scope     string `json:"scope"`
	UserID    *int64 `json:"user_id,omitempty"`
	Value     string `json:"value"`
	ValueType string `json:"value_type,omitempty"`
}

type record struct {
	Data      map[string]any `json:"data"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
}

type settingsFile struct {
	NodeID   string                       `json:"node_id"`
	Secrets  []secret                     `json:"secrets"`
	Disabled []string                     `json:"disabled_commands"`
	Data     map[string]map[string]record `json:"command_data"`
}

// OpenStore loads the store for nodeID from dir. Files written for another node (an earlier sign-in)
// are removed, so their key, secrets and data never reach the new node.
func OpenStore(dir, nodeID string) (*Store, error) {
	st := &Store{dir: dir, nodeID: nodeID, s: settingsFile{NodeID: nodeID}}
	var k K2
	switch err := readJSON(filepath.Join(dir, K2File), &k); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case k.NodeID != nodeID:
		if err := os.Remove(filepath.Join(dir, K2File)); err != nil {
			return nil, err
		}
	default:
		key, err := DecodeKey(k.K2)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", K2File, err)
		}
		st.k2, st.key = &k, key
	}
	var s settingsFile
	switch err := readJSON(filepath.Join(dir, SettingsFile), &s); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case s.NodeID != nodeID:
		if err := os.Remove(filepath.Join(dir, SettingsFile)); err != nil {
			return nil, err
		}
	default:
		st.s = s
	}
	return st, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeJSON writes 0600 in a 0700 dir, atomically (as node.SaveCreds).
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
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

// Key is K2, or nil when none was provisioned.
func (st *Store) Key() []byte {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.key
}

// HasK2 says whether K2 is stored (the heartbeat's needs_k2 is its negation).
func (st *Store) HasK2() bool { return st.Key() != nil }

// SaveK2 validates and stores K2 (replacing any earlier one, as save_k2 does).
func (st *Store) SaveK2(k2, kid, createdAt string) error {
	key, err := DecodeKey(k2)
	if err != nil {
		return err
	}
	k := K2{NodeID: st.nodeID, K2: k2, Kid: kid, CreatedAt: createdAt}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := writeJSON(filepath.Join(st.dir, K2File), k); err != nil {
		return err
	}
	st.k2, st.key = &k, key
	return nil
}

func (st *Store) saveLocked() error {
	st.s.NodeID = st.nodeID
	return writeJSON(filepath.Join(st.dir, SettingsFile), st.s)
}

func sameUser(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// userFor keeps a user id only for scope "user", as the Python node's secret service does.
func userFor(scope string, uid *int64) *int64 {
	if scope != "user" {
		return nil
	}
	return uid
}

func (st *Store) findLocked(key, scope string, uid *int64) int {
	for i, s := range st.s.Secrets {
		if s.Key == key && s.Scope == scope && sameUser(s.UserID, uid) {
			return i
		}
	}
	return -1
}

// Secret is a stored secret's value ("" when unset).
func (st *Store) Secret(key, scope string, uid *int64) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if i := st.findLocked(key, scope, userFor(scope, uid)); i >= 0 {
		return st.s.Secrets[i].Value
	}
	return ""
}

// ScopeOf is the scope a key is already stored under without a user, as get_secret_scope.
func (st *Store) ScopeOf(key string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, s := range st.s.Secrets {
		if s.Key == key && s.UserID == nil {
			return s.Scope
		}
	}
	return ""
}

// SetSecret stores a value; "" deletes it (mobile clears a secret by pushing "").
func (st *Store) SetSecret(key, value, scope, valueType string, uid *int64) error {
	uid = userFor(scope, uid)
	st.mu.Lock()
	defer st.mu.Unlock()
	i := st.findLocked(key, scope, uid)
	switch {
	case value == "" && i < 0:
		return nil
	case value == "":
		st.s.Secrets = append(st.s.Secrets[:i], st.s.Secrets[i+1:]...)
	case i >= 0:
		st.s.Secrets[i].Value = value
		if valueType != "" {
			st.s.Secrets[i].ValueType = valueType
		}
	default:
		st.s.Secrets = append(st.s.Secrets, secret{Key: key, Scope: scope, UserID: uid, Value: value, ValueType: valueType})
	}
	return st.saveLocked()
}

// DeleteSecret removes a secret.
func (st *Store) DeleteSecret(key, scope string, uid *int64) error {
	return st.SetSecret(key, "", scope, "", uid)
}

// Enabled says whether a command is offered (the command_registry config push).
func (st *Store) Enabled(command string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.s.Disabled {
		if c == command {
			return false
		}
	}
	return true
}

// SetEnabled turns a command on or off.
func (st *Store) SetEnabled(command string, on bool) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := st.s.Disabled[:0:0]
	for _, c := range st.s.Disabled {
		if c != command {
			out = append(out, c)
		}
	}
	if !on {
		out = append(out, command)
	}
	st.s.Disabled = out
	return st.saveLocked()
}

// --- command data (JarvisStorage's save/get/get_all/delete/delete_all) ---

func live(r record, now time.Time) bool { return r.ExpiresAt == nil || r.ExpiresAt.After(now) }

// SaveData upserts a record.
func (st *Store) SaveData(command, key string, data map[string]any, expires *time.Time) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.s.Data == nil {
		st.s.Data = map[string]map[string]record{}
	}
	if st.s.Data[command] == nil {
		st.s.Data[command] = map[string]record{}
	}
	st.s.Data[command][key] = record{Data: data, ExpiresAt: expires}
	return st.saveLocked()
}

// Data is a live record, or nil.
func (st *Store) Data(command, key string, now time.Time) map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	if r, ok := st.s.Data[command][key]; ok && live(r, now) {
		return r.Data
	}
	return nil
}

// AllData is every live record of a command, ordered by key.
func (st *Store) AllData(command string, now time.Time) []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	keys := make([]string, 0, len(st.s.Data[command]))
	for k := range st.s.Data[command] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := []map[string]any{}
	for _, k := range keys {
		if r := st.s.Data[command][k]; live(r, now) {
			out = append(out, r.Data)
		}
	}
	return out
}

// DeleteData removes one record and says whether it existed.
func (st *Store) DeleteData(command, key string) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.s.Data[command][key]; !ok {
		return false, nil
	}
	delete(st.s.Data[command], key)
	return true, st.saveLocked()
}

// DeleteAllData removes a command's records and returns how many there were.
func (st *Store) DeleteAllData(command string) (int, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := len(st.s.Data[command])
	if n == 0 {
		return 0, nil
	}
	delete(st.s.Data, command)
	return n, st.saveLocked()
}
