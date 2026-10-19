package music

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The cases follow N:tests/test_music_control.py.

// stream is one sink-input as pactl -f json prints it.
type stream struct {
	id     int
	binary string
	corked bool
	volume string // "61%"; "" leaves the volume out (unreadable)
}

// fakePulse answers pactl and pkill as Pulse would for a set of streams, and records every call.
type fakePulse struct {
	mu       sync.Mutex
	streams  []stream
	sinks    string          // pactl list short sinks
	fail     map[string]bool // a call (argv joined by spaces) that exits non-zero
	listFail bool
	calls    []string
}

func (f *fakePulse) run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	argv := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, argv)
	if f.fail[argv] {
		return "", errors.New("exit status 1")
	}
	switch argv {
	case "pactl -f json list sink-inputs":
		if f.listFail {
			return "", errors.New("exit status 1")
		}
		var items []map[string]any
		for _, s := range f.streams {
			it := map[string]any{"index": s.id, "corked": s.corked,
				"properties": map[string]any{"application.process.binary": "/usr/bin/" + s.binary}}
			if s.volume != "" {
				it["volume"] = map[string]any{"front-left": map[string]any{"value": 1, "value_percent": s.volume},
					"front-right": map[string]any{"value": 1, "value_percent": s.volume}}
			}
			items = append(items, it)
		}
		b, _ := json.Marshal(items)
		return string(b), nil
	case "pactl list short sinks":
		return f.sinks, nil
	}
	if strings.HasPrefix(argv, "pkill ") {
		// pkill exits 1 when nothing matched.
		for _, s := range f.streams {
			if strings.HasSuffix(argv, " "+s.binary) {
				return "", nil
			}
		}
		return "", errors.New("exit status 1")
	}
	return "", nil
}

func (f *fakePulse) has(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

func (f *fakePulse) index(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Index(f.calls, call)
}

func (f *fakePulse) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// fakeGain is the HAT's mic PGA.
type fakeGain struct {
	pct, sets int
	missing   bool
	failSet   bool
}

func (g *fakeGain) Get(context.Context) (int, bool) { return g.pct, !g.missing }
func (g *fakeGain) Set(_ context.Context, p int) bool {
	if g.failSet {
		return false
	}
	g.pct, g.sets = p, g.sets+1
	return true
}

func newDucker(f *fakePulse, g Gain) *Ducker {
	d := New(g, nil)
	d.Run = f.run
	return d
}

func TestDuckParksStopsAndMutes(t *testing.T) {
	f := &fakePulse{sinks: "123\tjarvis_duck_null\tmodule-null-sink.c\n",
		streams: []stream{{id: 10, binary: "mpv"}, {id: 11, binary: "go-librespot"}}}
	newDucker(f, nil).Duck(context.Background())
	for _, want := range []string{
		"pactl move-sink-input 10 jarvis_duck_null", // SIGSTOP class: parked on the null sink
		"pactl set-sink-input-mute 11 1",            // mute-only class, volume unreadable: muted
		"pkill -STOP -x mpv",
	} {
		if !f.has(want) {
			t.Errorf("missing %q in %q", want, f.calls)
		}
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "pkill -STOP") && strings.HasSuffix(c, "go-librespot") {
			t.Errorf("go-librespot SIGSTOPped (%q): its HTTP API would hang", c)
		}
		if strings.Contains(c, "load-module") {
			t.Errorf("null sink loaded again although Pulse has it: %q", c)
		}
	}
}

func TestDuckLoadsTheNullSinkOnce(t *testing.T) {
	f := &fakePulse{sinks: "0\tjarvis_output\tmodule-alsa-sink.c\n"}
	newDucker(f, nil).Duck(context.Background())
	if !f.has("pactl load-module module-null-sink sink_name=jarvis_duck_null sink_properties=device.description=jarvis-duck-null") {
		t.Fatalf("null sink not loaded: %q", f.calls)
	}
}

func TestDuckContinuesAfterAFailure(t *testing.T) {
	f := &fakePulse{sinks: NullSink, streams: []stream{{id: 10, binary: "mpv"}},
		fail: map[string]bool{"pactl move-sink-input 10 jarvis_duck_null": true}}
	newDucker(f, nil).Duck(context.Background())
	if !f.has("pkill -STOP -x mpv") {
		t.Fatalf("a failed move stopped the duck: %q", f.calls)
	}
}

