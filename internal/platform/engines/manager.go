package engines

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
)

// Manager holds jarvisd's engines by name.
type Manager struct {
	log  *slog.Logger
	mu   sync.Mutex
	sups map[string]*Supervisor
}

// NewManager creates an empty manager.
func NewManager(log *slog.Logger) *Manager {
	return &Manager{log: log, sups: map[string]*Supervisor{}}
}

// Add registers an engine without starting it. Names are unique.
func (m *Manager) Add(spec Spec) (*Supervisor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.sups[spec.Name]; dup {
		return nil, fmt.Errorf("engines: duplicate engine %q", spec.Name)
	}
	s, err := New(spec, m.log)
	if err != nil {
		return nil, err
	}
	m.sups[spec.Name] = s
	return s, nil
}

// Get returns the named engine, or nil.
func (m *Manager) Get(name string) *Supervisor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sups[name]
}

func (m *Manager) all() []*Supervisor {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Supervisor, 0, len(m.sups))
	for _, s := range m.sups {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *Supervisor) int { return cmp.Compare(a.Name(), b.Name()) })
	return out
}

// StartAll starts every engine that isn't running. One failing to launch doesn't stop the rest.
func (m *Manager) StartAll() error {
	var errs []error
	for _, s := range m.all() {
		s.mu.Lock()
		running := s.cur != nil
		s.mu.Unlock()
		if running {
			continue
		}
		errs = append(errs, s.Start())
	}
	return errors.Join(errs...)
}

// StopAll stops every engine concurrently.
func (m *Manager) StopAll(ctx context.Context) error {
	sups := m.all()
	errs := make([]error, len(sups))
	var wg sync.WaitGroup
	for i, s := range sups {
		wg.Go(func() { errs[i] = s.Stop(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Status reports every engine, sorted by name.
func (m *Manager) Status() []Status {
	sups := m.all()
	out := make([]Status, len(sups))
	for i, s := range sups {
		out[i] = s.Status()
	}
	return out
}
