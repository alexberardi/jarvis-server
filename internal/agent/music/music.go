// Package music is the Pi node's music ducking and takeover, ported from N:core/music_control.py
// (go-pi-node.md §7 5.11).
//
//   - Ducking (wake window only): Duck when the wake word fires and again before each follow-up
//     listen, Restore once the turn is over. The players the music packages run get one of two
//     treatments. SIGSTOP class (mpv, ffplay, cvlc, vlc, spotifyd, librespot): the PulseAudio
//     sink-input moves to a null sink (a frozen process's uncorked stream would underrun and wedge
//     the real sink) and the process is SIGSTOPped. Mute-only class (shairport-sync, go-librespot,
//     mpd): the stream stays on the real sink at DuckPercent of its volume (0: muted), since
//     these answer a control protocol that a SIGSTOP would hang (N:'s 2026-08-15 incident).
//   - Takeover (permanent): before a music command's "play" runs, StopOthers stops every other
//     player that is producing audio, so two streams don't share the speaker.
//   - The capture gain profile: while the node's own music plays, the HAT's mic PGA drops to
//     MusicPGA (at 60% the ADC clips under music and the wake word goes deaf); it is restored when
//     the music stops. Changes happen only at duck and restore time, never mid-recording.
//   - Echo cancellation (17b, echocancel.go): while the node's own music plays, the music goes
//     through Pulse's webrtc canceller and the voice pipeline hears the cancelled mic. It follows
//     the cached "self playing" flag at restore time and when a deferred play starts (between
//     recordings), never at a duck: switching capture at the wake fire would lose the command.
//
// Everything is best-effort and never fatal: pactl, pkill or amixer failing (no PulseAudio, no
// HAT, a desktop) leaves the music as it was.
package music

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Run runs a program and returns its stdout; a non-zero exit is an error.
type Run func(ctx context.Context, name string, args ...string) (string, error)

// ExecRun runs the real command with N:'s 2 s limit.
func ExecRun(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// Gain is the mic's capture gain (the HAT codec's PGA), as an amixer percent.
type Gain interface {
	Get(ctx context.Context) (int, bool)
	Set(ctx context.Context, pct int) bool
}

var (
	// sigstopPlayers only read upstream and write to Pulse: pausing the process is safe.
	sigstopPlayers = []string{"mpv", "ffplay", "cvlc", "vlc", "spotifyd", "librespot"}
	// muteOnlyPlayers answer a control protocol (AirPlay, go-librespot's HTTP API, mpd's live
	// stream): never SIGSTOP them (N:'s CONTROL-API REACHABILITY invariant).
	muteOnlyPlayers = []string{"shairport-sync", "go-librespot", "mpd"}
	// pkillIntPlayers are stopped by SIGINT on a takeover; mpd and go-librespot by their APIs.
	pkillIntPlayers = []string{"mpv", "ffplay", "vlc", "cvlc", "spotifyd", "librespot", "shairport-sync"}
)

func isPlayer(b string) bool {
	return slices.Contains(sigstopPlayers, b) || slices.Contains(muteOnlyPlayers, b)
}

// NullSink parks the SIGSTOP class's streams during a duck.
const NullSink = "jarvis_duck_null"

// restoreSink is where parked streams go back to: the canceller's sink while it is engaged (N:),
// else the default sink.
func (d *Ducker) restoreSink() string {
	if d.EC.Engaged() {
		return ECSink
	}
	return "@DEFAULT_SINK@"
}

// Defaults are N:'s config defaults (music_duck_percent, music_pga_percent,
// capture_pga_normal_percent; the last is install-pi.sh's baseline).
const (
	DefaultDuckPercent = 20
	DefaultMusicPGA    = 49
	DefaultNormalPGA   = 49
)

// Ducker ducks, restores and takes over the node's music. Its methods are safe to call from any
// goroutine; the voice pipeline runs them in order off its own goroutine.
type Ducker struct {
	Run Run
	Log *slog.Logger
	// DuckPercent is the mute-only class's level during a duck; 0 or less mutes it instead.
	DuckPercent int
	// MusicPGA is the mic gain while the node's music plays (0 or less: no gain profile);
	// NormalPGA the resting gain restored when nothing was saved (boot after a crash).
	MusicPGA, NormalPGA int
	// Gain is the HAT's mic gain; nil (no HAT) leaves the gain alone.
	Gain Gain
	// EC is the echo canceller engaged while the node's own music plays; nil: none.
	EC *EchoCancel
	// MPDAddr and LibrespotURL are the takeover's control endpoints (defaults: N:'s).
	MPDAddr      string
	LibrespotURL string
	HTTP         *http.Client

	mu          sync.Mutex
	saved       map[string]int // mute-only sink-input id → its volume before the duck
	selfPlaying bool
	pgaEngaged  bool
	savedPGA    int // the gain before the music profile; -1: none saved
}

// New is a Ducker with N:'s defaults over the real commands.
func New(gain Gain, log *slog.Logger) *Ducker {
	return &Ducker{Run: ExecRun, Log: log, DuckPercent: DefaultDuckPercent, MusicPGA: DefaultMusicPGA,
		NormalPGA: DefaultNormalPGA, Gain: gain}
}

func (d *Ducker) log() *slog.Logger { return logger(d.Log) }

func logger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return l
}

