//go:build linux

package vkbd

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// A scripted compositor on a socketpair: it advertises globals, answers syncs, records every
// request (reading the keymap out of the passed memfd) and can raise a protocol error.

type req struct {
	id     uint32
	op     uint16
	args   []byte
	keymap string // for keymap requests: the memfd's content
}

type fake struct {
	c       *net.UnixConn
	globals []string
	// errOnCreate raises virtual_keyboard_manager.error unauthorized on create.
	errOnCreate bool
	// keymap is what wl_keyboard.keymap hands a new keyboard ("": no event).
	keymap string

	mu   sync.Mutex
	reqs []req
	ids  map[string]uint32
}

func newFake(t *testing.T, globals ...string) (*fake, func() (*wlwire.Conn, error)) {
	return newFakeOpt(t, false, globals...)
}

func newFakeOpt(t *testing.T, errOnCreate bool, globals ...string) (*fake, func() (*wlwire.Conn, error)) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "wl")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	f := &fake{c: conn(fds[0]), globals: globals, ids: map[string]uint32{}, errOnCreate: errOnCreate}
	client := conn(fds[1])
	t.Cleanup(func() { f.c.Close() })
	go f.serve()
	used := false
	return f, func() (*wlwire.Conn, error) {
		if used {
			t.Fatal("dialed twice")
		}
		used = true
		return wlwire.NewConn(client), nil
	}
}

func (f *fake) event(id uint32, op uint16, a *wlwire.Args) {
	body := a.Bytes()
	m := binary.LittleEndian.AppendUint32(nil, id)
	m = binary.LittleEndian.AppendUint32(m, uint32(8+len(body))<<16|uint32(op))
	_, _ = f.c.Write(append(m, body...))
}

func (f *fake) serve() {
	var buf []byte
	tmp := make([]byte, 65536)
	oob := make([]byte, unix.CmsgSpace(4*4))
	var fds []int
	for {
		n, oobn, _, _, err := f.c.ReadMsgUnix(tmp, oob)
		if err != nil {
			return
		}
		if oobn > 0 {
			msgs, _ := unix.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				got, _ := unix.ParseUnixRights(&m)
				fds = append(fds, got...)
			}
		}
		buf = append(buf, tmp[:n]...)
		for len(buf) >= 8 {
			size := int(binary.LittleEndian.Uint32(buf[4:]) >> 16)
			if len(buf) < size {
				break
			}
			r := req{id: binary.LittleEndian.Uint32(buf), op: uint16(binary.LittleEndian.Uint32(buf[4:])), args: append([]byte(nil), buf[8:size]...)}
			buf = buf[size:]
			f.mu.Lock()
			isKeymap := r.id == f.ids["kb"] && r.op == opKeymap
			f.mu.Unlock()
			if isKeymap {
				fd := fds[0]
				fds = fds[1:]
				a := wlwire.NewReader(r.args)
				_, size := a.U32(), a.U32()
				mem, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
				if err == nil {
					r.keymap = string(mem)
					_ = unix.Munmap(mem)
				}
				_ = unix.Close(fd)
			}
			f.mu.Lock()
			f.reqs = append(f.reqs, r)
			f.mu.Unlock()
			f.answer(r)
		}
	}
}

// eventFD sends an event with a file descriptor holding data.
func (f *fake) eventFD(id uint32, op uint16, a *wlwire.Args, data string) {
	fd, err := unix.MemfdCreate("keymap", unix.MFD_CLOEXEC)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	_, _ = unix.Write(fd, []byte(data))
	body := a.Bytes()
	m := binary.LittleEndian.AppendUint32(nil, id)
	m = binary.LittleEndian.AppendUint32(m, uint32(8+len(body))<<16|uint32(op))
	_, _, _ = f.c.WriteMsgUnix(append(m, body...), unix.UnixRights(fd), nil)
}

