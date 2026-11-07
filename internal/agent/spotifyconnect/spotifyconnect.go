// Package spotifyconnect runs go-librespot (https://github.com/devgianlu/go-librespot) as a supervised Spotify
// Connect speaker on a node with no screen. A display node gets the same receiver from internal/agent/display,
// which also shows what is playing; this package is the headless counterpart. go-librespot is GPL-3.0, so it
// runs as its own program and is never linked in. Spotify Premium is required and the client is unofficial.
package spotifyconnect

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Port is go-librespot's default API port. The music package already pauses and ducks a player there, so
// using it makes voice takeover and ducking work for this receiver without any further setting.
const Port = 3678

// Program is the binary looked up on PATH.
const Program = "go-librespot"

// ErrNotInstalled means go-librespot is not on PATH. The caller logs it once; nothing is retried.
var ErrNotInstalled = errors.New("go-librespot is not installed")

// WriteConfig writes go-librespot's config.yml: a speaker called name, joined from the Spotify app (zeroconf,
// remembered after the first connection), PulseAudio output, and the API on loopback only.
func WriteConfig(dir, name string, port int) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	quoted, err := json.Marshal(name) // a JSON string is a valid YAML double-quoted scalar
	if err != nil {
		return err
	}
	config := "device_name: " + string(quoted) + "\ndevice_type: speaker\naudio_backend: pulseaudio\nzeroconf_enabled: true\n" +
		"credentials:\n  type: zeroconf\n  zeroconf:\n    persist_credentials: true\n" +
		"server:\n  enabled: true\n  address: 127.0.0.1\n  port: " + strconv.Itoa(port) + "\n"
	return os.WriteFile(filepath.Join(dir, "config.yml"), []byte(config), 0600)
}

// Receiver supervises one go-librespot. The zero value is not usable; set Dir and Name.
type Receiver struct {
	Dir  string // config and remembered pairing live here (mode 0700)
	Name string // the speaker's name in the Spotify app
	Port int    // 0 means Port
	Log  *slog.Logger

	// Command builds the process; nil runs go-librespot with --config_dir Dir. Tests replace it.
	Command func(ctx context.Context) *exec.Cmd
	// Backoff is the wait after an exit, doubling to MaxBackoff. Zero values mean 2s and 60s.
	Backoff, MaxBackoff time.Duration
}

// Run starts the receiver and restarts it when it exits, until ctx ends. A receiver that keeps failing backs off
// instead of spinning, and one that ran for a while starts over from the short wait. It returns ErrNotInstalled
// at once when go-librespot is missing, and ctx.Err() when stopped.
func (r *Receiver) Run(ctx context.Context) error {
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	command := r.Command
	if command == nil {
		if _, err := exec.LookPath(Program); err != nil {
			return ErrNotInstalled
		}
		command = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, Program, "--config_dir", r.Dir) }
	}
	port := r.Port
	if port == 0 {
		port = Port
	}
	if err := WriteConfig(r.Dir, r.Name, port); err != nil {
		return err
	}
	wait, max := r.Backoff, r.MaxBackoff
	if wait <= 0 {
		wait = 2 * time.Second
	}
	if max <= 0 {
		max = time.Minute
	}
	next := wait
	for {
		started := time.Now()
		cmd := command(ctx)
		err := cmd.Run()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > 5*max/6 { // it was healthy for a while: a fresh failure starts the wait over
			next = wait
		}
		log.Warn("spotify connect receiver exited; restarting", "err", err, "in", next)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(next):
		}
		if next *= 2; next > max {
			next = max
		}
	}
}