func TestRestoreResumesBeforeMovingBack(t *testing.T) {
	f := &fakePulse{streams: []stream{{id: 10, binary: "mpv"}, {id: 11, binary: "go-librespot"}}}
	newDucker(f, nil).Restore(context.Background())
	cont, move := f.index("pkill -CONT -x mpv"), f.index("pactl move-sink-input 10 @DEFAULT_SINK@")
	if cont < 0 || move < 0 || cont > move {
		t.Fatalf("want SIGCONT then the move back, got %q", f.calls)
	}
	if !f.has("pactl set-sink-input-mute 11 0") {
		t.Fatalf("mute-only stream not unmuted: %q", f.calls)
	}
}

func TestVolumeDuck(t *testing.T) {
	ctx := context.Background()
	f := &fakePulse{sinks: NullSink, streams: []stream{{id: 11, binary: "go-librespot", volume: "61%"}, {id: 10, binary: "mpv", volume: "80%"}}}
	d := newDucker(f, nil)
	d.Duck(ctx)
	if !f.has("pactl set-sink-input-volume 11 20%") || f.has("pactl set-sink-input-mute 11 1") {
		t.Fatalf("mute-only stream not ducked to 20%%: %q", f.calls)
	}
	if f.has("pactl set-sink-input-volume 10 20%") {
		t.Fatal("the SIGSTOP class was volume-ducked")
	}
	// A second duck in the same turn (a follow-up) reads the ducked level: it must not be saved.
	f.streams[0].volume = "20%"
	d.Duck(ctx)
	f.reset()
	d.Restore(ctx)
	if !f.has("pactl set-sink-input-volume 11 61%") {
		t.Fatalf("original volume not restored: %q", f.calls)
	}
	// Restored once: the next restore has nothing saved.
	f.reset()
	d.Restore(ctx)
	for _, c := range f.calls {
		if strings.HasPrefix(c, "pactl set-sink-input-volume") {
			t.Fatalf("second restore set a volume again: %q", c)
		}
	}
}

func TestVolumeDuckRestoreClearsEvenWhenPactlFails(t *testing.T) {
	ctx := context.Background()
	f := &fakePulse{sinks: NullSink, streams: []stream{{id: 11, binary: "mpd", volume: "70%"}},
		fail: map[string]bool{"pactl set-sink-input-volume 11 70%": true}}
	d := newDucker(f, nil)
	d.Duck(ctx)
	d.Restore(ctx)
	if len(d.saved) != 0 {
		t.Fatalf("saved volumes kept after a failed restore: %v", d.saved)
	}
}

func TestDuckPercentZeroMutes(t *testing.T) {
	f := &fakePulse{sinks: NullSink, streams: []stream{{id: 11, binary: "shairport-sync", volume: "61%"}}}
	d := newDucker(f, nil)
	d.DuckPercent = 0
	d.Duck(context.Background())
	if !f.has("pactl set-sink-input-mute 11 1") || len(d.saved) != 0 {
		t.Fatalf("depth 0 should mute and save nothing: %q, %v", f.calls, d.saved)
	}
}

func TestVolumeParser(t *testing.T) {
	for _, tc := range []struct {
		vol  string
		want int
		ok   bool
	}{
		{`{"mono": {"value_percent": "45%"}}`, 45, true},
		{`{"front-left": {"value_percent": "30%"}, "front-right": {"value_percent": "62%"}}`, 62, true},
		{`{"mono": {"value_percent": "loud"}}`, 0, false},
		{`{"mono": "x"}`, 0, false},
		{`{}`, 0, false},
	} {
		var s sinkInput
		if err := json.Unmarshal([]byte(`{"volume": `+tc.vol+`}`), &s); err != nil {
			t.Fatal(err)
		}
		if got, ok := s.volume(); got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %d, %v; want %d, %v", tc.vol, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsPlaying(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		f    *fakePulse
		want bool
	}{
		{"uncorked player", &fakePulse{streams: []stream{{id: 1, binary: "librespot"}}}, true},
		{"corked daemon", &fakePulse{streams: []stream{{id: 1, binary: "go-librespot", corked: true}}}, false},
		{"unknown binary", &fakePulse{streams: []stream{{id: 1, binary: "paplay"}}}, false},
		{"nothing", &fakePulse{}, false},
		{"pactl fails", &fakePulse{listFail: true, streams: []stream{{id: 1, binary: "mpv"}}}, false},
	} {
		if got := newDucker(tc.f, nil).IsPlaying(ctx); got != tc.want {
			t.Errorf("%s: IsPlaying = %v", tc.name, got)
		}
	}
}

