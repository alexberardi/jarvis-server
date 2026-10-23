package packages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MusicPlayers are the units whose PulseAudio stream can die when the default sink moves (#478:
// shairport-sync's pa backend logged "Pulseaudio stream failed!" when headphones took the
// output, and stayed silent until restarted). The Bluetooth node restarts the running ones.
var MusicPlayers = []string{"shairport-sync", "spotifyd", "go-librespot"}

const restartTimeout = 30 * time.Second

// RestartUnits restarts the units that are running: a system unit a package configured (its
// managed drop-in) through jarvis-post-install's restart-service, as that package, and otherwise
// the node user's own unit with systemctl --user. A unit that isn't running is left alone (a
// system unit no package configured is not the node's to restart). It goes on past a failure;
// the error names each one.
func (s *System) RestartUnits(ctx context.Context, units ...string) error {
	var errs []error
	for _, unit := range units {
		if !nameRE.MatchString(unit) {
			continue
		}
		if err := s.restartUnit(ctx, unit); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", unit, err))
		}
	}
	return errors.Join(errs...)
}

func (s *System) restartUnit(ctx context.Context, unit string) error {
	if owner := s.dropinOwner(unit); owner != "" {
		if !s.active(ctx, "systemctl", "is-active", "--quiet", unit) {
			return nil
		}
		b, _ := json.Marshal(map[string]string{"service": unit})
		if _, err := s.sudo(ctx, restartTimeout, b, PostInstallWrapper, "--package", owner, "restart-service"); err != nil {
			return err
		}
		s.Log.Info("packages: music player restarted", "unit", unit, "package", owner)
		return nil
	}
	if !s.active(ctx, "systemctl", "--user", "is-active", "--quiet", unit) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()
	if out, err := s.Run(ctx, nil, "systemctl", "--user", "restart", unit); err != nil {
		if text := strings.TrimSpace(string(out)); text != "" {
			return errors.New(text)
		}
		return err
	}
	s.Log.Info("packages: music player restarted", "unit", unit, "user", true)
	return nil
}

func (s *System) active(ctx context.Context, name string, args ...string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.Run(ctx, nil, name, args...)
	return err == nil
}

// dropinOwner is the package whose managed drop-in configures unit ("" for none).
func (s *System) dropinOwner(unit string) string {
	b, err := os.ReadFile(filepath.Join(s.dropinRoot(), unit+".service.d", "jarvis.conf"))
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(b), "\n")
	owner, ok := strings.CutPrefix(first, managedByPrefix)
	owner = strings.TrimSpace(owner)
	if !ok || !nameRE.MatchString(owner) {
		return ""
	}
	return owner
}
