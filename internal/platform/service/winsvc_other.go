//go:build !windows

package service

import (
	"context"
	"errors"
)

// IsWindowsService is false off Windows.
func IsWindowsService() bool { return false }

// RunWindowsService exists only on Windows.
func RunWindowsService(func(ctx context.Context) error) error {
	return errors.New("not a Windows service")
}

// RunHelperService exists only on Windows.
func RunHelperService(func(ctx context.Context) error) error {
	return errors.New("not a Windows service")
}

// RedirectStderr is only needed under the Windows SCM; elsewhere the supervisor captures
// stderr (journald, launchd's StandardErrorPath).
func RedirectStderr(string) error { return nil }
