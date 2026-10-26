package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/pinode"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// hatFlags drive the ReSpeaker 2-mics HAT's LEDs and button (Pi nodes; go-pi-node.md §3.11).
type hatFlags struct {
	mode       string
	brightness int
	noLEDs     bool
	noLEDIdle  bool
	poweroff   bool
}

func addHatFlags(fs *flag.FlagSet) *hatFlags {
	h := &hatFlags{}
	fs.StringVar(&h.mode, "hat", "auto", `ReSpeaker 2-mics HAT LEDs and button: "auto" (a Raspberry Pi with the HAT), "on" or "off"`)
	fs.IntVar(&h.brightness, "led-brightness", 100, "HAT LED brightness, percent (the Python node's led_brightness_percent)")
	fs.BoolVar(&h.noLEDs, "no-leds", false, "keep the HAT LEDs dark (the Python node's led_enabled false)")
	fs.BoolVar(&h.noLEDIdle, "no-led-idle", false, "keep the HAT LEDs dark while idle: they light only when responding (wake, listening, thinking, speaking, errors, alerts)")
	fs.BoolVar(&h.poweroff, "button-poweroff", true, "holding the HAT button 3 s powers the Pi off (sudo -n systemctl poweroff), as the Python node does")
	return h
}

// hatDevices is the HAT in use: LEDs shown the voice state, and the button. On a Pi without the
// HAT it is the ACT LED alone (act; no button).
type hatDevices struct {
	leds      *hat.LEDs
	button    *hat.Button
	closeLine func() error
	act       bool
}

// The HAT's probes and the ACT LED's directories (tests point them at fakes).
var (
	hatDetected = hat.Detected
	openStrip   = hat.OpenStrip
	actDirs     = hat.ACTDirs
)

// openHat opens the LEDs and the button, whose short press runs press (nil: logged only). With
// no HAT chain (absent, or its LEDs unusable) it falls back to the Pi's ACT LED (§3.11). It
// returns nil (and logs why) when the HAT is off, or neither is usable: the agent runs without.
func openHat(f *hatFlags, press func(leds *hat.LEDs), log *slog.Logger) *hatDevices {
	switch f.mode {
	case "off":
		return nil
	case "auto":
		if !hatDetected() {
			return openACT(f, log)
		}
	case "on":
	default:
		log.Error("hat: --hat must be auto, on or off", "got", f.mode)
		return nil
	}
	strip, err := openStrip(hat.SPIDevice)
	if err != nil {
		log.Warn("hat: LEDs unavailable", "err", err)
		return openACT(f, log)
	}
	h := &hatDevices{leds: hat.NewLEDs(strip, log)}
	h.leds.SetBrightness(f.brightness)
	h.leds.SetIdleDark(f.noLEDIdle)
	h.leds.SetEnabled(!f.noLEDs)
	h.button = &hat.Button{LEDs: h.leds, Log: log,
		OnShortPress: func() {
			if press == nil {
				log.Info("hat: button pressed (no alert queue to read)")
				return
			}
			press(h.leds)
		}}
	if f.poweroff {
		h.button.OnHold = func() {
			if out, err := exec.Command("sudo", "-n", "systemctl", "poweroff").CombinedOutput(); err != nil {
				log.Error("hat: poweroff failed", "err", err, "output", string(out))
			}
		}
	}
	if h.closeLine, err = hat.WatchButton(h.button); err != nil {
		// The Python node may hold the line while both run side by side.
		log.Warn("hat: button unavailable", "err", err)
	}
	log.Info("hat: ReSpeaker HAT LEDs on", "button", h.closeLine != nil, "brightness", f.brightness)
	return h
}

// openACT shows the voice state on the Pi's activity LED (hat.NewACTLEDs), or returns nil when
// there is none this user may drive.
func openACT(f *hatFlags, log *slog.Logger) *hatDevices {
	dir := hat.FindACT(actDirs...)
	if dir == "" {
		return nil
	}
	leds, err := hat.NewACTLEDs(dir, log)
	if err != nil {
		log.Warn("hat: ACT LED unavailable", "err", err)
		return nil
	}
	leds.SetEnabled(!f.noLEDs) // brightness has no effect on the ACT LED
	leds.SetIdleDark(f.noLEDIdle)
	log.Info("hat: no HAT, showing state on the ACT LED", "dir", dir)
	return &hatDevices{leds: leds, act: true}
}

func (h *hatDevices) Close() error {
	if h == nil {
		return nil
	}
	if h.button != nil {
		h.button.Close()
	}
	var errs []error
	if h.closeLine != nil {
		errs = append(errs, h.closeLine())
	}
	return errors.Join(append(errs, h.leds.Close())...)
}

