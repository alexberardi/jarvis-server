package input

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// fakeX11 records each call to the display as a line.
type fakeX11 struct {
	ok  bool
	got []string
}

func (f *fakeX11) TypingAvailable(context.Context) (bool, string) {
	if !f.ok {
		return false, "no X11 display (DISPLAY is unset)"
	}
	return true, ""
}
func (f *fakeX11) log(format string, a ...any) error {
	f.got = append(f.got, fmt.Sprintf(format, a...))
	return nil
}
func (f *fakeX11) Move(_ context.Context, x, y int) error { return f.log("move %d,%d", x, y) }
func (f *fakeX11) Button(_ context.Context, b byte, down bool) error {
	return f.log("button %d %v", b, down)
}
func (f *fakeX11) Click(_ context.Context, x, y int, b byte, n int) error {
	return f.log("click %d,%d b%d x%d", x, y, b, n)
}
func (f *fakeX11) Drag(_ context.Context, p [][2]int) error { return f.log("drag %v", p) }
func (f *fakeX11) Scroll(_ context.Context, x, y, dx, dy int) error {
	return f.log("scroll %d,%d %d %d", x, y, dx, dy)
}
func (f *fakeX11) Keys(_ context.Context, c []string) error { return f.log("keys %v", c) }
func (f *fakeX11) Send(_ context.Context, s []xkb.Stroke) error {
	return f.log("send %v", s)
}
func (f *fakeX11) Pointer(context.Context) (int, int, error) { return 7, 8, nil }

func TestX11Backend(t *testing.T) {
	ctx := context.Background()
	f := &fakeX11{ok: true}
	k := &X11{D: f}
	_ = k.Click(ctx, 10, 20, "", 1)
	_ = k.Click(ctx, 10, 20, "right", 2)
	_ = k.Button(ctx, "middle", true)
	_ = k.Move(ctx, 3, 4)
	_ = k.Scroll(ctx, 1, 2, 0, -3)
	_ = k.Drag(ctx, [][2]int{{1, 1}, {2, 2}})
	_ = k.Keys(ctx, []string{"ctrl", "s"})
	_ = k.Type(ctx, "a\r\n\tb")
	check(t, f.got, []string{"click 10,20 b1 x1", "click 10,20 b3 x2", "button 2 true", "move 3,4",
		"scroll 1,2 0 -3", "drag [[1 1] [2 2]]", "keys [ctrl s]",
		fmt.Sprintf("send %v", []xkb.Stroke{{Sym: 'a'}, {Sym: xkb.KeyReturn}, {Sym: xkb.KeyTab}, {Sym: 'b'}})})
	if x, y, err := k.Cursor(ctx); err != nil || x != 7 || y != 8 {
		t.Fatalf("Cursor = %d,%d %v", x, y, err)
	}
	f.got = nil
	for _, err := range []error{k.Click(ctx, 1, 1, "back", 1), k.Click(ctx, 1, 1, "left", 4),
		k.Drag(ctx, [][2]int{{1, 1}}), k.Type(ctx, "a\x1bb")} {
		if err == nil {
			t.Error("bad input accepted")
		}
	}
	check(t, f.got, nil)
}

func TestX11Unavailable(t *testing.T) {
	ctx := context.Background()
	f := &fakeX11{}
	k := &X11{D: f}
	if ok, why := k.Available(ctx); ok || why == "" {
		t.Fatalf("Available = %v %q", ok, why)
	}
	if err := k.Click(ctx, 1, 1, "left", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Click = %v, want ErrUnavailable", err)
	}
	if _, _, err := k.Cursor(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Cursor = %v, want ErrUnavailable", err)
	}
	check(t, f.got, nil)
	if ok, _ := (&X11{}).Available(ctx); ok {
		t.Fatal("no display: want unavailable")
	}
}

func TestX11Injecting(t *testing.T) {
	k := &X11{D: &fakeX11{ok: true}}
	before := time.Now()
	_ = k.Keys(context.Background(), []string{"a"})
	s, e := k.Injecting()
	if s.Before(before) || e.Before(s) {
		t.Fatalf("Injecting = %v..%v, called at %v", s, e, before)
	}
}