// sinkInput is one entry of pactl -f json list sink-inputs.
type sinkInput struct {
	Index      *int                       `json:"index"`
	Corked     *bool                      `json:"corked"`
	Sink       *int                       `json:"sink"`
	Volume     map[string]json.RawMessage `json:"volume"`
	Properties map[string]any             `json:"properties"`
}

func (s sinkInput) id() string { return strconv.Itoa(*s.Index) }

// binary is the producing process's basename (Pulse reports an absolute path).
func (s sinkInput) binary() string {
	b, _ := s.Properties["application.process.binary"].(string)
	if b == "" {
		return ""
	}
	return path.Base(b)
}

// volume is the loudest channel's percent; false when pactl's shape isn't recognised.
func (s sinkInput) volume() (int, bool) {
	best, ok := 0, false
	for _, raw := range s.Volume {
		var ch struct {
			ValuePercent string `json:"value_percent"`
		}
		if json.Unmarshal(raw, &ch) != nil || !strings.HasSuffix(ch.ValuePercent, "%") {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(ch.ValuePercent, "%")))
		if err == nil && (!ok || v > best) {
			best, ok = v, true
		}
	}
	return best, ok
}

// sinkInputs lists Pulse's streams; failed is why the empty list is not "no players".
func (d *Ducker) sinkInputs(ctx context.Context) (items []sinkInput, failed string) {
	return listSinkInputs(ctx, d.Run)
}

func listSinkInputs(ctx context.Context, run Run) (items []sinkInput, failed string) {
	out, err := run(ctx, "pactl", "-f", "json", "list", "sink-inputs")
	if err != nil {
		return nil, err.Error()
	}
	if strings.TrimSpace(out) == "" {
		return nil, ""
	}
	if json.Unmarshal([]byte(out), &items) != nil {
		return nil, "parse_error"
	}
	return items, ""
}

func of(items []sinkInput, class []string) []sinkInput {
	var out []sinkInput
	for _, it := range items {
		if it.Index != nil && slices.Contains(class, it.binary()) {
			out = append(out, it)
		}
	}
	return out
}

func anyUncorked(items []sinkInput) bool {
	for _, it := range items {
		if isPlayer(it.binary()) && it.Corked != nil && !*it.Corked {
			return true
		}
	}
	return false
}

// IsPlaying: a known player has an uncorked stream (a daemon's process alone says nothing).
func (d *Ducker) IsPlaying(ctx context.Context) bool {
	items, _ := d.sinkInputs(ctx)
	return anyUncorked(items)
}

