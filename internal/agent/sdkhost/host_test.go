package sdkhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/music"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// fakeCC is command-center's node API, recording each request.
type fakeCC struct {
	mu       sync.Mutex
	got      map[string]map[string]any
	keys     []string
	inboxOK  bool
	failNext bool
}

func (f *fakeCC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.got[r.URL.Path] = body
	f.keys = append(f.keys, r.Header.Get("X-API-Key"))
	if f.failNext {
		f.failNext = false
		http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/api/v0/node/llm/chat":
		_, _ = w.Write([]byte(`{"content":"a joke"}`))
	case "/api/v0/node/inbox-item":
		_ = json.NewEncoder(w).Encode(map[string]any{"sent": f.inboxOK})
	case "/api/v0/signals":
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	default:
		_, _ = w.Write([]byte(`{"echo":true}`))
	}
}

func ask(t *testing.T, h *Host, method string, params any) any {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := h.Answer(context.Background(), method, b)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return v
}

func TestCommandCenterBackendsGoOutAsTheNode(t *testing.T) {
	f := &fakeCC{got: map[string]map[string]any{}, inboxOK: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	h := &Host{}
	// Before the agent is connected: errors for llm/cc, tags for inbox/signals.
	if _, err := h.Answer(context.Background(), "llm.chat_text", json.RawMessage(`{"prompt":"x"}`)); !errors.Is(err, errNoCC) {
		t.Fatalf("llm before connect = %v", err)
	}
	if v := ask(t, h, "inbox.post", map[string]any{"title": "t"}); v != "no_cc_url" {
		t.Fatalf("inbox before connect = %v", v)
	}
	h.SetCC(node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k"}))

	if v := ask(t, h, "llm.chat_text", map[string]any{"prompt": "tell a joke"}); v != "a joke" {
		t.Fatalf("llm = %v", v)
	}
	msgs, _ := json.Marshal(f.got["/api/v0/node/llm/chat"])
	if string(msgs) != `{"messages":[{"content":"tell a joke","role":"system"}],"model":"live","temperature":0}` {
		t.Fatalf("llm body = %s", msgs)
	}
	if v := ask(t, h, "cc.post", map[string]any{"path": "api/v0/node/send-link", "json": map[string]any{"url": "u"}}); v.(map[string]any)["echo"] != true {
		t.Fatalf("cc.post = %v", v)
	}
	if f.got["/api/v0/node/send-link"]["url"] != "u" {
		t.Fatalf("cc.post body = %v", f.got)
	}
	// Only the node plugin API.
	for _, p := range []string{"/api/v0/admin/nodes", "/api/v0/node/../admin/x", "/api/v0/nodes/n1", "/api/v0/node/x?y=1"} {
		if _, err := h.Answer(context.Background(), "cc.post", json.RawMessage(`{"path":"`+p+`","json":{}}`)); err == nil {
			t.Errorf("cc.post %s was sent", p)
		}
	}
	v := ask(t, h, "inbox.post", map[string]any{"command_name": "export_shopping_list", "title": "Shopping", "body": "milk",
		"category": "general", "metadata": map[string]any{"n": 1}, "user_id": 7, "create_push_notification": true, "target_type": "user"})
	if v != "ok" {
		t.Fatalf("inbox = %v", v)
	}
	ib, _ := json.Marshal(f.got["/api/v0/node/inbox-item"])
	if string(ib) != `{"body":"milk","category":"general","create_push_notification":true,"metadata":{"n":1},"summary":"","target_type":"user","title":"Shopping","user_id":7}` {
		t.Fatalf("inbox body = %s", ib)
	}
	f.inboxOK = false
	if v := ask(t, h, "inbox.post", map[string]any{"title": "x"}); v != "http_error" {
		t.Fatalf("inbox sent=false = %v", v)
	}
	if v := ask(t, h, "inbox.post", map[string]any{"title": " "}); v != "invalid" {
		t.Fatalf("inbox empty title = %v", v)
	}
	if v := ask(t, h, "signals.emit", map[string]any{"payload": map[string]any{"signal": map[string]any{"kind": "k"}}}); v != "ok" {
		t.Fatalf("signal = %v", v)
	}
	f.failNext = true
	if v := ask(t, h, "signals.emit", map[string]any{"payload": map[string]any{}}); v != "http_error" {
		t.Fatalf("signal on a 500 = %v", v)
	}
	for _, k := range f.keys {
		if k != "n1:k" {
			t.Fatalf("a request without the node key: %q", k)
		}
	}
}

func TestTimerAndAlertMethods(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	ts := &timers.Service{Path: filepath.Join(t.TempDir(), "timers.json"), Now: func() time.Time { return now },
		AfterFunc: func(time.Duration, func()) func() bool { return func() bool { return true } }}
	ts.Start()
	q := &Alerts{Now: func() time.Time { return now }}
	h := &Host{Timers: ts, Alerts: q}
	id := ask(t, h, "timer.set", map[string]any{"seconds": 90, "label": "pasta"}).(string)
	if ask(t, h, "timer.find_by_label", map[string]any{"label": "PAST"}) != id || ask(t, h, "timer.find_by_label", map[string]any{"label": "x"}) != nil {
		t.Fatal("find_by_label")
	}
	if g := ask(t, h, "timer.get", map[string]any{"timer_id": id}).(map[string]any); g["remaining_seconds"] != 90 {
		t.Fatalf("get = %v", g)
	}
	if ask(t, h, "timer.get", map[string]any{"timer_id": "nope"}) != nil {
		t.Fatal("get of an unknown timer")
	}
	if l := ask(t, h, "timer.list_active", nil).([]map[string]any); len(l) != 1 {
		t.Fatalf("list = %v", l)
	}
	if ask(t, h, "timer.cancel", map[string]any{"timer_id": id}) != true || ask(t, h, "timer.clear_all", nil) != 0 {
		t.Fatal("cancel/clear")
	}
	if _, err := h.Answer(context.Background(), "timer.set", json.RawMessage(`{"seconds":0}`)); err == nil {
		t.Fatal("a 0 s timer")
	}

	q.Add(Alert{ID: "a", Title: "Storm", Priority: 2, CreatedAt: now, ExpiresAt: now.Add(time.Hour), Category: "weather"})
	q.Add(Alert{ID: "b", Title: "News", Priority: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	q.Add(Alert{ID: "c", Title: "storm ", Priority: 3, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}) // replaces a
	q.Add(Alert{ID: "d", Title: "STORM", Priority: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})  // a duplicate
	q.Add(Alert{ID: "e", Title: "Old", Priority: 3, CreatedAt: now, ExpiresAt: now})                   // already expired
	p := ask(t, h, "alerts.pending", nil).([]map[string]any)
	if len(p) != 2 || p[0]["id"] != "c" || p[1]["id"] != "b" || p[0]["created_at"] != "2026-10-18T09:00:00+00:00" {
		t.Fatalf("pending = %v", p)
	}
	if ask(t, h, "alerts.count", nil) != 2 || ask(t, h, "alerts.remove", map[string]any{"ids": []string{"c", "zz"}}) != 1 {
		t.Fatal("count/remove")
	}
	if f := ask(t, h, "alerts.flush", nil).([]map[string]any); len(f) != 1 || f[0]["id"] != "b" || ask(t, h, "alerts.count", nil) != 0 {
		t.Fatalf("flush = %v", f)
	}
}

func TestAlertsExpireAndCap(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	q := &Alerts{Now: func() time.Time { return now }}
	q.Add(Alert{ID: "soon", Title: "soon", Priority: 3, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	for i := 0; i < maxAlerts; i++ {
		q.Add(Alert{ID: fmt.Sprintf("n%02d", i), Title: fmt.Sprintf("n%02d", i), Priority: 2, CreatedAt: now.Add(time.Duration(i) * time.Second), ExpiresAt: now.Add(time.Hour)})
	}
	if q.Count() != maxAlerts {
		t.Fatalf("count = %d", q.Count())
	}
	// Over the cap the oldest of the lowest priority went; the high one stayed.
	if p := q.Pending(); p[0].ID != "soon" || p[1].ID != "n01" {
		t.Fatalf("after the cap: %s %s", p[0].ID, p[1].ID)
	}
	now = now.Add(2 * time.Minute)
	if p := q.Pending(); len(p) != maxAlerts-1 || p[0].ID == "soon" {
		t.Fatalf("after expiry: %d, %s", len(p), p[0].ID)
	}
}

// fakeMixer answers pactl/amixer from a table of failing commands and records every call.
type fakeMixer struct {
	mu    sync.Mutex
	calls []string
	fail  func(cmd string) bool
	out   map[string]string
}

func (m *fakeMixer) run(_ context.Context, name string, args ...string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := name + " " + strings.Join(args, " ")
	m.calls = append(m.calls, cmd)
	if m.fail != nil && m.fail(cmd) {
		return "", errors.New("exit status 1")
	}
	return m.out[cmd], nil
}

func (m *fakeMixer) take() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := strings.Join(m.calls, "\n")
	m.calls = nil
	return s
}

func TestVolumeThroughPulseAndAmixer(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMixer{out: map[string]string{
		"pactl list short sinks":               "0\tjarvis_output\tmodule-alsa-sink.c\ts16le 2ch 48000Hz\tIDLE\n1\tbluez_sink.AA_BB.a2dp_sink\tmodule-bluez5-device.c\ts16le\tIDLE\n",
		"pactl get-sink-volume @DEFAULT_SINK@": "Volume: front-left: 32768 /  50% / -18.00 dB,   front-right: 36045 /  55% / -15.5 dB\n",
	}}
	a := &Audio{Path: filepath.Join(dir, "volume.json"), Run: m.run}
	h := &Host{Audio: a}
	ctx := context.Background()

	// Nothing persisted yet: "volume up" counts from the mixer's level.
	if v := ask(t, h, "audio.adjust_volume_percent", map[string]any{"delta": 10}); v != 65 {
		t.Fatalf("adjust = %v", v)
	}
	if got := m.take(); got != "pactl get-sink-volume @DEFAULT_SINK@\npactl set-sink-volume @DEFAULT_SINK@ 65%\npactl list short sinks\npactl set-sink-volume bluez_sink.AA_BB.a2dp_sink 65%" {
		t.Fatalf("calls:\n%s", got)
	}
	if b, _ := os.ReadFile(a.Path); string(b) != `{"volume_percent":65,"is_muted":false}` {
		t.Fatalf("persisted %s", b)
	}
	// Then from the persisted level, clamped.
	if v := ask(t, h, "audio.adjust_volume_percent", map[string]any{"delta": 50}); v != 100 {
		t.Fatalf("adjust = %v", v)
	}
	m.take()
	// The default sink failing: the HAT's sink; pactl failing: amixer.
	m.fail = func(c string) bool { return strings.Contains(c, "@DEFAULT_SINK@") }
	if ask(t, h, "audio.set_volume_percent", map[string]any{"percent": 30}) != true {
		t.Fatal("set on jarvis_output")
	}
	if got := m.take(); !strings.Contains(got, "pactl set-sink-volume jarvis_output 30%") || strings.Contains(got, "amixer") {
		t.Fatalf("calls:\n%s", got)
	}
	m.fail = func(c string) bool { return strings.HasPrefix(c, "pactl") }
	if ask(t, h, "audio.set_muted", map[string]any{"muted": true}) != true {
		t.Fatal("mute through amixer")
	}
	if got := m.take(); !strings.Contains(got, "amixer -q sset Master mute") {
		t.Fatalf("calls:\n%s", got)
	}
	// No mixer at all: persisted only, and false.
	m.fail = func(string) bool { return true }
	if ask(t, h, "audio.set_volume_percent", map[string]any{"percent": 20}) != false {
		t.Fatal("set with no mixer")
	}
	if b, _ := os.ReadFile(a.Path); string(b) != `{"volume_percent":20,"is_muted":true}` {
		t.Fatalf("persisted %s", b)
	}
	// At start the persisted level and mute are applied again.
	m.fail = nil
	m.take()
	a.Restore(ctx)
	if got := m.take(); !strings.Contains(got, "pactl set-sink-volume @DEFAULT_SINK@ 20%") || !strings.Contains(got, "pactl set-sink-mute @DEFAULT_SINK@ 1") {
		t.Fatalf("restore calls:\n%s", got)
	}
}

func TestSpeakQueuesWithoutBlocking(t *testing.T) {
	var said []string
	h := &Host{Say: func(text string) bool { said = append(said, text); return true }}
	if ask(t, h, "audio.speak", map[string]any{"text": "hello"}) != true || ask(t, h, "audio.speak", map[string]any{"text": " "}) != false {
		t.Fatal("speak")
	}
	if len(said) != 1 || said[0] != "hello" {
		t.Fatalf("said %v", said)
	}
	if v := ask(t, &Host{}, "audio.speak", map[string]any{"text": "x"}); v != false {
		t.Fatalf("speak with no speaker = %v", v)
	}
	if _, err := (&Host{}).Answer(context.Background(), "audio.set_muted", json.RawMessage(`{"muted":true}`)); err == nil {
		t.Fatal("mute without volume control")
	}
	if _, err := (&Host{}).Answer(context.Background(), "bogus.x", nil); err == nil {
		t.Fatal("an unknown method was answered")
	}
}

// A music command's play stops the other players: the legacy map names spotify's own player
// (go-librespot), so only mpv is interrupted; anything else leaves them alone.
func TestBeforeCallTakesOverForAMusicPlay(t *testing.T) {
	var calls []string
	run := func(_ context.Context, name string, args ...string) (string, error) {
		argv := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, argv)
		if argv == "pactl -f json list sink-inputs" {
			return `[{"index": 1, "corked": false, "properties": {"application.process.binary": "/usr/bin/mpv"}},
				{"index": 2, "corked": false, "properties": {"application.process.binary": "/opt/go-librespot"}}]`, nil
		}
		return "", nil
	}
	m := music.New(nil, nil)
	m.Run = run
	m.LibrespotURL = "http://127.0.0.1:1" // must not be reached: it's the caller's own player
	h := &Host{Music: m}
	ctx := context.Background()
	h.BeforeCall(ctx, "spotify", "", map[string]any{"action": "now_playing"})
	h.BeforeCall(ctx, "get_weather", "", map[string]any{"action": "play"})
	if len(calls) != 0 {
		t.Fatalf("not a music play, but ran %q", calls)
	}
	h.BeforeCall(ctx, "spotify", "", map[string]any{"action": "play"})
	if want := []string{"pactl -f json list sink-inputs", "pkill -INT -x mpv"}; strings.Join(calls, ";") != strings.Join(want, ";") {
		t.Fatalf("calls %q, want %q", calls, want)
	}
	(&Host{}).BeforeCall(ctx, "spotify", "", map[string]any{"action": "play"}) // no Music: nothing
}
