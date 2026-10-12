//go:build !linux

package hat

import "errors"

// SPIDevice is the HAT's LED chain (Linux only).
const SPIDevice = "/dev/spidev0.0"

var errUnsupported = errors.New("the HAT is Linux-only")

// OpenStrip is Linux-only.
func OpenStrip(string) (Strip, error) { return nil, errUnsupported }

// WatchButton is Linux-only.
func WatchButton(*Button) (func() error, error) { return nil, errUnsupported }

// Detected is false off Linux.
func Detected() bool { return false }