// SelfPlaying is the cached "the node's own music is playing", refreshed at every duck and set
// when a media command's deferred play starts.
func (d *Ducker) SelfPlaying() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.selfPlaying
}

// SetSelfPlaying sets that flag; its false→true edge engages the music gain profile and its
// true→false edge restores the normal gain. The echo canceller follows it. Never call it while a
// recording runs.
func (d *Ducker) SetSelfPlaying(ctx context.Context, on bool, trigger string) {
	d.setSelfPlaying(ctx, on, trigger)
	d.syncEC(ctx, trigger)
}

// setSelfPlaying is SetSelfPlaying without the echo canceller (a duck: the wake just fired).
func (d *Ducker) setSelfPlaying(ctx context.Context, on bool, trigger string) {
	d.mu.Lock()
	old := d.selfPlaying
	d.selfPlaying = on
	d.mu.Unlock()
	switch {
	case on && !old:
		d.engagePGA(ctx, trigger)
	case old && !on:
		d.restorePGA(ctx, trigger)
	}
}

func (d *Ducker) ok(ctx context.Context, name string, args ...string) bool {
	_, err := d.Run(ctx, name, args...)
	return err == nil
}

// ensureNullSink loads the duck's null sink once Pulse lacks it.
func (d *Ducker) ensureNullSink(ctx context.Context) {
	out, err := d.Run(ctx, "pactl", "list", "short", "sinks")
	if err != nil || strings.Contains(out, NullSink) {
		return
	}
	d.ok(ctx, "pactl", "load-module", "module-null-sink", "sink_name="+NullSink,
		"sink_properties=device.description=jarvis-duck-null")
}

// Duck silences or lowers the players now (pause_active_playback). It may run several times in
// one turn (the wake, then each follow-up): a stream's saved volume is read only once.
func (d *Ducker) Duck(ctx context.Context) {
	d.ensureNullSink(ctx)
	items, failed := d.sinkInputs(ctx)
	if failed != "" {
		d.log().Warn("music: duck: listing Pulse's streams failed; music may stay loud", "err", failed)
	}
	uncorked := anyUncorked(items)
	d.setSelfPlaying(ctx, uncorked, "duck_enumeration")
	if uncorked {
		// Retry a profile whose earlier engage failed, and cover a session started from a phone.
		d.engagePGA(ctx, "duck_active_players")
	}
	var parked, ducked, muted, stopped []string
	for _, it := range of(items, sigstopPlayers) {
		if d.ok(ctx, "pactl", "move-sink-input", it.id(), NullSink) {
			parked = append(parked, it.id())
		}
	}
	for _, it := range of(items, muteOnlyPlayers) {
		id, orig, have := it.id(), 0, false
		if d.DuckPercent > 0 {
			d.mu.Lock()
			if d.saved == nil {
				d.saved = map[string]int{}
			}
			if orig, have = d.saved[id]; !have {
				if orig, have = it.volume(); have {
					d.saved[id] = orig
				}
			}
			d.mu.Unlock()
		}
		if have {
			if d.ok(ctx, "pactl", "set-sink-input-volume", id, strconv.Itoa(d.DuckPercent)+"%") {
				ducked = append(ducked, id)
			}
			continue
		}
		// Duck depth 0, or a volume we couldn't read: mute (Restore always unmutes).
		if d.ok(ctx, "pactl", "set-sink-input-mute", id, "1") {
			muted = append(muted, id)
		}
	}
	for _, b := range sigstopPlayers {
		if d.ok(ctx, "pkill", "-STOP", "-x", b) {
			stopped = append(stopped, b)
		}
	}
	var unmatched []string
	for _, it := range items {
		if b := it.binary(); b != "" && !isPlayer(b) && !slices.Contains(unmatched, b) {
			unmatched = append(unmatched, b)
		}
	}
	slices.Sort(unmatched)
	d.log().Info("music: duck", "parked", parked, "ducked", ducked, "muted", muted, "duck_percent", d.DuckPercent,
		"sigstopped", stopped, "self_playing", d.SelfPlaying(), "streams", len(items), "unmatched", unmatched)
}

