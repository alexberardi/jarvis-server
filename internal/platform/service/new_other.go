//go:build !linux && !darwin && !windows

package service

import (
	"errors"
	"io"
)

var errUnsupported = errors.New("no supported service manager on this OS; run `jarvisd serve` under your own supervisor")

// New fails: only systemd, launchd and the Windows SCM are supported.
func New(bool, io.Writer) (Manager, error) { return nil, errUnsupported }

// Open fails like New.
func Open(bool, io.Writer) (Manager, error) { return nil, errUnsupported }

// InstalledHome is always "" here.
func InstalledHome() string { return "" }
