//go:build !linux

package vptr

import (
	"context"
	"errors"
)

// Pointer is unavailable off Linux.
type Pointer struct {
	Getenv func(string) string
}

func (*Pointer) Name() string { return Name }

func (*Pointer) Available(context.Context) (bool, string) {
	return false, "the wlroots virtual pointer is Linux-only"
}

func (*Pointer) Send(context.Context, ...Event) error {
	return errors.New("the wlroots virtual pointer is Linux-only")
}

func (*Pointer) Close() {}
