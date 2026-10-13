package portal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// fakeBus is a scripted RemoteDesktop portal. It hands out restore tokens tok-1, tok-2, … and
// knows which one is current; a Start without the current token "shows the dialog", answered
// with dialogAnswer.
type fakeBus struct {
	version, types uint32
	noProps        bool
	current        string // the restore token the desktop honours now
	issued         int
	dialogs        int
	dialogAnswer   uint32 // 0 allow, 1 cancel
	noToken        bool   // a portal that never returns one
	devices        uint32 // what Start grants (default keyboard)
	log            []string
	closed         bool
	pending        string // the token SelectDevices was given
	rejectUnknown  bool   // refuse an unknown token in SelectDevices (xdg-desktop-portal 1.22)
}

func (f *fakeBus) Version(context.Context) (uint32, error) {
	if f.noProps {
		return 0, errors.New("org.freedesktop.DBus.Error.UnknownInterface")
	}
	return f.version, nil
}
func (f *fakeBus) DeviceTypes(context.Context) (uint32, error) { return f.types, nil }
func (f *fakeBus) CreateSession(_ context.Context, _ map[string]any) (uint32, map[string]any, error) {
	f.log = append(f.log, "create")
	return 0, map[string]any{"session_handle": "/s/1"}, nil
}
func (f *fakeBus) SelectDevices(_ context.Context, s string, o map[string]any) (uint32, map[string]any, error) {
	f.pending, _ = o["restore_token"].(string)
	f.log = append(f.log, fmt.Sprintf("select %s types=%v persist=%v token=%q", s, o["types"], o["persist_mode"], f.pending))
	if f.rejectUnknown && f.pending != "" && f.pending != f.current {
		return 0, nil, fmt.Errorf("%w (Restore token is not valid)", ErrBadToken)
	}
	return 0, nil, nil
}
func (f *fakeBus) Start(_ context.Context, s string, _ map[string]any) (uint32, map[string]any, error) {
	if f.pending == "" || f.pending != f.current {
		f.dialogs++
		f.log = append(f.log, "dialog")
		if f.dialogAnswer != 0 {
			return f.dialogAnswer, nil, nil
		}
	}
	f.log = append(f.log, "start "+s)
	res := map[string]any{"devices": uint32(1)}
	if f.devices != 0 {
		res["devices"] = f.devices
	}
	if !f.noToken {
		f.issued++
		f.current = fmt.Sprintf("tok-%d", f.issued)
		res["restore_token"] = f.current
	}
	return 0, res, nil
}
func (f *fakeBus) Keysym(_ context.Context, _ string, sym int32, down bool) error {
	st := "up"
	if down {
		st = "down"
	}
	f.log = append(f.log, fmt.Sprintf("key %#x %s", sym, st))
	return nil
}
func (f *fakeBus) CloseSession(_ context.Context, s string) error {
	f.log = append(f.log, "close "+s)
	return nil
}
func (f *fakeBus) Close() { f.closed = true }

func newRD(t *testing.T, f *fakeBus) (*RemoteDesktop, *TokenStore) {
	ts := &TokenStore{Path: filepath.Join(t.TempDir(), "cfg", "portal-remote-desktop.token")}
	return &RemoteDesktop{Dial: func() (Bus, error) { return f, nil }, Tokens: ts}, ts
}

func TestProbeStates(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		bus  *fakeBus
		want string
	}{
		{"no portal", &fakeBus{noProps: true}, "no RemoteDesktop portal"},
		{"v1", &fakeBus{version: 1, types: 7}, "version 1"},
		{"no keyboard", &fakeBus{version: 2, types: 2}, "no keyboard"},
		{"not allowed yet", &fakeBus{version: 2, types: 7}, AllowCommand},
	} {
		rd, _ := newRD(t, c.bus)
		st := rd.Probe(ctx)
		if st.Allowed || !strings.Contains(st.Why, c.want) {
			t.Errorf("%s: %+v", c.name, st)
		}
		if ok, _ := (&Keyboard{R: rd}).Available(ctx); ok {
			t.Errorf("%s: available", c.name)
		}
		if len(c.bus.log) != 0 {
			t.Errorf("%s: probing did %v (it must never open a session or a dialog)", c.name, c.bus.log)
		}
	}
	f := &fakeBus{version: 2, types: 7}
	rd, ts := newRD(t, f)
	_ = ts.Save("tok-9")
	if st := rd.Probe(ctx); !st.Portal || !st.Allowed || st.Why != "" {
		t.Fatalf("%+v", st)
	}
	if ok, why := (&Keyboard{R: rd}).Available(ctx); !ok {
		t.Fatal(why)
	}
	if (&Keyboard{}).Name() != Name {
		t.Fatal("name")
	}
}

