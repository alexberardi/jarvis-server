package service

import (
	"context"
	"errors"
	"os"
)

// DetectHelper reports the updater LaunchDaemon when its plist is installed.
func DetectHelper() Helper {
	if st, err := os.Stat(helperPlist); err == nil && st.Mode().IsRegular() {
		return HelperLaunchd
	}
	return HelperNone
}

// StartHelper is for the Windows helper; the macOS one is woken by a request file
// (update.Request), which an unprivileged process can write.
func StartHelper(context.Context) error {
	return errors.New("the macOS helper is woken by a request file, not started")
}

// RestartFromHelper restarts the jarvisd LaunchDaemon (kill, then start again) after the root
// helper swapped or restored its binary. launchctl by absolute path: the helper trusts no PATH.
func RestartFromHelper(ctx context.Context) error {
	_, err := execRun(ctx, "/bin/launchctl", "kickstart", "-k", target)
	return err
}

// helperLaunchd is the launchd manager as the root helper uses it: launchctl by absolute path.
func helperLaunchd() *launchd {
	l := newLaunchd(nil)
	l.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			name = "/bin/launchctl"
		}
		return execRun(ctx, name, args...)
	}
	return l
}

// RefreshDefinition rewrites the LaunchDaemon plists this build renders differently (see
// launchd.refresh). For the root helper, right after it installed this binary.
func RefreshDefinition(_ context.Context, exe, home string) (RefreshResult, error) {
	return helperLaunchd().refresh(exe, home)
}

// ReloadFromHelper restarts jarvisd on its rewritten plist (bootout, bootstrap, kickstart):
// RestartFromHelper's kickstart -k would keep the plist launchd has loaded.
func ReloadFromHelper(ctx context.Context) error { return helperLaunchd().reloadFromHelper(ctx) }
