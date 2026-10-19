//go:build !linux

package provisioning

import "errors"

func dialNMBus() (nmBus, error) { return nil, errors.New("NetworkManager is Linux-only") }
