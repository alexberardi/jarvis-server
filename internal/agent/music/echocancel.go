package music

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
)

// Echo cancellation while the node's own music plays (17b; N:core/echo_cancel.py, reworked for
// the Go node's capture path, decision 2026-10-22-pi-node-echo-cancel.md). N: moved a Pulse
// capture stream onto jarvis_ec_source, but the node captures straight from the HAT through
// ALSA dsnoop and Pulse has no mic source (install-pi.sh: the HAT is playback only), so N:'s
// engage never found a stream to move. Here Pulse gets a source on the same dsnoop, the
// webrtc canceller runs between it and the HAT's sink, and the voice pipeline reads the
// cancelled source through asound.conf's pulse PCM jarvis_ec until the music stops.
//
// Like the rest of this package everything is best-effort: a step that fails undoes what this
// engage loaded and leaves capture on the plain mic, and the next try waits for the next start
// of the music.
const (
	// MicSource is Pulse's source on the HAT's dsnoop, the canceller's master.
	MicSource = "jarvis_mic_raw"
	// ECSource is the cancelled mic; ECSink the sink the music plays into while engaged (it
	// forwards to the HAT and is what the canceller subtracts).
	ECSource = "jarvis_ec_source"
	ECSink   = "jarvis_ec_sink"
	// ECCapture is the ALSA PCM (type pulse, device jarvis_ec_source) the pipeline reads.
	ECCapture = "jarvis_ec"
	// DefaultMic and DefaultSink are install-pi.sh's capture PCM and HAT sink.
	DefaultMic  = "dsnoopmic"
	DefaultSink = "jarvis_output"
)

// Capture is the voice pipeline's microphone (device.Switch): Use moves it to another ALSA
// device between reads, "" back to Base.
type Capture interface {
	Use(name string)
	Base() string
}

// EchoCancel loads and unloads the canceller around the node's music. Safe from any goroutine.
type EchoCancel struct {
	Run Run
	Log *slog.Logger
	// Mic is the ALSA device Pulse's source opens; it must be the pipeline's own input, or there
	// is nothing to cancel for (a wav: input, a USB mic). Sink is the real sink the canceller
	// plays to.
	Mic, Sink string
	// Capture is switched to ECCapture while engaged; nil: no echo cancellation.
	Capture Capture

	mu          sync.Mutex
	engaged     bool
	gaveUp      bool     // an engage failed or the capture fell back: no retry until Rearm
	modules     []string // the module indexes this engage loaded, in load order
	prevDefault string   // the default sink before the engage
}

// NewEchoCancel is the Pi's canceller over the real pactl, switching capture.
func NewEchoCancel(capture Capture, log *slog.Logger) *EchoCancel {
	return &EchoCancel{Run: ExecRun, Log: log, Mic: DefaultMic, Sink: DefaultSink, Capture: capture}
}

func (e *EchoCancel) log() *slog.Logger { return logger(e.Log) }

// Engaged: the music plays through ECSink and the pipeline hears ECSource.
func (e *EchoCancel) Engaged() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.engaged
}

// Engage loads the canceller, makes ECSink the default sink, moves the players' streams onto it
// and switches capture (enable_echo_cancel). Never call it while a recording runs. False: not
// engaged (not applicable, failed now or earlier since the last Rearm).
func (e *EchoCancel) Engage(ctx context.Context, trigger string) bool {
	if e == nil || e.Capture == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.engaged {
		return true
	}
	if e.gaveUp || e.Capture.Base() != e.Mic {
		return false
	}
	fail := func(step string, err error) bool {
		e.unloadLocked(ctx)
		e.gaveUp = true
		e.log().Warn("music: echo cancel not engaged; capture stays on the plain mic until the music starts again",
			"step", step, "err", err, "trigger", trigger)
		return false
	}
	idx, err := e.load(ctx, "module-alsa-source", "device="+e.Mic, "source_name="+MicSource,
		"rate=16000", "channels=1", "source_properties=device.description=jarvis-mic-raw")
	if err != nil {
		return fail("load_mic_source", err)
	}
	e.modules = append(e.modules, idx)
	// The webrtc canceller's own gain controls stay off: the HAT's PGA is the music profile's.
	idx, err = e.load(ctx, "module-echo-cancel", "aec_method=webrtc",
		`aec_args="analog_gain_control=0 digital_gain_control=0"`,
		"source_master="+MicSource, "sink_master="+e.Sink, "source_name="+ECSource, "sink_name="+ECSink)
	if err != nil {
		return fail("load_echo_cancel", err)
	}
	e.modules = append(e.modules, idx)
	e.prevDefault = e.Sink
	if out, err := e.Run(ctx, "pactl", "get-default-sink"); err == nil && strings.TrimSpace(out) != "" &&
		strings.TrimSpace(out) != ECSink {
		e.prevDefault = strings.TrimSpace(out)
	}
	// New streams (the player a deferred play just started, the replies) land on ECSink too.
	if _, err := e.Run(ctx, "pactl", "set-default-sink", ECSink); err != nil {
		e.log().Warn("music: echo cancel: default sink not moved; new streams skip the canceller", "err", err)
	}
	moved := e.moveLocked(ctx, func(sinkInput) bool { return true }, ECSink)
	e.Capture.Use(ECCapture)
	e.engaged = true
	e.log().Info("music: echo cancel engaged", "modules", e.modules, "moved", moved, "capture", ECCapture,
		"trigger", trigger)
	return true
}