func TestSelfPlayingRefreshedByTheDuck(t *testing.T) {
	ctx := context.Background()
	f := &fakePulse{sinks: NullSink, streams: []stream{{id: 1, binary: "go-librespot", corked: true}}}
	d := newDucker(f, nil)
	d.Duck(ctx)
	if d.SelfPlaying() {
		t.Fatal("a corked daemon counted as playing")
	}
	f.streams[0].corked = false
	d.Duck(ctx)
	if !d.SelfPlaying() {
		t.Fatal("an uncorked player not seen")
	}
	f.streams = nil
	d.Duck(ctx)
	if d.SelfPlaying() {
		t.Fatal("still playing with no streams")
	}
}

func TestPlayerClasses(t *testing.T) {
	// The CONTROL-API REACHABILITY invariant: daemons with control APIs are never SIGSTOPped.
	for _, b := range []string{"go-librespot", "mpd", "shairport-sync"} {
		if slices.Contains(sigstopPlayers, b) || !slices.Contains(muteOnlyPlayers, b) {
			t.Errorf("%s must be mute-only", b)
		}
	}
	for _, b := range sigstopPlayers {
		if slices.Contains(muteOnlyPlayers, b) {
			t.Errorf("%s is in both classes", b)
		}
	}
}

func TestMicGainProfile(t *testing.T) {
	ctx := context.Background()
	t.Run("engage on the deferred play, restore when the music stops", func(t *testing.T) {
		g := &fakeGain{pct: 60}
		d := newDucker(&fakePulse{}, g)
		d.SetSelfPlaying(ctx, true, "deferred_play")
		if g.pct != 49 {
			t.Fatalf("gain = %d, want the music profile 49", g.pct)
		}
		d.SetSelfPlaying(ctx, true, "deferred_play")
		if g.sets != 1 {
			t.Fatalf("engage not idempotent: %d sets", g.sets)
		}
		d.SetSelfPlaying(ctx, false, "duck_enumeration")
		if g.pct != 60 {
			t.Fatalf("gain = %d, want the saved 60 back", g.pct)
		}
	})
	t.Run("already at the music gain: nothing written", func(t *testing.T) {
		g := &fakeGain{pct: 49}
		d := newDucker(&fakePulse{}, g)
		d.SetSelfPlaying(ctx, true, "t")
		d.SetSelfPlaying(ctx, false, "t")
		if g.sets != 0 {
			t.Fatalf("%d amixer writes, want 0", g.sets)
		}
	})
	t.Run("restore without an engage does nothing", func(t *testing.T) {
		g := &fakeGain{pct: 60}
		newDucker(&fakePulse{}, g).restorePGA(ctx, "t")
		if g.sets != 0 {
			t.Fatal("restored without an engage")
		}
	})
	t.Run("disabled, or no HAT", func(t *testing.T) {
		g := &fakeGain{pct: 60}
		d := newDucker(&fakePulse{}, g)
		d.MusicPGA = 0
		d.SetSelfPlaying(ctx, true, "t")
		if g.sets != 0 {
			t.Fatal("profile ran while disabled")
		}
		d = newDucker(&fakePulse{}, &fakeGain{missing: true})
		d.SetSelfPlaying(ctx, true, "t")
		newDucker(&fakePulse{}, nil).SetSelfPlaying(ctx, true, "t") // no panic
	})
	t.Run("a failed engage is retried by the next duck", func(t *testing.T) {
		g := &fakeGain{pct: 60, failSet: true}
		f := &fakePulse{sinks: NullSink, streams: []stream{{id: 1, binary: "mpd"}}}
		d := newDucker(f, g)
		d.Duck(ctx)
		if g.pct != 60 || d.pgaEngaged {
			t.Fatal("engaged although amixer failed")
		}
		g.failSet = false
		d.Duck(ctx) // flag already true: no edge, the duck's ensure retries
		if g.pct != 49 {
			t.Fatalf("gain = %d, want 49 after the retry", g.pct)
		}
	})
	t.Run("a duck with no players restores the gain", func(t *testing.T) {
		g := &fakeGain{pct: 60}
		f := &fakePulse{sinks: NullSink}
		d := newDucker(f, g)
		d.SetSelfPlaying(ctx, true, "deferred_play")
		d.Duck(ctx)
		if g.pct != 60 {
			t.Fatalf("gain = %d, want 60 once the music is gone", g.pct)
		}
	})
}

