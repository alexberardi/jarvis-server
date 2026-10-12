//go:build !darwin

package screen

import "errors"

func frontWindow() (FrontWindow, error) { return FrontWindow{}, errors.New("not macOS") }