func TestAllowAsksOnceAndKeepsTheToken(t *testing.T) {
	ctx := context.Background()
	f := &fakeBus{version: 2, types: 7}
	rd, ts := newRD(t, f)
	if err := rd.Allow(ctx); err != nil {
		t.Fatal(err)
	}
	if tok, _ := ts.Load(); tok != "tok-1" || f.dialogs != 1 {
		t.Fatalf("token %q dialogs %d", tok, f.dialogs)
	}
	want := []string{"create", `select /s/1 types=1 persist=2 token=""`, "dialog", "start /s/1", "close /s/1"}
	if !slices.Equal(f.log, want) || !f.closed {
		t.Fatalf("%q", f.log)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(ts.Path)
		dir, _ := os.Stat(filepath.Dir(ts.Path))
		if st.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Fatalf("modes %o %o", st.Mode().Perm(), dir.Mode().Perm())
		}
	}

	// Typing restores without a dialog and keeps the rotated token.
	f.log = nil
	strokes, _ := xkb.Strokes("hé")
	strokes = append(strokes, xkb.Stroke{Sym: xkb.KeyTab, Shift: true})
	if err := rd.Send(ctx, strokes); err != nil {
		t.Fatal(err)
	}
	want = []string{"create", `select /s/1 types=1 persist=2 token="tok-1"`, "start /s/1",
		"key 0x68 down", "key 0x68 up", "key 0xe9 down", "key 0xe9 up",
		"key 0xffe1 down", "key 0xff09 down", "key 0xff09 up", "key 0xffe1 up", "close /s/1"}
	if !slices.Equal(f.log, want) || f.dialogs != 1 {
		t.Fatalf("got  %q\nwant %q", f.log, want)
	}
	if tok, _ := ts.Load(); tok != "tok-2" {
		t.Fatalf("token not rotated: %q", tok)
	}
	if err := rd.Revoke(); err != nil {
		t.Fatal(err)
	}
	if err := rd.Send(ctx, strokes); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("after revoke: %v", err)
	}
}

func TestAllowRefusals(t *testing.T) {
	ctx := context.Background()
	f := &fakeBus{version: 2, types: 7, dialogAnswer: 1}
	rd, ts := newRD(t, f)
	if err := rd.Allow(ctx); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancel: %v", err)
	}
	f = &fakeBus{version: 2, types: 7, noToken: true}
	rd, ts = newRD(t, f)
	if err := rd.Allow(ctx); err == nil || !strings.Contains(err.Error(), "no restore token") {
		t.Fatalf("no token: %v", err)
	}
	if tok, _ := ts.Load(); tok != "" {
		t.Fatal("stored a token")
	}
	f = &fakeBus{version: 2, types: 7, devices: 2}
	rd, _ = newRD(t, f)
	if err := rd.Allow(ctx); err == nil || !strings.Contains(err.Error(), "keyboard") {
		t.Fatalf("pointer only: %v", err)
	}
	rd, _ = newRD(t, &fakeBus{version: 1, types: 7})
	if err := rd.Allow(ctx); err == nil {
		t.Fatal("allowed on a v1 portal")
	}
}

