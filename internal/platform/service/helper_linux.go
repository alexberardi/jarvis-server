package service

import (
	"context"
	"errors"
	"fmt"
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

// RefreshDefinition rewrites the system unit as this build renders it, when it differs (see
// systemd.refresh). For the root pre-start helper, right after it installed this binary.
func RefreshDefinition(ctx context.Context, exe, home string) (RefreshResult, error) {
	if _, err := os.Stat(systemUnit); err != nil {
		return RefreshResult{}, fmt.Errorf("no system unit to refresh: %w", err)
	}
	return newSystemd(false, nil).refresh(ctx, exe, home)
}

// ReloadFromHelper is for the on-demand helpers (macOS, Windows).
func ReloadFromHelper(ctx context.Context) error { return RestartFromHelper(ctx) }