func TestNormalizePGA(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name               string
		music, normal, cur int
		want               int
	}{
		{"crashed profile restored", 40, 49, 40, 49},
		{"hand-tuned gain left alone", 40, 49, 55, 55},
		{"profile equals normal", 49, 49, 49, 49},
		{"profile disabled", 0, 49, 0, 0},
	} {
		g := &fakeGain{pct: tc.cur}
		d := newDucker(&fakePulse{}, g)
		d.MusicPGA, d.NormalPGA = tc.music, tc.normal
		d.NormalizePGA(ctx)
		if g.pct != tc.want {
			t.Errorf("%s: gain = %d, want %d", tc.name, g.pct, tc.want)
		}
	}
}

func TestTakeoverBinary(t *testing.T) {
	for _, tc := range []struct{ command, declared, want string }{
		{"spotify", "custom-player", "custom-player"}, // the SDK property wins
		{"audacy", "", "mpd"},
		{"spotify", "", "go-librespot"},
		{"music", "", "shairport-sync"},
		{"pandora", "", "mpv"},
		{"get_weather", "", ""},
	} {
		if got := TakeoverBinary(tc.command, tc.declared); got != tc.want {
			t.Errorf("TakeoverBinary(%q, %q) = %q, want %q", tc.command, tc.declared, got, tc.want)
		}
	}
}

func TestStopOthers(t *testing.T) {
	ctx := context.Background()
	t.Run("nothing playing: nothing stopped", func(t *testing.T) {
		f := &fakePulse{}
		newDucker(f, nil).StopOthers(ctx, "")
		if len(f.calls) != 1 {
			t.Fatalf("calls = %q", f.calls)
		}
	})
	t.Run("each player its own way, the caller's own left alone", func(t *testing.T) {
		// mpd's control port.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip("no loopback:", err)
		}
		defer ln.Close()
		mpdGot := make(chan string, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			fmt.Fprint(c, "OK MPD 0.23.5\n")
			r := bufio.NewReader(c)
			var lines []string
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					break
				}
				lines = append(lines, strings.TrimSpace(l))
				if l == "command_list_end\n" {
					break
				}
			}
			fmt.Fprint(c, "OK\n")
			mpdGot <- strings.Join(lines, ";")
		}()
		var paused []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paused = append(paused, r.Method+" "+r.URL.Path)
		}))
		defer srv.Close()

		f := &fakePulse{streams: []stream{{id: 1, binary: "mpd"}, {id: 2, binary: "go-librespot"},
			{id: 3, binary: "mpv"}, {id: 4, binary: "shairport-sync"}, {id: 5, binary: "paplay"}}}
		d := newDucker(f, nil)
		d.MPDAddr, d.LibrespotURL = ln.Addr().String(), srv.URL
		d.StopOthers(ctx, "shairport-sync")
		if got := <-mpdGot; got != "command_list_begin;stop;clear;command_list_end" {
			t.Errorf("mpd got %q", got)
		}
		if len(paused) != 1 || paused[0] != "POST /player/pause" {
			t.Errorf("go-librespot got %q", paused)
		}
		if !f.has("pkill -INT -x mpv") {
			t.Errorf("mpv not interrupted: %q", f.calls)
		}
		for _, c := range f.calls {
			if strings.Contains(c, "shairport-sync") || strings.Contains(c, "paplay") {
				t.Errorf("%q: the caller's own player and non-players are left alone", c)
			}
		}
	})
}

func TestBeforeCall(t *testing.T) {
	ctx := context.Background()
	f := &fakePulse{streams: []stream{{id: 3, binary: "mpv"}}}
	d := newDucker(f, nil)
	d.BeforeCall(ctx, "go-librespot", map[string]any{"action": "pause"})
	d.BeforeCall(ctx, "", map[string]any{"action": "play"})
	if len(f.calls) != 0 {
		t.Fatalf("only a music command's play takes over: %q", f.calls)
	}
	d.BeforeCall(ctx, "go-librespot", map[string]any{"action": "play"})
	if !f.has("pkill -INT -x mpv") {
		t.Fatalf("play didn't take over: %q", f.calls)
	}
}
