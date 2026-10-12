//go:build !darwin

package screen

import "errors"

func windowList() ([]MacWindow, error) { return nil, errors.New("not macOS") }
