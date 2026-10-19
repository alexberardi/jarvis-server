//go:build !darwin

package screen

import (
	"errors"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

func windowList() ([]MacWindow, error) { return nil, errors.New("not macOS") }

func mainDisplay() (computeruse.Geometry, error) {
	return computeruse.Geometry{}, errors.New("not macOS")
}