func (f *fake) answer(r req) {
	a := wlwire.NewReader(r.args)
	f.mu.Lock()
	ids := f.ids
	f.mu.Unlock()
	set := func(k string, v uint32) { f.mu.Lock(); f.ids[k] = v; f.mu.Unlock() }
	switch {
	case r.id == wlwire.DisplayID && r.op == wlwire.OpDisplayGetRegistry:
		reg := a.U32()
		set("registry", reg)
		for i, g := range f.globals {
			f.event(reg, wlwire.EvGlobal, wlwire.NewArgs().U32(uint32(i+1)).Str(g).U32(1))
		}
	case r.id == wlwire.DisplayID && r.op == wlwire.OpDisplaySync:
		f.event(a.U32(), wlwire.EvCallbackDone, wlwire.NewArgs().U32(0))
	case r.id == ids["registry"] && r.op == wlwire.OpRegistryBind:
		_, iface, v, id := a.U32(), a.Str(), a.U32(), a.U32()
		set(iface, id)
		set(iface+".v", v)
	case r.id == ids[IfaceSeat] && r.op == opSeatGetKeyboard:
		wk := a.U32()
		set("wl_keyboard", wk)
		f.mu.Lock()
		km := f.keymap
		f.mu.Unlock()
		if km != "" {
			f.eventFD(wk, evKeyboardKeymap, wlwire.NewArgs().U32(keymapFormatXKBV1).U32(uint32(len(km)+1)), km+"\x00")
		}
	case r.id == ids[IfaceManager] && r.op == opCreateVirtualKeyboard:
		seat, kb := a.U32(), a.U32()
		set("kb", kb)
		set("kb.seat", seat)
		if f.errOnCreate {
			f.event(wlwire.DisplayID, wlwire.EvDisplayError, wlwire.NewArgs().U32(r.id).U32(0).Str("unauthorized"))
		}
	}
}

func (f *fake) setKeymap(km string) { f.mu.Lock(); f.keymap = km; f.mu.Unlock() }

func (f *fake) requests() []req {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]req(nil), f.reqs...)
}
func (f *fake) id(k string) uint32 { f.mu.Lock(); defer f.mu.Unlock(); return f.ids[k] }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var wlEnv = env(map[string]string{"WAYLAND_DISPLAY": "wayland-9", "XDG_CURRENT_DESKTOP": "sway"})

const guard = 5 * time.Second // a hang guard, never waited on when the fake answers

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return nil
}

