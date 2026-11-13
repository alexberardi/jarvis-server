package discord

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// ConfigSource says which households run a bot and how.
type ConfigSource interface {
	Configs(ctx context.Context) ([]Config, error)
}

// Connector opens a household's bot (Connect in production, a fake in tests).
type Connector func(ctx context.Context, cfg Config, b *Bridge, log *slog.Logger) (io.Closer, error)

// Manager keeps one bot running per configured household, starting, restarting and stopping them
// as the settings change.
type Manager struct {
	Source  ConfigSource
	Store   *Store
	Codes   *Codes
	Chat    ChatFunc
	STT     Transcribe
	TTS     Speak
	Shrink  func(raw []byte) ([]byte, bool)
	HearWAV func(ctx context.Context, wav []byte) (string, error)
	Synth   func(ctx context.Context, text string) ([][]byte, error)
	Nodes   func(ctx context.Context, hh string) ([]NodeInfo, error)
	Connect Connector
	Log     *slog.Logger
	Every   time.Duration // how often to look at the settings (default 30 seconds)

	// recMu makes a whole Reconcile one step, so two overlapping calls cannot undo each other.
	recMu sync.Mutex
	mu    sync.Mutex
	runs  map[string]*run
}

type run struct {
	cfg    Config
	bridge *Bridge
	closer io.Closer
}

// SetSource and SetConnector swap the manager's collaborators while it runs (tests; production
// sets them once before Start). The settings loop never sees a half-written value.
func (m *Manager) SetSource(s ConfigSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Source = s
}

func (m *Manager) SetConnector(c Connector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Connect = c
}

// connect is called with m.mu held.
func (m *Manager) connect() Connector {
	if m.Connect != nil {
		return m.Connect
	}
	return func(ctx context.Context, cfg Config, b *Bridge, log *slog.Logger) (io.Closer, error) {
		g, err := Connect(ctx, cfg, b, log)
		if err != nil {
			return nil, err
		}
		return closerFunc(g.Close), nil
	}
}

type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }

// Start runs Reconcile now and then on a timer until ctx ends, then stops every bot.
func (m *Manager) Start(ctx context.Context) {
	every := m.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	go func() {
		m.Reconcile(ctx)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				m.stopAll()
				return
			case <-t.C:
				m.Reconcile(ctx)
			}
		}
	}()
}

// Reconcile brings the running bots in line with the settings.
func (m *Manager) Reconcile(ctx context.Context) {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	m.mu.Lock()
	src := m.Source
	m.mu.Unlock()
	cfgs, err := src.Configs(ctx)
	if err != nil {
		m.Log.Warn("discord: could not read the settings", "err", err)
		return
	}
	want := map[string]Config{}
	for _, c := range cfgs {
		want[c.HouseholdID] = c
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runs == nil {
		m.runs = map[string]*run{}
	}
	for hh, r := range m.runs {
		if c, ok := want[hh]; !ok || !c.same(r.cfg) {
			_ = r.closer.Close()
			delete(m.runs, hh)
			m.Log.Info("discord: bot stopped", "household", hh)
		}
	}
	for hh, c := range want {
		if _, running := m.runs[hh]; running {
			continue
		}
		b := &Bridge{HouseholdID: hh, ChannelID: c.ChannelID, VoiceChannelID: c.VoiceChannelID, Store: m.Store, Codes: m.Codes,
			Chat: m.Chat, STT: m.STT, TTS: m.TTS, HearWAV: m.HearWAV, Synth: m.Synth, Shrink: m.Shrink, Fetch: FetchAttachment, Nodes: m.Nodes, Log: m.Log}
		closer, err := m.connect()(ctx, c, b, m.Log)
		if err != nil {
			// The token is never logged; Discord's error says what was wrong.
			m.Log.Warn("discord: bot did not start", "household", hh, "err", err)
			continue
		}
		m.runs[hh] = &run{cfg: c, bridge: b, closer: closer}
		m.Log.Info("discord: bot started", "household", hh)
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for hh, r := range m.runs {
		_ = r.closer.Close()
		delete(m.runs, hh)
	}
}

// Online lists the households whose bot is running.
func (m *Manager) Online() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for hh := range m.runs {
		out = append(out, hh)
	}
	sort.Strings(out)
	return out
}

// IsOnline reports whether a household's bot is connected.
func (m *Manager) IsOnline(hh string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.runs[hh]
	return ok
}

// Notify sends a reminder or alarm to the user's Discord DMs through every household bot where
// they opted in. It reports whether anything was sent.
func (m *Manager) Notify(ctx context.Context, userID int64, text string) bool {
	m.mu.Lock()
	var bridges []*Bridge
	for _, r := range m.runs {
		bridges = append(bridges, r.bridge)
	}
	m.mu.Unlock()
	sent := false
	for _, b := range bridges {
		ok, err := b.Notify(ctx, userID, text)
		if err != nil {
			m.Log.Warn("discord: reminder not delivered", "err", err)
		}
		sent = sent || ok
	}
	return sent
}
