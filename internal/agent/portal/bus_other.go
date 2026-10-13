//go:build !linux

package portal

import "errors"

func dialBus() (Bus, error) { return nil, errors.New("the desktop portal is Linux-only") }