func TestAvailable(t *testing.T) {
	ctx := context.Background()
	_, dial := newFake(t, "wl_compositor", IfaceSeat, IfaceManager)
	if ok, why := (&Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard}).Available(ctx); !ok {
		t.Fatal(why)
	}
	_, dial = newFake(t, "wl_compositor", IfaceSeat)
	if ok, why := (&Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard}).Available(ctx); ok || !strings.Contains(why, "virtual-keyboard") {
		t.Fatal(ok, why)
	}
	_, dial = newFake(t, IfaceManager)
	if ok, why := (&Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard}).Available(ctx); ok || !strings.Contains(why, "seat") {
		t.Fatal(ok, why)
	}
	never := func() (*wlwire.Conn, error) { t.Fatal("dialed"); return nil, nil }
	for _, e := range []map[string]string{
		{"XDG_CURRENT_DESKTOP": "sway"},
		{"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "GNOME"},
		{"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "KDE"},
	} {
		if ok, _ := (&Keyboard{Getenv: env(e), Dial: never}).Available(ctx); ok {
			t.Fatalf("available with %v", e)
		}
	}
}

// keyEvents decodes the key and modifiers requests on kb, in order.
func keyEvents(t *testing.T, f *fake) (out []string, keymaps []string) {
	kb := f.id("kb")
	for _, r := range f.requests() {
		if r.id != kb {
			continue
		}
		a := wlwire.NewReader(r.args)
		switch r.op {
		case opKeymap:
			if format, size := a.U32(), a.U32(); format != keymapFormatXKBV1 || int(size) != len(r.keymap) {
				t.Fatalf("keymap format %d size %d (content %d)", format, size, len(r.keymap))
			}
			keymaps = append(keymaps, r.keymap)
			out = append(out, "keymap")
		case opKey:
			_, code, state := a.U32(), a.U32(), a.U32()
			out = append(out, map[uint32]string{1: "down", 0: "up"}[state]+" "+itoa(code))
		case opModifiers:
			out = append(out, "mods "+itoa(a.U32())+" "+itoa(a.U32())+" "+itoa(a.U32())+" "+itoa(a.U32()))
		case opDestroy:
			out = append(out, "destroy")
		}
	}
	return out, keymaps
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

// deskKeymap is a desktop layout as Hyprland serialises it (keysyms in hex).
const deskKeymap = `xkb_keymap {
xkb_keycodes "evdev_aliases(qwerty)" {
	<ESC> = 9; 
	<TAB> = 23;
	<RTRN> = 36;
	<AC06> = 43;
	<SPCE> = 65;
};
xkb_symbols "pc_us" {
	key <ESC> {	[ 0xff1b ] };
	key <TAB> {	[ 0xff09, 0xfe20 ] };
	key <RTRN> {	[ 0xff0d ] };
	key <AC06> {	[ 0x68, 0x48 ] };
	key <SPCE> {	[ 0x20 ] };
};
};`

// fullDesk is deskKeymap made a whole layout (letters and digits on other keys).
func fullDesk() string {
	var codes, syms strings.Builder
	for i, c := range "abcdefgijklmnopqrstuvwxyz0123456789" {
		code := 150 + i
		fmt.Fprintf(&codes, "<X%d> = %d;\n", code, code)
		fmt.Fprintf(&syms, "key <X%d> { [ 0x%x ] };\n", code, c)
	}
	km := strings.Replace(deskKeymap, "<ESC> = 9;", "<ESC> = 9;\n"+codes.String(), 1)
	return strings.Replace(km, "key <ESC> {", syms.String()+"key <ESC> {", 1)
}

func TestSendTypesOnTheDesktopLayout(t *testing.T) {
	f, dial := newFake(t, "wl_compositor", IfaceSeat, IfaceManager, IfaceSeat)
	f.setKeymap(fullDesk())
	sl := &sleeps{}
	k := &Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard, Sleep: sl.sleep}
	strokes := []xkb.Stroke{{Sym: 'h'}, {Sym: 'H'}, {Sym: 0x10065e5}, {Sym: 'h'}, {Sym: xkb.KeyTab, Shift: true}}
	if err := k.Send(context.Background(), strokes); err != nil {
		t.Fatal(err)
	}
	if f.id("wl_seat.v") != 1 || f.id(IfaceManager+".v") != 1 || f.id("kb.seat") != f.id("wl_seat") || f.id("kb") == 0 || f.id("wl_keyboard") == 0 {
		t.Fatalf("binds: %v", f.ids)
	}
	got, keymaps := keyEvents(t, f)
	want := []string{"keymap", "mods 0 0 0 0", // the keyboard takes over before its first key
		"down 35", "up 35", // h: the layout's keycode 43 = evdev 35
		"mods 1 0 0 0", "down 35", "up 35", "mods 0 0 0 0", // H: the same key, shifted
		"down 2", "up 2", // 日: not on the layout, on its first empty keycode (10 = evdev 2)
		"down 35", "up 35",
		"mods 1 0 0 0", "down 15", "up 15", "mods 0 0 0 0", // Shift+Tab
		"destroy"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	l, _ := xkb.ParseKeymap(fullDesk())
	bs, _ := xkb.Plan(strokes, l)
	if len(keymaps) != 1 || keymaps[0] != bs[0].Keymap()+"\x00" {
		t.Fatalf("keymap:\n%q", keymaps)
	}
	if len(sl.d) != 1 || sl.d[0] != DefaultSettle {
		t.Fatalf("settles %v, want one of %v before the first key", sl.d, DefaultSettle)
	}
}

func TestSendKeepsTheLastWholeLayout(t *testing.T) {
	k := &Keyboard{Getenv: wlEnv, Timeout: guard, Sleep: (&sleeps{}).sleep}
	typeH := func(keymap string) []string {
		f, dial := newFake(t, IfaceSeat, IfaceManager)
		f.setKeymap(keymap)
		k.Dial = dial
		if err := k.Send(context.Background(), []xkb.Stroke{{Sym: 'h'}}); err != nil {
			t.Fatal(err)
		}
		got, _ := keyEvents(t, f)
		return got
	}
	// No keymap from the compositor and none read before: US (h on keycode 43).
	if got := typeH(""); !slices.Contains(got, "down 35") {
		t.Fatalf("US fallback: %v", got)
	}
	// A whole layout with h elsewhere is taken...
	moved := strings.ReplaceAll(fullDesk(), "<AC06> = 43;", "<AC06> = 44;")
	if got := typeH(moved); !slices.Contains(got, "down 36") {
		t.Fatalf("desktop layout: %v", got)
	}
	// ...and kept while the active keyboard holds a fragment (another virtual keyboard's
	// keymap, or ours echoed by an input method).
	if got := typeH(deskKeymap); !slices.Contains(got, "down 36") {
		t.Fatalf("fragment: %v", got)
	}
	ours, _ := xkb.Plan([]xkb.Stroke{{Sym: 'q'}}, nil)
	if got := typeH(ours[0].Keymap()); !slices.Contains(got, "down 36") {
		t.Fatalf("our own keymap: %v", got)
	}
}

func TestSendRefusesCharactersAnX11WindowCantRead(t *testing.T) {
	f, dial := newFake(t, IfaceSeat, IfaceManager)
	f.setKeymap(fullDesk())
	k := &Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard, Sleep: (&sleeps{}).sleep,
		X11Focused: func(context.Context) bool { return true }}
	err := k.Send(context.Background(), []xkb.Stroke{{Sym: 'h'}, {Sym: 0x10065e5}, {Sym: 0xe9}})
	var nl *NotOnLayoutError
	if !errors.As(err, &nl) || nl.ErrorCode() != "unsupported_characters" || !strings.Contains(err.Error(), `'日', 'é'`) {
		t.Fatalf("err %v", err)
	}
	if f.id("kb") != 0 {
		t.Fatal("created a keyboard for text it refused")
	}
	// Text on the layout goes to an X11 window as usual.
	f, dial = newFake(t, IfaceSeat, IfaceManager)
	f.setKeymap(fullDesk())
	k.Dial = dial
	if err := k.Send(context.Background(), []xkb.Stroke{{Sym: 'h'}, {Sym: 'H'}}); err != nil {
		t.Fatal(err)
	}
}

func TestSendManyDistinctCharactersUsesSeveralKeymaps(t *testing.T) {
	f, dial := newFake(t, IfaceSeat, IfaceManager)
	f.setKeymap(fullDesk())
	sl := &sleeps{}
	k := &Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard, Sleep: sl.sleep, Settle: -1}
	var strokes []xkb.Stroke
	for i := range 1000 {
		strokes = append(strokes, xkb.Stroke{Sym: uint32(0x1004e00 + i)})
	}
	if err := k.Send(context.Background(), strokes); err != nil {
		t.Fatal(err)
	}
	got, keymaps := keyEvents(t, f)
	downs := 0
	for _, g := range got {
		if strings.HasPrefix(g, "down") {
			downs++
		}
	}
	if len(keymaps) < 2 || downs != 1000 || got[len(got)-1] != "destroy" {
		t.Fatalf("%d keymaps, %d keys, last %q", len(keymaps), downs, got[len(got)-1])
	}
	if len(sl.d) != 0 {
		t.Fatalf("settle -1 still slept %v", sl.d)
	}
}

func TestSendReportsTheCompositorRefusing(t *testing.T) {
	f, dial := newFakeOpt(t, true, IfaceSeat, IfaceManager)
	k := &Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard, Sleep: (&sleeps{}).sleep}
	err := k.Send(context.Background(), []xkb.Stroke{{Sym: 'a'}})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err %v", err)
	}
	for _, r := range f.requests() {
		if r.id == f.id("kb") && r.op == opKey {
			t.Fatal("pressed a key after the error")
		}
	}
}

func TestSendStopsWhenCancelled(t *testing.T) {
	f, dial := newFake(t, IfaceSeat, IfaceManager)
	ctx, cancel := context.WithCancel(context.Background())
	k := &Keyboard{Getenv: wlEnv, Dial: dial, Timeout: guard,
		Sleep: func(context.Context, time.Duration) error { cancel(); return context.Canceled }}
	if err := k.Send(ctx, []xkb.Stroke{{Sym: 'a'}}); err == nil {
		t.Fatal("no error")
	}
	for _, r := range f.requests() {
		if r.id == f.id("kb") && r.op == opKey {
			t.Fatal("typed after cancel")
		}
	}
}
