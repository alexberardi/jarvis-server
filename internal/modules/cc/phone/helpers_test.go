package phone

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- fakes (no real telephony provider, no real models) ---

type fakeRoles struct{ members map[string]map[int64]bool }

func (f *fakeRoles) HouseholdRole(_ context.Context, uid int64, hh string) (authn.Role, bool, error) {
	if f.members[hh][uid] {
		return authn.RoleMember, true, nil
	}
	return "", false, nil
}

type sentCard struct {
	notifications.NewInboxItem
	push notifications.Notification
}

type fakeNotify struct {
	mu    sync.Mutex
	cards []sentCard
}

func (f *fakeNotify) CreateInboxItem(_ context.Context, _ *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cards = append(f.cards, sentCard{NewInboxItem: in})
	return notifications.InboxItem{ID: fmt.Sprintf("item-%d", len(f.cards))}, nil
}

func (f *fakeNotify) Notify(_ context.Context, _ *sql.Tx, _ string, n notifications.Notification) (notifications.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cards[len(f.cards)-1].push = n
	return notifications.Delivery{}, nil
}

func (f *fakeNotify) all() []sentCard {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentCard(nil), f.cards...)
}

// titled returns the cards whose title starts with prefix.
func (f *fakeNotify) titled(prefix string) []sentCard {
	var out []sentCard
	for _, c := range f.all() {
		if strings.HasPrefix(c.Title, prefix) {
			out = append(out, c)
		}
	}
	return out
}

type fakeLLM struct {
	mu        sync.Mutex
	draft     string
	draftErr  error
	verdict   string
	assess    string
	replies   []string // live stream replies, in order
	streams   [][]llm.Message
	drafts    []llm.ChatRequest
	classifys int
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sys := ""
	if len(req.Messages) > 0 && req.Messages[0].Content != nil && req.Messages[0].Content.Text != nil {
		sys = *req.Messages[0].Content.Text
	}
	switch {
	case sys == draftSystemPrompt:
		f.drafts = append(f.drafts, req)
		if f.draftErr != nil {
			return nil, f.draftErr
		}
		return &llm.ChatResponse{Content: f.draft}, nil
	case req.Label == "live":
		f.classifys++
		return &llm.ChatResponse{Content: f.verdict}, nil
	default:
		return &llm.ChatResponse{Content: f.assess}, nil
	}
}

func (f *fakeLLM) Stream(_ context.Context, req llm.ChatRequest) (<-chan llm.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams = append(f.streams, req.Messages)
	if len(f.replies) == 0 {
		return nil, errors.New("no scripted reply")
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	ch := make(chan llm.Frame, len(reply)+2)
	for i := 0; i < len(reply); i += 4 {
		end := min(i+4, len(reply))
		ch <- llm.Frame{Delta: reply[i:end]}
	}
	ch <- llm.Frame{Done: true, Content: reply}
	close(ch)
	return ch, nil
}

type fakeSTT struct {
	mu    sync.Mutex
	heard []string
}

func (f *fakeSTT) Transcribe(_ context.Context, _ []byte, _ stt.TranscribeOptions) (stt.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.heard) == 0 {
		return stt.Result{}, nil
	}
	t := f.heard[0]
	f.heard = f.heard[1:]
	return stt.Result{Text: t}, nil
}

// fakeTTS renders 10 ms of a tone per character at 16 kHz (resampled to 8 kHz by the call).
type fakeTTS struct {
	mu    sync.Mutex
	texts []string
	fail  bool
}

func (f *fakeTTS) SynthesizePCM(_ context.Context, text string) ([]int16, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, 0, errors.New("tts down")
	}
	f.texts = append(f.texts, text)
	pcm := make([]int16, 160*len([]rune(text)))
	for i := range pcm {
		if i%2 == 0 {
			pcm[i] = 2000
		} else {
			pcm[i] = -2000
		}
	}
	return pcm, 16000, nil
}

func (f *fakeTTS) spoken() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

type startedCall struct{ to, twiml string }

type fakeProvider struct {
	mu       sync.Mutex
	started  chan startedCall
	ended    []string
	lineType string
	failDial bool
}

func newFakeProvider() *fakeProvider { return &fakeProvider{started: make(chan startedCall, 4)} }

func (f *fakeProvider) StartCall(_ context.Context, to, twiml string) (string, error) {
	if f.failDial {
		return "", errors.New("account suspended")
	}
	f.started <- startedCall{to, twiml}
	return "CA1", nil
}

func (f *fakeProvider) EndCall(_ context.Context, sid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, sid)
	return nil
}