// A token the desktop stopped honouring brings its dialog back; a cancel there forgets it, so
// status says to allow again instead of popping dialogs on every dictation.
func TestSendWithARevokedGrant(t *testing.T) {
	ctx := context.Background()
	f := &fakeBus{version: 2, types: 7, current: "something-else", dialogAnswer: 1}
	rd, ts := newRD(t, f)
	_ = ts.Save("tok-old")
	err := rd.Send(ctx, []xkb.Stroke{{Sym: 'a'}})
	if err == nil || !strings.Contains(err.Error(), AllowCommand) {
		t.Fatalf("err %v", err)
	}
	if tok, _ := ts.Load(); tok != "" {
		t.Fatalf("token kept: %q", tok)
	}
	for _, l := range f.log {
		if strings.HasPrefix(l, "key") {
			t.Fatal("typed without a session")
		}
	}
	if f.log[len(f.log)-1] != "close /s/1" {
		t.Fatalf("session left open: %q", f.log)
	}
}

func TestSendWithATokenThePortalRejects(t *testing.T) {
	f := &fakeBus{version: 2, types: 7, current: "tok-new", rejectUnknown: true}
	rd, ts := newRD(t, f)
	_ = ts.Save("tok-old")
	err := rd.Send(context.Background(), []xkb.Stroke{{Sym: 'a'}})
	if !errors.Is(err, ErrBadToken) || !strings.Contains(err.Error(), AllowCommand) || f.dialogs != 0 {
		t.Fatalf("err %v dialogs %d", err, f.dialogs)
	}
	if tok, _ := ts.Load(); tok != "" {
		t.Fatal("kept a rejected token")
	}
	if st := rd.Probe(context.Background()); st.Allowed {
		t.Fatal("still reported as allowed")
	}
}

func TestTokenStore(t *testing.T) {
	ts := &TokenStore{Path: filepath.Join(t.TempDir(), "a", "t")}
	if tok, err := ts.Load(); tok != "" || err != nil {
		t.Fatal(tok, err)
	}
	if ts.Save("") == nil || ts.Save("a\nb") == nil {
		t.Fatal("bad tokens accepted")
	}
	if err := ts.Save("abc"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := ts.Load(); tok != "abc" {
		t.Fatal(tok)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(ts.Path, 0o644)
		if _, err := ts.Load(); err == nil {
			t.Fatal("read a world-readable token")
		}
	}
	if err := ts.Clear(); err != nil || ts.Clear() != nil {
		t.Fatal("clear")
	}
}

func TestDesktopFile(t *testing.T) {
	dir := t.TempDir()
	p, err := DesktopFilePath(func(k string) string { return map[string]string{"XDG_DATA_HOME": dir}[k] })
	if err != nil || p != filepath.Join(dir, "applications", "net.jarvisautomation.agent.desktop") {
		t.Fatal(p, err)
	}
	if err := WriteDesktopFile(p, "/opt/jarvis/jarvis-agent"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{"[Desktop Entry]", "Exec=/opt/jarvis/jarvis-agent run", "NoDisplay=true", "Name=Jarvis Automation Agent"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("lacks %q:\n%s", want, b)
		}
	}
	if err := WriteDesktopFile(p, "/usr/bin/jarvis-agent"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "Exec=/usr/bin/jarvis-agent run") {
		t.Fatal("not refreshed for a moved binary")
	}
}

func TestMigrateLegacy(t *testing.T) {
	dir := t.TempDir()
	env := func(k string) string { return map[string]string{"XDG_DATA_HOME": dir}[k] }
	ts := &TokenStore{Path: filepath.Join(dir, "cfg", "portal-remote-desktop.token")}
	if err := ts.Save("tok"); err != nil {
		t.Fatal(err)
	}
	// No old .desktop file: the token was granted under some other id; keep it.
	if did, err := MigrateLegacy(env, ts); did || err != nil {
		t.Fatal(did, err)
	}
	if tok, _ := ts.Load(); tok != "tok" {
		t.Fatal("token dropped without a legacy grant")
	}
	old := filepath.Join(dir, "applications", "io.jarvis.agent.desktop")
	if err := WriteDesktopFile(old, "/opt/jarvis/jarvis-agent"); err != nil {
		t.Fatal(err)
	}
	if did, err := MigrateLegacy(env, ts); !did || err != nil {
		t.Fatal(did, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old .desktop file kept")
	}
	if tok, _ := ts.Load(); tok != "" {
		t.Fatal("a token granted to the old app id was kept")
	}
	if did, err := MigrateLegacy(env, ts); did || err != nil {
		t.Fatal("not idempotent", did, err)
	}
}
