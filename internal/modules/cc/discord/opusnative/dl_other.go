//go:build !darwin && !freebsd && !linux && !windows

package opusnative

import "errors"

func openLib(string) (uintptr, error) {
	return 0, errors.New("opusnative: loading libopus is not supported on this platform")
}