func (f *fakeProvider) endedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ended...)
}

func (f *fakeProvider) LineType(context.Context, string) string { return f.lineType }

type fakeSearch struct {
	results []servertools.SearchResult
	queries []string
}

func (f *fakeSearch) Search(_ context.Context, q string, _ int) ([]servertools.SearchResult, error) {
	f.queries = append(f.queries, q)
	return f.results, nil
}

type fakeErrands struct {
	mu    sync.Mutex
	snaps []CallSnapshot
}

func (f *fakeErrands) CallTerminal(_ context.Context, s CallSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snaps = append(f.snaps, s)
}

// --- environment ---

const (
	hh        = "hh-1"
	otherHH   = "hh-2"
	authToken = "test-signing-key"
	publicURL = "https://phone.example"
)

type env struct {
	t        *testing.T
	s        *Service
	d        *db.DB
	notify   *fakeNotify
	llm      *fakeLLM
	stt      *fakeSTT
	tts      *fakeTTS
	provider *fakeProvider
	search   *fakeSearch
	errands  *fakeErrands
	srv      *httptest.Server
	clock    time.Time
	cmu      sync.Mutex
}

func (e *env) now() time.Time {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	return e.clock
}

func (e *env) advance(d time.Duration) {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	e.clock = e.clock.Add(d)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "cc", os.DirFS("../migrations")); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(logOut(), nil))
	defs := append(Definitions(),
		settings.Definition{Key: settingWebSearch, Category: "web_search", Type: settings.Bool, Default: false},
		settings.Definition{Key: settingLocation, Category: "household", Type: settings.String, Default: ""})
	svc, err := settings.New(d, "cc", defs, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, d: d, notify: &fakeNotify{}, llm: &fakeLLM{draft: "Order a large cheese pizza for pickup."},
		stt: &fakeSTT{}, tts: &fakeTTS{}, provider: newFakeProvider(), search: &fakeSearch{}, errands: &fakeErrands{},
		clock: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	e.s = &Service{
		DB: d, Log: log, Settings: svc,
		Roles: &fakeRoles{members: map[string]map[int64]bool{hh: {1: true, 2: true}, otherHH: {9: true}}},
		LLM:   e.llm, STT: e.stt, TTS: e.tts, Notify: e.notify, Provider: e.provider, Search: e.search,
		Names: names{1: "Alex"}, Errands: e.errands, Options: Options{PublicURL: publicURL, AuthToken: authToken},
		Now: e.now, HeartbeatInterval: time.Hour, EscalationWindow: 2 * time.Second,
	}
	sctx, cancel := context.WithCancel(ctx)
	e.s.Start(sctx)
	t.Cleanup(func() { cancel(); e.s.Wait() })
	mux := http.NewServeMux()
	e.s.Mount(mux, func(h UserHandler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var uid int64
			if _, err := fmt.Sscanf(r.Header.Get("Authorization"), "Bearer tok-%d", &uid); err != nil {
				http.Error(w, `{"detail":"Missing or invalid Authorization header"}`, http.StatusUnauthorized)
				return
			}
			h(w, r, authn.User{ID: uid})
		}
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

// logOut shows the service log with PHONE_TEST_LOG=1.
func logOut() io.Writer {
	if os.Getenv("PHONE_TEST_LOG") != "" {
		return os.Stderr
	}
	return io.Discard
}

type names map[int64]string

func (n names) UserNames(_ context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		if v, ok := n[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (e *env) set(key string, v any, sc settings.Scope) {
	e.t.Helper()
	if err := e.s.Settings.Set(context.Background(), key, v, sc); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) enable() { e.set(SettingEnabled, true, settings.Scope{HouseholdID: hh}) }

type resp struct {
	status int
	body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("not JSON (%d): %s", r.status, r.body)
	}
	return v
}

func (e *env) do(method, path string, uid int64, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if uid != 0 {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer tok-%d", uid))
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, b}
}

func (e *env) session(id string) *Session {
	e.t.Helper()
	s, err := e.s.session(context.Background(), e.d.Read, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) addContact(name, number string, dnc bool) string {
	e.t.Helper()
	id := uuid4()
	now := dbTime(e.now())
	if _, err := e.d.Write.Exec(`INSERT INTO cc_phone_contacts (id, household_id, name, normalized_name, number,
		source, do_not_call, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'manual', ?, ?, ?)`,
		id, hh, name, NormalizeName(name), number, boolInt(dnc), now, now); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func uidPtr(v int64) *int64 { return &v }

// waitFor polls cond until it holds or 5 s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