// Disengage switches capture back, moves the streams off ECSink and unloads what Engage
// loaded (disable_echo_cancel).
func (e *EchoCancel) Disengage(ctx context.Context, trigger string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.engaged {
		return
	}
	e.disengageLocked(ctx, trigger)
}

func (e *EchoCancel) disengageLocked(ctx context.Context, trigger string) {
	e.Capture.Use("")
	if _, err := e.Run(ctx, "pactl", "set-default-sink", e.prevDefault); err != nil {
		e.log().Warn("music: echo cancel: default sink not restored", "sink", e.prevDefault, "err", err)
	}
	// Only what plays on ECSink: a stream a duck parked on the null sink stays parked.
	var ec int
	ecOK := false
	if out, err := e.Run(ctx, "pactl", "list", "short", "sinks"); err == nil {
		ec, ecOK = sinkIndex(out, ECSink)
	}
	moved := e.moveLocked(ctx, func(it sinkInput) bool { return ecOK && it.Sink != nil && *it.Sink == ec }, e.prevDefault)
	modules := e.modules
	e.unloadLocked(ctx)
	e.engaged = false
	e.log().Info("music: echo cancel disengaged", "unloaded", modules, "moved", moved, "restore_sink", e.prevDefault,
		"trigger", trigger)
}

// GiveUp is the capture's fallback (it failed or went silent): disengage and stay off until
// Rearm. The capture is already back on the plain mic.
func (e *EchoCancel) GiveUp(ctx context.Context, reason string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gaveUp = true
	if e.engaged {
		e.disengageLocked(ctx, "capture_"+reason)
	}
}

// Rearm lets the next music start engage again (the music stopped).
func (e *EchoCancel) Rearm() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.gaveUp = false
	e.mu.Unlock()
}

// Cleanup unloads a canceller a crash left loaded (once at start, before the pipeline opens the
// mic): the modules named for it, the canceller first.
func (e *EchoCancel) Cleanup(ctx context.Context) {
	if e == nil {
		return
	}
	out, err := e.Run(ctx, "pactl", "list", "short", "modules")
	if err != nil {
		return
	}
	var ec, src []string
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 3 {
			continue
		}
		switch {
		case f[1] == "module-echo-cancel" && strings.Contains(f[2], "sink_name="+ECSink):
			ec = append(ec, f[0])
		case f[1] == "module-alsa-source" && strings.Contains(f[2], "source_name="+MicSource):
			src = append(src, f[0])
		}
	}
	stale := append(ec, src...)
	for _, idx := range stale {
		_, _ = e.Run(ctx, "pactl", "unload-module", idx)
	}
	if len(stale) > 0 {
		e.log().Warn("music: echo cancel was left loaded (a crash mid-music?); unloaded", "modules", stale)
	}
}

func (e *EchoCancel) load(ctx context.Context, module string, args ...string) (string, error) {
	out, err := e.Run(ctx, "pactl", append([]string{"load-module", module}, args...)...)
	if err != nil {
		return "", err
	}
	idx := strings.TrimSpace(out)
	if _, err := strconv.Atoi(idx); err != nil {
		return "", err
	}
	return idx, nil
}

// unloadLocked unloads this engage's modules, last loaded first.
func (e *EchoCancel) unloadLocked(ctx context.Context) {
	for i := len(e.modules) - 1; i >= 0; i-- {
		if _, err := e.Run(ctx, "pactl", "unload-module", e.modules[i]); err != nil {
			e.log().Warn("music: echo cancel: module not unloaded", "module", e.modules[i], "err", err)
		}
	}
	e.modules = nil
}

// moveLocked moves the players' streams that match to sink.
func (e *EchoCancel) moveLocked(ctx context.Context, match func(sinkInput) bool, sink string) []string {
	items, _ := listSinkInputs(ctx, e.Run)
	var moved []string
	for _, it := range items {
		if it.Index == nil || !isPlayer(it.binary()) || !match(it) {
			continue
		}
		if _, err := e.Run(ctx, "pactl", "move-sink-input", it.id(), sink); err == nil {
			moved = append(moved, it.id())
		}
	}
	return moved
}

// sinkIndex finds name's index in pactl list short sinks.
func sinkIndex(out, name string) (int, bool) {
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 2 && f[1] == name {
			n, err := strconv.Atoi(f[0])
			return n, err == nil
		}
	}
	return 0, false
}
