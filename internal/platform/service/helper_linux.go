package service

import (
	"context"
	"errors"
	"os"
)

// DetectHelper reports the system unit's root ExecStartPre (the unit sets EnvUpgradeHelper).
func DetectHelper() Helper {
	if os.Getenv(EnvUpgradeHelper) == "1" {
		return HelperPrestart
	}
	return HelperNone
}

// StartHelper is for the Windows helper; the systemd one runs before every start.
func StartHelper(context.Context) error {
	return errors.New("the systemd helper runs before every start; restart the service")
}

// RestartFromHelper is for the on-demand helpers (macOS, Windows).
func RestartFromHelper(context.Context) error {
	return errors.New("the systemd helper runs as part of the start")
}
