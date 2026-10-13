//go:build !linux

package vkbd

import (
	"context"
	"errors"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// Keyboard is unavailable off Linux.
type Keyboard struct {
	Getenv     func(string) string
	Settle     time.Duration
	Sleep      func(context.Context, time.Duration) error
	X11Focused func(context.Context) bool
}

func (*Keyboard) Name() string { return Name }

func (*Keyboard) Available(context.Context) (bool, string) {
	return false, "the Wayland virtual keyboard is Linux-only"
}

func (*Keyboard) Send(context.Context, []xkb.Stroke) error {
	return errors.New("the Wayland virtual keyboard is Linux-only")
}