// Restore undoes Duck (resume_active_playback): SIGCONT first, then the streams back.
func (d *Ducker) Restore(ctx context.Context) {
	var resumed, moved, unmuted, unducked []string
	for _, b := range sigstopPlayers {
		if d.ok(ctx, "pkill", "-CONT", "-x", b) {
			resumed = append(resumed, b)
		}
	}
	items, _ := d.sinkInputs(ctx)
	restoreSink := d.restoreSink()
	for _, it := range of(items, sigstopPlayers) {
		if d.ok(ctx, "pactl", "move-sink-input", it.id(), restoreSink) {
			moved = append(moved, it.id())
		}
	}
	for _, it := range of(items, muteOnlyPlayers) {
		if d.ok(ctx, "pactl", "set-sink-input-mute", it.id(), "0") {
			unmuted = append(unmuted, it.id())
		}
	}
	// Drained first, so it is empty whatever happens next; a stream gone mid-turn just fails.
	d.mu.Lock()
	saved := d.saved
	d.saved = nil
	d.mu.Unlock()
	ids := make([]string, 0, len(saved))
	for id := range saved {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if d.ok(ctx, "pactl", "set-sink-input-volume", id, strconv.Itoa(saved[id])+"%") {
			unducked = append(unducked, id)
		}
	}
	d.log().Info("music: restore", "sigcont", resumed, "restore_sink", restoreSink, "moved", moved,
		"unmuted", unmuted, "unducked", unducked)
	// The turn is over: the canceller catches up with what the duck saw.
	d.syncEC(ctx, "restore")
}

// syncEC engages the echo canceller while the node's own music plays and disengages it once it
// stopped, which also lets the next start retry an engage that failed.
func (d *Ducker) syncEC(ctx context.Context, trigger string) {
	if d.EC == nil {
		return
	}
	if d.SelfPlaying() {
		d.EC.Engage(ctx, trigger)
		return
	}
	d.EC.Disengage(ctx, trigger)
	d.EC.Rearm()
}

// engagePGA drops the mic gain to the music profile, once (engage_music_pga).
func (d *Ducker) engagePGA(ctx context.Context, trigger string) {
	if d.MusicPGA <= 0 || d.Gain == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pgaEngaged {
		return
	}
	old, ok := d.Gain.Get(ctx)
	if !ok {
		return
	}
	if old == d.MusicPGA {
		// Already there (the default install): engaged, nothing to restore but the normal gain.
		d.pgaEngaged, d.savedPGA = true, -1
		return
	}
	if !d.Gain.Set(ctx, d.MusicPGA) {
		d.log().Warn("music: mic gain profile not engaged (amixer failed); the next duck retries",
			"old_percent", old, "target_percent", d.MusicPGA, "trigger", trigger)
		return
	}
	d.pgaEngaged, d.savedPGA = true, old
	d.log().Info("music: mic gain profile engaged", "old_percent", old, "new_percent", d.MusicPGA, "trigger", trigger)
}

// restorePGA puts the gain saved at engage back, else NormalPGA (restore_normal_pga).
func (d *Ducker) restorePGA(ctx context.Context, trigger string) {
	if d.Gain == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pgaEngaged {
		return
	}
	target := d.savedPGA
	if target < 0 {
		target = d.NormalPGA
	}
	d.pgaEngaged, d.savedPGA = false, -1
	if old, ok := d.Gain.Get(ctx); ok && old == target {
		return
	}
	if !d.Gain.Set(ctx, target) {
		d.log().Warn("music: mic gain not restored (amixer failed); it may stay at the music profile until the next start",
			"target_percent", target, "trigger", trigger)
		return
	}
	d.log().Info("music: mic gain restored", "new_percent", target, "trigger", trigger)
}

