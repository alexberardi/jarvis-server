package hat

// The HAT's TLV320AIC3104 codec self-heal, ported from N:utils/audio_volume.py
// (ensure_adc_hpf_enabled, ensure_output_baseline). The installer sets the mixer and
// alsactl-stores it, but the restore doesn't always replay every control after a boot
// (enumerated controls across kernel updates, the codec's driver), so the node re-asserts
// both on every start. Each step is idempotent and never fatal: unreadable state is skipped
// and retried on the next start.

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Card is the HAT's ALSA card id (seeed-voicecard).
const Card = "seeed2micvoicec"

// Run runs a command and returns its stdout; a non-zero exit is an error.
type Run func(ctx context.Context, name string, args ...string) (string, error)

// ExecRun runs the real command, each with the Python node's 2 s timeout.
func ExecRun(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return string(out), &stderrError{err, strings.TrimSpace(string(ee.Stderr))}
	}
	return string(out), err
}

type stderrError struct {
	err    error
	stderr string
}

func (e *stderrError) Error() string { return e.err.Error() + ": " + e.stderr }

// HealCodec runs both self-heals. Call it only when the HAT is present (Detected).
func HealCodec(ctx context.Context, run Run, log *slog.Logger) {
	EnsureADCHPF(ctx, run, log)
	EnsureOutputBaseline(ctx, run, log)
}

const (
	hpfControl = "ADC HPF Cut-off"
	hpfTarget  = 1 // 0.0045xFs: blocks the DC bias, keeps speech
)

var reHPF = regexp.MustCompile(`(?m)^\s*:\s*values=(\d+)`)

// EnsureADCHPF enables the ADC high-pass filter when it is disabled (item 0). A higher
// cutoff a user picked is left alone.
func EnsureADCHPF(ctx context.Context, run Run, log *slog.Logger) {
	out, err := run(ctx, "amixer", "-c", Card, "cget", "name="+hpfControl)
	m := reHPF.FindStringSubmatch(out)
	if err != nil || m == nil {
		log.Info("hat: ADC HPF state unreadable, skipping self-heal (codec may be busy)", "err", err)
		return
	}
	cur, _ := strconv.Atoi(m[1])
	if cur >= 1 {
		return
	}
	log.Warn("hat: ADC HPF was disabled, enabling it to remove DC bias from recordings",
		"previous_item", cur, "target_item", hpfTarget)
	if _, err := run(ctx, "amixer", "-c", Card, "cset", "name="+hpfControl, strconv.Itoa(hpfTarget)); err != nil {
		log.Error("hat: amixer cset for ADC HPF failed", "err", err)
	}
}

// outputBaseline mirrors the installer's mixer baseline: line out on at the calibrated
// level, the headphone path muted.
var outputBaseline = [][]string{
	{"PCM", "100%", "unmute"},
	{"Line", "2", "unmute"},
	{"Line DAC", "115", "unmute"},
	{"Left Line Mixer DACL1", "on"},
	{"Right Line Mixer DACR1", "on"},
	{"HP", "0", "mute"},
	{"HP DAC", "0", "mute"},
	{"HPCOM", "0", "mute"},
	{"HPCOM DAC", "0", "mute"},
}

// Drift floors: Line 1 lets a user hand-tune one notch below the baseline's 2; Line DAC 100
// (about -9 dB) is "loud enough".
const (
	lineMin    = 1
	lineDACMin = 100
)

// AlsaStore persists the mixer (alsactl store) through a NOPASSWD sudo wrapper the
// installer adds, so the next boot restores the healed values.
const AlsaStore = "/usr/local/sbin/jarvis-alsa-store"

// EnsureOutputBaseline re-applies the output mixer baseline when Line or Line DAC is below
// its floor or the headphone path is unmuted, then persists it.
func EnsureOutputBaseline(ctx context.Context, run Run, log *slog.Logger) {
	read := func(control string) (int, switchState, bool) {
		out, err := run(ctx, "amixer", "-c", Card, "sget", control)
		if err != nil {
			return 0, noSwitch, false
		}
		return parsePlayback(out)
	}
	line, _, ok1 := read("Line")
	lineDAC, _, ok2 := read("Line DAC")
	_, hp, ok3 := read("HP")
	_, hpcom, ok4 := read("HPCOM")
	if !ok1 || !ok2 || !ok3 || !ok4 || hp == noSwitch || hpcom == noSwitch {
		log.Info("hat: output mixer state unreadable, skipping self-heal (codec may be busy)")
		return
	}
	if line >= lineMin && lineDAC >= lineDACMin && hp == switchOff && hpcom == switchOff {
		return
	}
	log.Warn("hat: output mixer below the calibrated baseline, re-asserting",
		"line", line, "line_dac", lineDAC, "hp_unmuted", hp == switchOn, "hpcom_unmuted", hpcom == switchOn)
	for _, c := range outputBaseline {
		if _, err := run(ctx, "amixer", append([]string{"-c", Card, "sset"}, c...)...); err != nil {
			log.Error("hat: output baseline amixer command failed", "control", c[0], "err", err)
		}
	}
	if _, err := run(ctx, "sudo", "-n", AlsaStore); err != nil {
		log.Warn("hat: jarvis-alsa-store failed, the output baseline won't survive a reboot", "err", err)
	}
}

type switchState int

const (
	noSwitch switchState = iota
	switchOn
	switchOff
)

var (
	rePlayback = regexp.MustCompile(`Playback (\d+)`)
	// The switch is the line's last token, so the dB column can't be read as one.
	reSwitch = regexp.MustCompile(`\[(on|off)\]\s*$`)
)

// parsePlayback reads the raw value and switch from the first "Playback … %" line of
// amixer sget, e.g. "  Front Left: Playback 0 [0%] [0.00dB] [on]".
func parsePlayback(out string) (int, switchState, bool) {
	for _, l := range strings.Split(out, "\n") {
		if !strings.Contains(l, "Playback") || !strings.Contains(l, "%") {
			continue
		}
		m := rePlayback.FindStringSubmatch(l)
		if m == nil {
			return 0, noSwitch, false
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, noSwitch, false
		}
		sw := noSwitch
		if s := reSwitch.FindStringSubmatch(l); s != nil {
			sw = map[string]switchState{"on": switchOn, "off": switchOff}[s[1]]
		}
		return v, sw, true
	}
	return 0, noSwitch, false
}
