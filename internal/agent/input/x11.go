package input

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// X11Display is the slice of *x11.Display the X11 backend uses (XTEST over a pure-Go X11
// connection).
type X11Display interface {
	TypingAvailable(ctx context.Context) (bool, string)
	Move(ctx context.Context, x, y int) error
	Button(ctx context.Context, b byte, down bool) error
	Click(ctx context.Context, x, y int, b byte, n int) error
	Drag(ctx context.Context, path [][2]int) error
	Scroll(ctx context.Context, x, y, dx, dy int) error
	Keys(ctx context.Context, chord []string) error
	Send(ctx context.Context, strokes []xkb.Stroke) error
	Pointer(ctx context.Context) (x, y int, err error)
}

var x11Buttons = map[string]byte{"": x11.ButtonLeft, "left": x11.ButtonLeft, "middle": x11.ButtonMiddle, "right": x11.ButtonRight}

// X11 posts XTEST events on an X11 session. Coordinates are root-window pixels (X11 has no
// logical scale).
type X11 struct {
	D X11Display

	mu sync.Mutex // serializes posting calls; held across post

	winMu      sync.Mutex // guards start and end only; never held across post
	start, end time.Time
}

func (k *X11) Available(ctx context.Context) (bool, string) {
	if k.D == nil {
		return false, "controlling the computer isn't supported in this build"
	}
	return k.D.TypingAvailable(ctx)
}

func (k *X11) Injecting() (start, end time.Time) {
	k.winMu.Lock()
	defer k.winMu.Unlock()
	return k.start, k.end
}

func (k *X11) setWindow(start, end time.Time) {
	k.winMu.Lock()
	k.start, k.end = start, end
	k.winMu.Unlock()
}

// inject runs post with the injection window recorded around it. Calls are serialized.
func (k *X11) inject(ctx context.Context, post func() error) error {
	if ok, why := k.Available(ctx); !ok {
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	start := time.Now()
	k.setWindow(start, time.Time{})
	defer func() { k.setWindow(start, time.Now()) }()
	return post()
}

func x11Button(b string) (byte, error) {
	n, ok := x11Buttons[b]
	if !ok {
		return 0, fmt.Errorf("unknown button %q", b)
	}
	return n, nil
}

func (k *X11) Move(ctx context.Context, x, y int) error {
	return k.inject(ctx, func() error { return k.D.Move(ctx, x, y) })
}

func (k *X11) Button(ctx context.Context, b string, down bool) error {
	n, err := x11Button(b)
	if err != nil {
		return err
	}
	return k.inject(ctx, func() error { return k.D.Button(ctx, n, down) })
}

func (k *X11) Click(ctx context.Context, x, y int, b string, n int) error {
	num, err := x11Button(b)
	if err != nil {
		return err
	}
	if n < 1 || n > 3 {
		return fmt.Errorf("click count %d: want 1 to 3", n)
	}
	return k.inject(ctx, func() error { return k.D.Click(ctx, x, y, num, n) })
}

func (k *X11) Drag(ctx context.Context, path [][2]int) error {
	if len(path) < 2 {
		return errors.New("drag needs at least two points")
	}
	return k.inject(ctx, func() error { return k.D.Drag(ctx, path) })
}

func (k *X11) Scroll(ctx context.Context, x, y, dx, dy int) error {
	return k.inject(ctx, func() error { return k.D.Scroll(ctx, x, y, dx, dy) })
}

func (k *X11) Keys(ctx context.Context, chord []string) error {
	return k.inject(ctx, func() error { return k.D.Keys(ctx, chord) })
}

// x11Strokes is text as keystrokes: a line break presses Return and a tab Tab; other control
// characters can't be typed.
func x11Strokes(text string) ([]xkb.Stroke, error) {
	var out []xkb.Stroke
	for _, r := range text {
		switch r {
		case '\n':
			out = append(out, xkb.Stroke{Sym: xkb.KeyReturn})
		case '\r':
		case '\t':
			out = append(out, xkb.Stroke{Sym: xkb.KeyTab})
		default:
			s, ok := xkb.FromRune(r)
			if !ok {
				return nil, fmt.Errorf("text holds a character that can't be typed (%U)", r)
			}
			out = append(out, xkb.Stroke{Sym: s})
		}
	}
	return out, nil
}

func (k *X11) Type(ctx context.Context, text string) error {
	strokes, err := x11Strokes(text)
	if err != nil {
		return err
	}
	return k.inject(ctx, func() error { return k.D.Send(ctx, strokes) })
}

func (k *X11) Cursor(ctx context.Context) (x, y int, err error) {
	if ok, why := k.Available(ctx); !ok {
		return 0, 0, fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	return k.D.Pointer(ctx)
}