// wakeFlash is how long the LEDs show wake_detected (purple) when a turn starts: the Python node
// shows it while the wake acknowledgement plays, then listening (green). The chime is 210 ms.
const wakeFlash = 300 * time.Millisecond

// errorFlash is how long a failed turn stays red; Python clears it after speaking the error.
const errorFlash = 1500 * time.Millisecond

// ledCue shows the voice pipeline's state on the HAT LEDs with the Python node's patterns.
type ledCue struct {
	leds *hat.LEDs
	log  *slog.Logger // debug: each state shown; nil: quiet
	// after schedules the end of the wake flash (time.AfterFunc; tests fire it themselves);
	// errorFor is how long an error stays red (errorFlash).
	after    func(time.Duration, func()) *time.Timer
	errorFor time.Duration

	mu   sync.Mutex
	last voice.Cue
	seq  int
}

func (c *ledCue) SetState(cue voice.Cue, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.last
	c.last = cue
	c.seq++
	if c.log != nil {
		c.log.Debug("hat: voice state", "cue", cue)
	}
	switch cue {
	case voice.CueListening:
		if prev == voice.CueIdle || prev == voice.CueError {
			seq := c.seq
			c.leds.SetTransient(hat.WakeDetected)
			after := c.after
			if after == nil {
				after = time.AfterFunc
			}
			after(wakeFlash, func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.seq == seq {
					c.leds.SetTransient(hat.Listening)
				}
			})
			return
		}
		c.leds.SetTransient(hat.Listening)
	case voice.CueFollowUp:
		c.leds.SetTransient(hat.Listening) // Python: the follow-up window shows listening
	case voice.CueThinking:
		c.leds.SetTransient(hat.Thinking)
	case voice.CueSpeaking:
		c.leds.SetTransient(hat.Speaking)
	case voice.CueError:
		c.leds.Preview(hat.Error, cmp.Or(c.errorFor, errorFlash))
	default:
		c.leds.SetTransient("")
	}
}

func (c *ledCue) Level(float32) {}

// indicators fans the pipeline's state out to several indicators (the island and the LEDs).
type indicators []voice.Indicator

func (is indicators) SetState(c voice.Cue, w time.Duration) {
	for _, i := range is {
		i.SetState(c, w)
	}
}

func (is indicators) Level(v float32) {
	for _, i := range is {
		i.Level(v)
	}
}

// addIndicator adds i to the stack's indicators.
func (v *voiceStack) addIndicator(i voice.Indicator) {
	switch cur := v.indicator.(type) {
	case nil:
		v.indicator = i
	case indicators:
		v.indicator = append(cur, i)
	default:
		v.indicator = indicators{cur, i}
	}
}

// hatCmd (not in the usage: a development aid) drives the HAT by hand.
//
//	jarvis-agent hat pattern NAME [--for 3s]   # show one LED pattern, then off
//	jarvis-agent hat cycle [--for 1s]          # every pattern in turn
//	jarvis-agent hat button [--for 30s]        # log presses; holding never powers off here
func hatCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jarvis-agent hat pattern NAME | cycle | button [--for D]")
	}
	fs := flag.NewFlagSet("hat", flag.ExitOnError)
	dur := fs.Duration("for", 3*time.Second, "how long")
	sub, rest := args[0], args[1:]
	name := ""
	if sub == "pattern" && len(rest) > 0 {
		name, rest = rest[0], rest[1:]
	}
	_ = fs.Parse(rest)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	log := logger()
	strip, err := hat.OpenStrip(hat.SPIDevice)
	if err != nil {
		return err
	}
	leds := hat.NewLEDs(strip, log)
	defer leds.Close()
	wait := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	switch sub {
	case "pattern":
		if !hat.Known(name) {
			return fmt.Errorf("unknown pattern %q", name)
		}
		leds.SetTransient(name)
		log.Info("hat: showing", "pattern", name, "for", *dur)
		wait(*dur)
	case "cycle":
		for _, n := range []string{hat.Normal, hat.WakeDetected, hat.Listening, hat.Thinking, hat.Speaking,
			hat.Error, hat.NotForMe, hat.Alert, hat.Muted, hat.ShutdownWarning, hat.Setup, hat.Off} {
			leds.SetTransient(n)
			log.Info("hat: showing", "pattern", n)
			if !wait(*dur) {
				break
			}
		}
	case "button":
		b := &hat.Button{LEDs: leds, Log: log,
			OnShortPress: func() { log.Info("hat: short press") },
			OnHold:       func() { log.Info("hat: hold (poweroff is not run by this command)") }}
		closeLine, err := hat.WatchButton(b)
		if err != nil {
			return err
		}
		defer closeLine()
		log.Info("hat: watching the button", "for", *dur)
		wait(*dur)
		b.Close()
	default:
		return fmt.Errorf("unknown hat command %q", sub)
	}
	return nil
}

