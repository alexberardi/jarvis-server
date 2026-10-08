//go:build !linux && !darwin && !windows

package service

import "context"

// DetectHelper: no privileged upgrade helper on this OS.
func DetectHelper() Helper { return HelperNone }

// StartHelper fails like New.
func StartHelper(context.Context) error { return errUnsupported }

// RestartFromHelper fails like New.
func RestartFromHelper(context.Context) error { return errUnsupported }
