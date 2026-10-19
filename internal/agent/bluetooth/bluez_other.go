//go:build !linux

package bluetooth

import (
	"errors"
	"log/slog"
)

// Open is BlueZ, which only Linux has.
func Open(*slog.Logger) (Bus, func(), error) {
	return nil, nil, errors.New("Bluetooth needs BlueZ (Linux)")
}
