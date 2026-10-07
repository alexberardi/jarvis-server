package service

import (
	"errors"
	"io"
)

// New returns the launchd (LaunchDaemon) manager.
func New(user bool, out io.Writer) (Manager, error) {
	if user {
		return nil, errors.New("--user is for Linux; on macOS jarvisd installs as a LaunchDaemon running as you")
	}
	return newLaunchd(out), nil
}

// Open returns the manager for the installed service.
func Open(user bool, out io.Writer) (Manager, error) { return New(user, out) }

// InstalledHome is the --home of the installed LaunchDaemon, "" when none.
func InstalledHome() string { return newLaunchd(nil).InstalledHome() }
