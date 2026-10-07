package service

import (
	"errors"
	"io"
	"os"
)

// New returns the systemd manager: the system unit, or the --user unit when user is set.
func New(user bool, out io.Writer) (Manager, error) {
	// sd_booted(): systemd is PID 1 only if this directory exists.
	if st, err := os.Stat("/run/systemd/system"); err != nil || !st.IsDir() {
		return nil, errors.New("systemd is not running here; run `jarvisd serve` under your own supervisor")
	}
	return newSystemd(user, out), nil
}

// Open returns the manager for the installed service: the --user unit when user is set or
// when only a user unit exists, else the system unit.
func Open(user bool, out io.Writer) (Manager, error) {
	if !user {
		if _, err := os.Stat(systemUnit); err != nil {
			if _, err := os.Stat(userUnitPath(os.Getenv, os.UserHomeDir)); err == nil {
				user = true
			}
		}
	}
	return New(user, out)
}

// InstalledHome is the --home of the installed system unit, else of the user's --user
// unit, "" when neither exists.
func InstalledHome() string {
	if h := newSystemd(false, nil).InstalledHome(); h != "" {
		return h
	}
	return newSystemd(true, nil).InstalledHome()
}
