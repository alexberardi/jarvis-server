package sdkhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runner runs one program and returns its stdout. Tests replace it; ExecRun runs it.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// ExecRun runs name with a 2 s limit, as N:utils/audio_volume's _run.
func ExecRun(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// outputSink is the HAT's PulseAudio sink install-pi.sh fixes (§6, 3b).
const outputSink = "jarvis_output"

// Audio is the Pi's volume and mute (N:utils/audio_volume.py): PulseAudio's default sink,
// else the HAT's jarvis_output, else ALSA's Master through amixer, plus every Bluetooth
// sink. The level is persisted first, so it survives a reboot even when PulseAudio isn't up
// yet, and it is what "volume up" counts from.
type Audio struct {
	Path string // volume.json in the node's state dir
	Run  Runner
	Log  *slog.Logger

	mu sync.Mutex
}

type volumeState struct {
	Percent *int `json:"volume_percent,omitempty"`
	Muted   bool `json:"is_muted"`
}

func (a *Audio) log() *slog.Logger {
	if a.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return a.Log
}

func (a *Audio) load() volumeState {
	var s volumeState
	if b, err := os.ReadFile(a.Path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (a *Audio) save(s volumeState) {
	b, _ := json.Marshal(s)
	tmp := a.Path + ".tmp"
	err := os.WriteFile(tmp, b, 0o600)
	if err == nil {
		err = os.Rename(tmp, a.Path)
	}
	if err != nil {
		a.log().Warn("audio: volume not persisted", "err", err)
	}
}

func clamp(p int) int { return max(0, min(100, p)) }

// apply runs the pactl command on the default sink, else jarvis_output, else the amixer one,
// and the pactl one on every Bluetooth sink; true when the speaker took it.
func (a *Audio) apply(ctx context.Context, pactl []string, amixer []string) bool {
	ok := false
	for _, sink := range []string{"@DEFAULT_SINK@", outputSink} {
		if _, err := a.Run(ctx, "pactl", append([]string{pactl[0], sink}, pactl[1:]...)...); err == nil {
			ok = true
			break
		}
	}
	if !ok {
		_, err := a.Run(ctx, "amixer", amixer...)
		ok = err == nil
	}
	if out, err := a.Run(ctx, "pactl", "list", "short", "sinks"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Split(line, "\t"); len(f) >= 2 && strings.HasPrefix(f[1], "bluez_sink.") {
				_, _ = a.Run(ctx, "pactl", append([]string{pactl[0], f[1]}, pactl[1:]...)...)
			}
		}
	}
	return ok
}

// SetVolume persists and applies pct (clamped to 0..100); false when no mixer took it.
func (a *Audio) SetVolume(ctx context.Context, pct int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setLocked(ctx, clamp(pct))
}

func (a *Audio) setLocked(ctx context.Context, pct int) bool {
	s := a.load()
	s.Percent = &pct
	a.save(s)
	if !a.apply(ctx, []string{"set-sink-volume", strconv.Itoa(pct) + "%"}, []string{"-q", "sset", "Master", strconv.Itoa(pct) + "%"}) {
		a.log().Warn("audio: no mixer took the volume; persisted only", "percent", pct)
		return false
	}
	a.log().Info("audio: volume set", "percent", pct)
	return true
}

var percentRE = regexp.MustCompile(`(\d+)%`)

// current is the persisted level, else the mixer's (the highest channel).
func (a *Audio) current(ctx context.Context) (int, error) {
	if s := a.load(); s.Percent != nil {
		return clamp(*s.Percent), nil
	}
	out, err := a.Run(ctx, "pactl", "get-sink-volume", "@DEFAULT_SINK@")
	if err != nil {
		if out, err = a.Run(ctx, "amixer", "sget", "Master"); err != nil {
			return 0, errors.New("the volume can't be read")
		}
	}
	best := -1
	for _, m := range percentRE.FindAllStringSubmatch(out, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > best {
			best = n
		}
	}
	if best < 0 {
		return 0, errors.New("the volume can't be read")
	}
	return clamp(best), nil
}

// Volume is the level in effect: the persisted one, else the mixer's.
func (a *Audio) Volume(ctx context.Context) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current(ctx)
}

// Muted is whether the speaker was last muted.
func (a *Audio) Muted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.load().Muted
}

// Adjust moves the volume by delta points and returns the new level.
func (a *Audio) Adjust(ctx context.Context, delta int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, err := a.current(ctx)
	if err != nil {
		return 0, err
	}
	pct := clamp(cur + delta)
	if !a.setLocked(ctx, pct) {
		return 0, errors.New("no mixer took the volume")
	}
	return pct, nil
}

// SetMuted mutes or unmutes the speaker; false when no mixer took it.
func (a *Audio) SetMuted(ctx context.Context, muted bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	flag, sw := "0", "unmute"
	if muted {
		flag, sw = "1", "mute"
	}
	ok := a.apply(ctx, []string{"set-sink-mute", flag}, []string{"-q", "sset", "Master", sw})
	if ok {
		s := a.load()
		s.Muted = muted
		a.save(s)
	} else {
		a.log().Warn("audio: no mixer took the mute", "muted", muted)
	}
	return ok
}

// Restore applies the persisted level and mute at start (nothing when none was set).
func (a *Audio) Restore(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.load()
	if s.Percent != nil {
		pct := strconv.Itoa(clamp(*s.Percent)) + "%"
		a.apply(ctx, []string{"set-sink-volume", pct}, []string{"-q", "sset", "Master", pct})
	}
	if s.Muted {
		a.apply(ctx, []string{"set-sink-mute", "1"}, []string{"-q", "sset", "Master", "mute"})
	}
}
