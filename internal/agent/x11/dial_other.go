//go:build !linux

package x11

import (
	"errors"
	"os"
)

var osGetenv = os.Getenv

func dialX() (Conn, error) { return nil, errors.New("X11 is only supported on Linux") }

// Record needs the real connection: Linux only.
func (d *Display) Record(func(RecordedInput)) (Keymap, func(), error) {
	return Keymap{}, nil, errors.New("X11 is only supported on Linux")
}
