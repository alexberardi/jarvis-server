//go:build !linux

package atspi

import (
	"context"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

func snapshotOf(context.Context, int, int, bool) (computeruse.Snapshot, error) {
	return computeruse.Snapshot{}, ErrUnsupported
}

func press(int, string) error                                       { return ErrUnsupported }
func setValue(int, string, string) error                            { return ErrUnsupported }
func focus(int, string) error                                       { return ErrUnsupported }
func element(int, string) (*computeruse.Element, error)             { return nil, ErrUnsupported }
func elementAt(int, int, int, uint32) (*computeruse.Element, error) { return nil, ErrUnsupported }
func focused(int) (*computeruse.Element, error)                     { return nil, ErrUnsupported }

func enable(context.Context) State { return State{Err: ErrUnsupported} }

const coordScreen, coordWindow = uint32(0), uint32(1)

func tabs(context.Context, int) ([]Tab, error)          { return nil, ErrUnsupported }
func selectTab(context.Context, int, int, string) error { return ErrUnsupported }
