package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ecPulse is a PulseAudio for the canceller: modules load and unload (their sinks with them),
// streams sit on sinks by index, and every pactl call is recorded.
type ecPulse struct {
	mu      sync.Mutex
	next    int               // the next module index
	modules map[string]string // index → "name args"
	sinks   map[string]int    // sink name → index
	def     string            // the default sink
	streams []ecStream
	fail    map[string]bool // a call prefix that exits non-zero
	calls   []string
}

type ecStream struct {
	id     int
	binary string
	sink   string
}

func newECPulse(streams ...ecStream) *ecPulse {
	return &ecPulse{next: 20, modules: map[string]string{}, sinks: map[string]int{"jarvis_output": 1, NullSink: 2},
		def: "jarvis_output", streams: streams, fail: map[string]bool{}}
}

func (f *ecPulse) run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	argv := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, argv)
	for p := range f.fail {
		if strings.HasPrefix(argv, p) {
			return "", errors.New("exit status 1")
		}
	}
	switch {
	case argv == "pactl -f json list sink-inputs":
		var items []map[string]any
		for _, s := range f.streams {
			items = append(items, map[string]any{"index": s.id, "corked": false, "sink": f.sinks[s.sink],
				"properties": map[string]any{"application.process.binary": "/usr/bin/" + s.binary}})
		}
		b, _ := json.Marshal(items)
		return string(b), nil
	case argv == "pactl list short sinks":
		var b strings.Builder
		for n, i := range f.sinks {
			fmt.Fprintf(&b, "%d\t%s\tmodule-x.c\ts16le 2ch 48000Hz\tRUNNING\n", i, n)
		}
		return b.String(), nil
	case argv == "pactl list short modules":
		var b strings.Builder
		for i, m := range f.modules {
			n, a, _ := strings.Cut(m, " ")
			fmt.Fprintf(&b, "%s\t%s\t%s\t\n", i, n, a)
		}
		return b.String(), nil
	case argv == "pactl get-default-sink":
		return f.def + "\n", nil
	case len(args) == 2 && args[0] == "set-default-sink":
		f.def = args[1]
	case len(args) >= 2 && args[0] == "load-module":
		idx := strconv.Itoa(f.next)
		f.next++
		f.modules[idx] = strings.Join(args[1:], " ")
		if args[1] == "module-echo-cancel" {
			f.sinks[ECSink] = 9
		}
		return idx + "\n", nil
	case len(args) == 2 && args[0] == "unload-module":
		m, ok := f.modules[args[1]]
		if !ok {
			return "", errors.New("no such module")
		}
		delete(f.modules, args[1])
		if strings.HasPrefix(m, "module-echo-cancel") {
			delete(f.sinks, ECSink)
			// Pulse moves a dying sink's streams to the default sink.
			for i := range f.streams {
				if f.streams[i].sink == ECSink {
					f.streams[i].sink = f.def
				}
			}
		}
	case len(args) == 3 && args[0] == "move-sink-input":
		for i := range f.streams {
			if strconv.Itoa(f.streams[i].id) == args[1] {
				f.streams[i].sink = strings.ReplaceAll(args[2], "@DEFAULT_SINK@", f.def)
			}
		}
	}
	return "", nil
}

func (f *ecPulse) sinkOf(id int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.streams {
		if s.id == id {
			return s.sink
		}
	}
	return ""
}

// pactlCalls are the calls that change Pulse, in order.
func (f *ecPulse) changes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.Contains(c, "list") || strings.Contains(c, "get-default") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// fakeCapture is the pipeline's switchable mic.
type fakeCapture struct {
	mu   sync.Mutex
	base string
	cur  string
	uses []string
}

func (c *fakeCapture) Use(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.uses = append(c.uses, name)
	c.cur = name
}
func (c *fakeCapture) Base() string { return c.base }

func newEC(p *ecPulse) (*EchoCancel, *fakeCapture) {
	c := &fakeCapture{base: DefaultMic}
	return &EchoCancel{Run: p.run, Mic: DefaultMic, Sink: DefaultSink, Capture: c}, c
}

const (
	loadMic = "pactl load-module module-alsa-source device=dsnoopmic source_name=jarvis_mic_raw rate=16000 channels=1 source_properties=device.description=jarvis-mic-raw"
	loadEC  = `pactl load-module module-echo-cancel aec_method=webrtc aec_args="analog_gain_control=0 digital_gain_control=0" source_master=jarvis_mic_raw sink_master=jarvis_output source_name=jarvis_ec_source sink_name=jarvis_ec_sink`
)