// piState is the node settings the Pi starts with: its flags, after agent.json.
func piState(vf *voiceFlags, hf *hatFlags) pinode.State {
	return pinode.State{WakeThreshold: vf.threshold, WakeAuto: vf.wakeAuto, WakeAck: !vf.noChimes, FollowUpListen: vf.followUp,
		FollowUpSilence: vf.followSilence, LEDEnabled: hf.mode != "off" && !hf.noLEDs, LEDIdle: !hf.noLEDIdle, LEDBrightness: hf.brightness,
		SilenceThreshold: vf.silenceThreshold, SilenceSecs: vf.listenSilence,
		FollowUpMinAfterOnset: vf.followMinAfterOnset, FollowUpMinSpeech: vf.followMinSpeech}
}

// piAudio is the speaker's volume as the settings verbs set it and the snapshot reports it
// (*sdkhost.Audio).
type piAudio interface {
	pinode.Audio
	Volume(ctx context.Context) (int, error)
	Muted() bool
}

// wirePiNode gives the runner the Pi's verbs (update_node_config, preview_led_pattern,
// measure_ambient_noise; internal/agent/pinode): changes go to the HAT's LEDs (hd, nil
// without one), to the voice pipeline through live, to the speaker through audio (nil: none),
// and into agent.json at path. It returns the settings snapshot's node_config, which reports them,
// and the settings in effect (the maintenance restart reads them: mf is its flags).
func wirePiNode(r *node.Runner, vf *voiceFlags, hf *hatFlags, mf *maintFlags, hd *hatDevices, live *voice.Live, audio *sdkhost.Audio,
	path string, log *slog.Logger) (func() map[string]any, func() pinode.State) {
	st := piState(vf, hf)
	st.Maintenance = mf.settings()
	pn := pinode.New(st, path, log)
	var pa piAudio
	if audio != nil {
		pn.Audio, pa = audio, audio
	}
	if hd != nil && hd.leds != nil {
		pn.LEDs = hd.leds
	}
	if voice.Supported {
		pn.Voice = live
	}
	verbs := map[string]func(context.Context, *node.CC, map[string]any){}
	for name, v := range pn.Verbs() {
		verbs[name] = func(ctx context.Context, cc *node.CC, det map[string]any) { v(ctx, cc, det) }
	}
	addVerbs(r, verbs)
	return piNodeConfig(pn.State, vf.bargeIn, hd, pa, hat.Detected()), pn.State
}

// piNodeConfig is the Pi profile's settings-snapshot node_config: the HAT (detected: hat.Detected;
// the LEDs and button this agent actually opened) and the voice and LED settings it runs with
// now (state: the flags, then the app's changes; bargeIn: the flag, not changed live yet), under
// the Python node's keys (settings_snapshot_service.py build_snapshot), and the speaker's volume
// and mute (audio; nil or unreadable: left out, as Python's). The RSS ceiling is the one in
// effect (the default resolved from the RAM). Keys the Go node has no setting for yet (min/max
// record seconds, barge-in thresholds, update consent) are left out.
func piNodeConfig(state func() pinode.State, bargeIn bool, hd *hatDevices, audio piAudio, detected bool) func() map[string]any {
	return func() map[string]any {
		chain := hd != nil && hd.leds != nil && !hd.act // the ACT LED is no HAT
		st := state()
		nc := map[string]any{
			"wake_word_threshold":                   st.WakeThreshold,
			"wake_word_threshold_auto":              st.WakeAuto,
			"wake_ack_audio_enabled":                st.WakeAck,
			"follow_up_listen_seconds":              st.FollowUpListen,
			"follow_up_silence_duration":            st.FollowUpSilence,
			"barge_in_enabled":                      bargeIn,
			"silence_threshold":                     int(st.SilenceThreshold),
			"silence_duration":                      st.SilenceSecs,
			"follow_up_min_record_after_onset_secs": st.FollowUpMinAfterOnset,
			"follow_up_min_speech_secs":             st.FollowUpMinSpeech,
			"maintenance_restart_enabled":           st.Maintenance.Enabled,
			"maintenance_restart_at_time":           st.Maintenance.At,
			"led_enabled":                           st.LEDEnabled,
			"led_idle_enabled":                      st.LEDIdle,
			"led_brightness_percent":                st.LEDBrightness,
			"hardware": map[string]any{
				"hat_detected":        detected || chain,
				"led_chain_available": chain,
				"button_available":    chain && hd.closeLine != nil,
			},
		}
		ram, _ := memTotalMB()
		nc["maintenance_restart_rss_ceiling_mb"] = st.Maintenance.Ceiling(ram)
		if audio != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if pct, err := audio.Volume(ctx); err == nil {
				nc["volume_percent"] = pct
			}
			nc["is_muted"] = audio.Muted()
		}
		return nc
	}
}