// NormalizePGA runs once at start (normalize_capture_pga_on_startup): a gain sitting exactly at
// the music profile, when that differs from the normal gain, is a crashed profile; put it back.
func (d *Ducker) NormalizePGA(ctx context.Context) {
	if d.Gain == nil || d.MusicPGA <= 0 || d.MusicPGA == d.NormalPGA {
		return
	}
	if cur, ok := d.Gain.Get(ctx); !ok || cur != d.MusicPGA {
		return
	}
	if d.Gain.Set(ctx, d.NormalPGA) {
		d.log().Warn("music: mic gain was left at the music profile (a crash mid-music?); restored",
			"old_percent", d.MusicPGA, "new_percent", d.NormalPGA)
	} else {
		d.log().Warn("music: mic gain is stuck at the music profile and the restore failed",
			"percent", d.MusicPGA, "target_percent", d.NormalPGA)
	}
}

// legacyTakeover maps music packages that predate the SDK's takes_over_playback_binary
// property to the player they control.
var legacyTakeover = map[string]string{
	"audacy":  "mpd",
	"spotify": "go-librespot",
	"music":   "shairport-sync", // jarvis-cmd-music-assistant
	"pandora": "mpv",
}

// TakeoverBinary is the player a command controls: its declared property, else the legacy map;
// "" is not a music command (_resolve_takeover_binary).
func TakeoverBinary(command, declared string) string {
	if declared != "" {
		return declared
	}
	return legacyTakeover[command]
}

// BeforeCall is the takeover hook for one tool call (N:'s _maybe_take_over_music): a "play" by a
// music command stops the other players first.
func (d *Ducker) BeforeCall(ctx context.Context, binary string, args map[string]any) {
	if binary == "" || args["action"] != "play" {
		return
	}
	d.StopOthers(ctx, binary)
}

// StopOthers stops every player producing audio except the one named (re-tuning a station must
// not stop its own player): mpd over its TCP port, go-librespot over its HTTP API, the rest by
// SIGINT. Paused daemons are left alone.
func (d *Ducker) StopOthers(ctx context.Context, except string) {
	items, _ := d.sinkInputs(ctx)
	var active []string
	for _, it := range items {
		if b := it.binary(); isPlayer(b) && !slices.Contains(active, b) {
			active = append(active, b)
		}
	}
	if len(active) == 0 {
		return
	}
	var stopped, skipped []string
	for _, b := range active {
		switch {
		case b == except:
			skipped = append(skipped, b)
			continue
		case b == "mpd":
			d.stopMPD()
		case b == "go-librespot":
			d.pauseLibrespot(ctx)
		case slices.Contains(pkillIntPlayers, b):
			d.ok(ctx, "pkill", "-INT", "-x", b)
		default:
			d.log().Warn("music: takeover: no way to stop this player", "binary", b)
			continue
		}
		stopped = append(stopped, b)
	}
	d.log().Info("music: takeover", "except", except, "stopped", stopped, "skipped", skipped)
}

// stopMPD sends stop + clear on mpd's control port (_stop_mpd_via_tcp).
func (d *Ducker) stopMPD() {
	addr := d.MPDAddr
	if addr == "" {
		addr = "localhost:6600"
	}
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	if _, err := c.Read(buf); err != nil { // "OK MPD <version>"
		return
	}
	if _, err := io.WriteString(c, "command_list_begin\nstop\nclear\ncommand_list_end\n"); err != nil {
		return
	}
	_, _ = c.Read(buf) // OK, for a clean close
}

// pauseLibrespot POSTs go-librespot's pause (_stop_go_librespot_via_http).
func (d *Ducker) pauseLibrespot(ctx context.Context) {
	base := d.LibrespotURL
	if base == "" {
		base = "http://localhost:3678"
	}
	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/player/pause", nil)
	if err != nil {
		return
	}
	if resp, err := hc.Do(req); err == nil {
		resp.Body.Close()
	}
}
