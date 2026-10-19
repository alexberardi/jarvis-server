//go:build !linux

package atspi

import (
	"context"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

func snapshot(context.Context, int, int) (computeruse.Snapshot, error) {
	return computeruse.Snapshot{}, ErrUnsupported
}

func press(int, string) error                               { return ErrUnsupported }
func setValue(int, string, string) error                    { return ErrUnsupported }
func focus(int, string) error                               { return ErrUnsupported }
func element(int, string) (*computeruse.Element, error)     { return nil, ErrUnsupported }
func elementAt(int, int, int) (*computeruse.Element, error) { return nil, ErrUnsupported }