func TestEchoCancelEngageLoadsMovesAndSwitchesCapture(t *testing.T) {
	p := newECPulse(ecStream{7, "mpv", "jarvis_output"}, ecStream{8, "paplay", "jarvis_output"})
	ec, c := newEC(p)
	if !ec.Engage(context.Background(), "deferred_play") || !ec.Engaged() {
		t.Fatal("not engaged")
	}
	want := []string{loadMic, loadEC, "pactl set-default-sink jarvis_ec_sink", "pactl move-sink-input 7 jarvis_ec_sink"}
	if got := p.changes(); !slices.Equal(got, want) {
		t.Fatalf("engage ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p.sinkOf(8) != "jarvis_output" {
		t.Fatal("a stream that isn't a player's (the keepalive) was moved")
	}
	if !slices.Equal(c.uses, []string{ECCapture}) {
		t.Fatalf("capture uses %v", c.uses)
	}
	// Engaging again changes nothing.
	n := len(p.changes())
	ec.Engage(context.Background(), "restore")
	if len(p.changes()) != n || len(c.uses) != 1 {
		t.Fatal("a second engage ran again")
	}
}

func TestEchoCancelDisengageUndoesExactlyItsEngage(t *testing.T) {
	p := newECPulse(ecStream{7, "mpv", "jarvis_output"})
	p.modules["3"] = "module-null-sink sink_name=" + NullSink // not ours
	ec, c := newEC(p)
	ec.Engage(context.Background(), "deferred_play")
	// A stream already parked by a duck stays parked.
	p.streams = append(p.streams, ecStream{11, "ffplay", NullSink})
	before := len(p.changes())
	ec.Disengage(context.Background(), "restore")
	want := []string{"pactl set-default-sink jarvis_output", "pactl move-sink-input 7 jarvis_output",
		"pactl unload-module 21", "pactl unload-module 20"}
	if got := p.changes()[before:]; !slices.Equal(got, want) {
		t.Fatalf("disengage ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ec.Engaged() || !slices.Equal(c.uses, []string{ECCapture, ""}) || p.sinkOf(11) != NullSink {
		t.Fatalf("engaged %v, uses %v, parked stream on %s", ec.Engaged(), c.uses, p.sinkOf(11))
	}
	if _, ok := p.modules["3"]; !ok || len(p.modules) != 1 {
		t.Fatalf("modules left %v", p.modules)
	}
	if c.uses[0] != ECCapture || len(c.uses) != 2 {
		t.Fatal("capture not switched back")
	}
	// The default sink a user had set comes back, not ours.
	p.def = "bluez_sink.xx"
	ec.Engage(context.Background(), "deferred_play")
	ec.Disengage(context.Background(), "restore")
	if p.def != "bluez_sink.xx" {
		t.Fatalf("default sink %q", p.def)
	}
}

// #469: with Bluetooth headphones or a speaker as the output, the node's music plays there
// through the canceller, and a Bluetooth route made while it played is kept when it stops.
func TestEchoCancelFollowsABluetoothOutput(t *testing.T) {
	const bt = "bluez_sink.EC_66_D1_B4_B5_D6.a2dp_sink"
	p := newECPulse(ecStream{7, "mpv", bt})
	p.sinks[bt], p.def = 5, bt
	ec, _ := newEC(p)
	ec.Engage(context.Background(), "deferred_play")
	if got := p.changes(); len(got) < 2 || !strings.Contains(got[1], "sink_master="+bt+" ") {
		t.Fatalf("the canceller doesn't play to the Bluetooth output: %v", got)
	}
	ec.Disengage(context.Background(), "restore")
	if p.def != bt || p.sinkOf(7) != bt {
		t.Fatalf("after the music: default %q, player on %q", p.def, p.sinkOf(7))
	}

	// The headphones connect while the music plays on the HAT: the route they got stays.
	p = newECPulse(ecStream{7, "mpv", "jarvis_output"})
	ec, _ = newEC(p)
	ec.Engage(context.Background(), "deferred_play")
	p.sinks[bt], p.def = 5, bt // the Bluetooth node routed the output
	ec.Disengage(context.Background(), "restore")
	if p.def != bt || p.sinkOf(7) != bt {
		t.Fatalf("a route made during the music was undone: default %q, player on %q", p.def, p.sinkOf(7))
	}
}

func TestEchoCancelEngageFailureFailsOpen(t *testing.T) {
	for _, step := range []string{"pactl load-module module-alsa-source", "pactl load-module module-echo-cancel"} {
		t.Run(step, func(t *testing.T) {
			p := newECPulse(ecStream{7, "mpv", "jarvis_output"})
			p.fail[step] = true
			ec, c := newEC(p)
			if ec.Engage(context.Background(), "deferred_play") || ec.Engaged() {
				t.Fatal("engaged")
			}
			if len(p.modules) != 0 || len(c.uses) != 0 || p.def != "jarvis_output" || p.sinkOf(7) != "jarvis_output" {
				t.Fatalf("left behind: modules %v, capture %v, default %s, stream on %s", p.modules, c.uses, p.def, p.sinkOf(7))
			}
			// No retry until the music starts again.
			n := len(p.calls)
			delete(p.fail, step)
			if ec.Engage(context.Background(), "restore") || len(p.calls) != n {
				t.Fatal("retried before Rearm")
			}
			ec.Rearm()
			if !ec.Engage(context.Background(), "deferred_play") {
				t.Fatal("not engaged after Rearm")
			}
		})
	}
}

func TestEchoCancelOnlyForThePipelinesOwnMic(t *testing.T) {
	p := newECPulse()
	ec, c := newEC(p)
	c.base = "wav:/tmp/in.wav"
	if ec.Engage(context.Background(), "deferred_play") || len(p.calls) != 0 {
		t.Fatalf("engaged over a %s input: %v", c.base, p.calls)
	}
	var nilEC *EchoCancel
	if nilEC.Engage(context.Background(), "x") || nilEC.Engaged() {
		t.Fatal("nil engaged")
	}
	nilEC.Disengage(context.Background(), "x")
	nilEC.GiveUp(context.Background(), "x")
	nilEC.Cleanup(context.Background())
}

func TestEchoCancelGiveUpDisengagesUntilRearm(t *testing.T) {
	p := newECPulse(ecStream{7, "mpv", "jarvis_output"})
	ec, _ := newEC(p)
	ec.Engage(context.Background(), "deferred_play")
	ec.GiveUp(context.Background(), "silent")
	if ec.Engaged() || len(p.modules) != 0 || p.sinkOf(7) != "jarvis_output" {
		t.Fatalf("engaged %v, modules %v, stream on %s", ec.Engaged(), p.modules, p.sinkOf(7))
	}
	if ec.Engage(context.Background(), "restore") {
		t.Fatal("re-engaged before the music stopped")
	}
}

func TestEchoCancelCleanupUnloadsStaleModules(t *testing.T) {
	p := newECPulse()
	p.modules["3"] = "module-null-sink sink_name=" + NullSink
	p.modules["14"] = "module-alsa-source device=dsnoopmic source_name=jarvis_mic_raw rate=16000 channels=1"
	p.modules["15"] = "module-echo-cancel aec_method=webrtc source_master=jarvis_mic_raw sink_master=jarvis_output source_name=jarvis_ec_source sink_name=jarvis_ec_sink"
	ec, _ := newEC(p)
	ec.Cleanup(context.Background())
	if got := p.changes(); !slices.Equal(got, []string{"pactl unload-module 15", "pactl unload-module 14"}) {
		t.Fatalf("cleanup ran %v", got)
	}
	if _, ok := p.modules["3"]; !ok || len(p.modules) != 1 {
		t.Fatalf("modules left %v", p.modules)
	}
}

// The Ducker drives the canceller: a deferred play engages it, a duck never touches it (the
// wake just fired), parked streams go back to the canceller's sink while it is engaged, and the
// restore after the music stopped disengages it and rearms it.
func TestDuckerDrivesEchoCancel(t *testing.T) {
	p := newECPulse()
	ec, c := newEC(p)
	d := &Ducker{Run: p.run, DuckPercent: 20, EC: ec}
	ctx := context.Background()

	d.Restore(ctx) // a turn with no music
	if ec.Engaged() {
		t.Fatal("engaged without music")
	}
	p.streams = []ecStream{{7, "mpv", "jarvis_output"}}
	d.SetSelfPlaying(ctx, true, "deferred_play")
	if !ec.Engaged() || p.sinkOf(7) != ECSink {
		t.Fatalf("deferred play: engaged %v, stream on %s", ec.Engaged(), p.sinkOf(7))
	}

	// The next wake: the duck parks mpv and leaves the capture alone.
	uses := len(c.uses)
	d.Duck(ctx)
	if p.sinkOf(7) != NullSink || len(c.uses) != uses || !ec.Engaged() {
		t.Fatalf("duck: stream on %s, capture uses %v", p.sinkOf(7), c.uses)
	}
	d.Restore(ctx)
	if p.sinkOf(7) != ECSink || !ec.Engaged() || len(c.uses) != uses {
		t.Fatalf("restore while playing: stream on %s, engaged %v, uses %v", p.sinkOf(7), ec.Engaged(), c.uses)
	}

	// "Stop the music": the next duck sees nothing playing, and only its restore disengages.
	p.streams = nil
	d.Duck(ctx)
	if !ec.Engaged() {
		t.Fatal("the duck disengaged mid-turn")
	}
	d.Restore(ctx)
	if ec.Engaged() || c.cur != "" || len(p.modules) != 0 {
		t.Fatalf("after the music stopped: engaged %v, capture %q, modules %v", ec.Engaged(), c.cur, p.modules)
	}
}

// Parked streams go back to jarvis_ec_sink by name (N:), not to the default sink: when Engage
// could not make the canceller's sink the default, the default is still the HAT's, and a stream
// restored there would play past the canceller.
func TestDuckerRestoresParkedStreamsToTheCancellersSink(t *testing.T) {
	p := newECPulse(ecStream{7, "mpv", "jarvis_output"})
	p.fail["pactl set-default-sink "+ECSink] = true
	ec, _ := newEC(p)
	d := &Ducker{Run: p.run, DuckPercent: 20, EC: ec}
	ctx := context.Background()
	d.SetSelfPlaying(ctx, true, "deferred_play")
	if !ec.Engaged() || p.def != DefaultSink || p.sinkOf(7) != ECSink {
		t.Fatalf("engage: engaged %v, default %s, stream on %s", ec.Engaged(), p.def, p.sinkOf(7))
	}
	d.Duck(ctx)
	if p.sinkOf(7) != NullSink {
		t.Fatalf("duck: stream on %s", p.sinkOf(7))
	}
	d.Restore(ctx)
	if p.sinkOf(7) != ECSink || !slices.Contains(p.changes(), "pactl move-sink-input 7 "+ECSink) {
		t.Fatalf("restore: stream on %s, calls %v; want it moved back to %s", p.sinkOf(7), p.changes(), ECSink)
	}
}

func TestDuckerRestoreEngagesForMusicStartedElsewhere(t *testing.T) {
	// Music a phone started (AirPlay): the first duck sees it, the restore engages and the
	// parked stream lands on the canceller's sink.
	p := newECPulse(ecStream{7, "mpv", "jarvis_output"})
	ec, _ := newEC(p)
	d := &Ducker{Run: p.run, EC: ec}
	d.Duck(context.Background())
	if ec.Engaged() {
		t.Fatal("engaged at the duck")
	}
	d.Restore(context.Background())
	if !ec.Engaged() || p.sinkOf(7) != ECSink {
		t.Fatalf("engaged %v, stream on %s", ec.Engaged(), p.sinkOf(7))
	}
}

func TestDuckerRearmsAFailedEngageWhenTheMusicStops(t *testing.T) {
	p := newECPulse(ecStream{7, "mpv", "jarvis_output"})
	p.fail["pactl load-module module-echo-cancel"] = true
	ec, _ := newEC(p)
	d := &Ducker{Run: p.run, EC: ec}
	ctx := context.Background()
	d.SetSelfPlaying(ctx, true, "deferred_play")
	delete(p.fail, "pactl load-module module-echo-cancel")
	d.Restore(ctx)
	if ec.Engaged() {
		t.Fatal("retried while the same music plays")
	}
	d.SetSelfPlaying(ctx, false, "stopped")
	d.SetSelfPlaying(ctx, true, "deferred_play")
	if !ec.Engaged() {
		t.Fatal("not engaged on the next start")
	}
}
