package bluetooth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record is a device the node paired, as N: keeps it (JarvisStorage("bluetooth")).
type Record struct {
	MAC           string    `json:"mac_address"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`         // source: the node plays to it; sink: it plays to the node
	AutoConnect   bool      `json:"auto_connect"` // the reconnect loop connects it
	LastConnected time.Time `json:"last_connected"`
}

// StoreFile is the store's file name, in the agent's config dir next to agent.json.
const StoreFile = "bluetooth.json"

// Store is the saved devices, in a JSON file (0600).
type Store struct {
	path string
	mu   sync.Mutex
	recs map[string]Record
}

// OpenStore loads the store at path; a missing file is an empty store.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, recs: map[string]Record{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Record
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		if m := normMAC(r.MAC); m != "" {
			r.MAC = m
			s.recs[m] = r
		}
	}
	return s, nil
}

// All is the records, by address.
func (s *Store) All() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	return out
}

// Get is mac's record.
func (s *Store) Get(mac string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[mac]
	return r, ok
}

// Paired records a pairing. A device seen before keeps its auto_connect (the user's switch
// survives a re-pair); a new one reconnects, as consumer Bluetooth does.
func (s *Store) Paired(mac, name, role string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	auto := true
	if old, ok := s.recs[mac]; ok {
		auto = old.AutoConnect
	}
	s.recs[mac] = Record{MAC: mac, Name: name, Role: role, AutoConnect: auto, LastConnected: at.UTC()}
	return s.save()
}

// SetAutoConnect sets mac's auto_connect; found is false (and nothing changes) when the node
// has no record of it.
func (s *Store) SetAutoConnect(mac string, on bool) (found bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[mac]
	if !ok {
		return false, nil
	}
	r.AutoConnect = on
	s.recs[mac] = r
	return true, s.save()
}

// Delete drops mac's record.
func (s *Store) Delete(mac string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.recs[mac]; !ok {
		return nil
	}
	delete(s.recs, mac)
	return s.save()
}

func (s *Store) save() error {
	list := make([]Record, 0, len(s.recs))
	for _, r := range s.recs {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MAC < list[j].MAC })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bluetooth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
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
	return os.Rename(tmp.Name(), s.path)
}
