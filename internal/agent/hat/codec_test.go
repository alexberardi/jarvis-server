package hat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fakeAmixer serves canned amixer output per argv and records every call.
type fakeAmixer struct {
	out   map[string]string // argv joined by spaces → stdout
	fail  map[string]bool   // argv → exit non-zero
	calls []string
}

func (f *fakeAmixer) run(_ context.Context, name string, args ...string) (string, error) {
	argv := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, argv)
	if f.fail[argv] {
		// The canned stdout comes back with the error, so a caller has to check err.
		return f.out[argv], errors.New("exit status 1")
	}
	if out, ok := f.out[argv]; ok {
		return out, nil
	}
	return "", nil
}

func (f *fakeAmixer) writes() []string {
	var w []string
	for _, c := range f.calls {
		if strings.Contains(c, " cset ") || strings.Contains(c, " sset ") || strings.HasPrefix(c, "sudo ") {
			w = append(w, c)
		}
	}
	return w
}

// The formats are the seeed2micvoicec card's (N:tests/test_audio_volume.py).
func hpfCget(v string) string {
	return "numid=36,iface=MIXER,name='ADC HPF Cut-off'\n" +
		"  ; type=ENUMERATED,access=rw------,values=2,items=4\n" +
		"  ; Item #0 'Disabled'\n" +
		"  ; Item #1 '0.0045xFs'\n" +
		"  ; Item #2 '0.0125xFs'\n" +
		"  ; Item #3 '0.025xFs'\n" +
		"  : values=" + v + "," + v + "\n"
}

func sget(control string, value int, sw string) string {
	caps, tok := "pvolume", ""
	if sw != "" {
		caps, tok = "pvolume pswitch", " ["+sw+"]"
	}
	l := fmt.Sprintf("Playback %d [%d%%] [%.2fdB]%s", value, 100*value/118, -0.5*float64(118-value), tok)
	return fmt.Sprintf("Simple mixer control '%s',0\n  Capabilities: %s\n  Playback channels: Front Left - Front Right\n"+
		"  Limits: Playback 0 - 118\n  Mono:\n  Front Left: %s\n  Front Right: %s\n", control, caps, l, l)
}

const hpfGet = "amixer -c seeed2micvoicec cget name=ADC HPF Cut-off"
const hpfSet = "amixer -c seeed2micvoicec cset name=ADC HPF Cut-off 1"

func TestADCHPF(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		fail bool
		want []string
	}{
		{"disabled is enabled", hpfCget("0"), false, []string{hpfSet}},
		{"0.0045xFs is left alone", hpfCget("1"), false, nil},
		{"a higher cutoff is not downgraded", hpfCget("2"), false, nil},
		{"the highest cutoff is not downgraded", hpfCget("3"), false, nil},
		{"garbage is skipped", "numid=36\n  : values=\n", false, nil},
		{"a failing cget is skipped", hpfCget("0"), true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAmixer{out: map[string]string{hpfGet: tc.out}, fail: map[string]bool{hpfGet: tc.fail}}
			EnsureADCHPF(context.Background(), f.run, quiet)
			if got := f.writes(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("writes = %q, want %q", got, tc.want)
			}
		})
	}
}

func baselineState(line, lineDAC int, hp, hpcom string) map[string]string {
	return map[string]string{
		"amixer -c seeed2micvoicec sget Line":     sget("Line", line, "on"),
		"amixer -c seeed2micvoicec sget Line DAC": sget("Line DAC", lineDAC, ""),
		"amixer -c seeed2micvoicec sget HP":       sget("HP", 0, hp),
		"amixer -c seeed2micvoicec sget HPCOM":    sget("HPCOM", 0, hpcom),
	}
}

var wantBaseline = []string{
	"amixer -c seeed2micvoicec sset PCM 100% unmute",
	"amixer -c seeed2micvoicec sset Line 2 unmute",
	"amixer -c seeed2micvoicec sset Line DAC 115 unmute",
	"amixer -c seeed2micvoicec sset Left Line Mixer DACL1 on",
	"amixer -c seeed2micvoicec sset Right Line Mixer DACR1 on",
	"amixer -c seeed2micvoicec sset HP 0 mute",
	"amixer -c seeed2micvoicec sset HP DAC 0 mute",
	"amixer -c seeed2micvoicec sset HPCOM 0 mute",
	"amixer -c seeed2micvoicec sset HPCOM DAC 0 mute",
	"sudo -n /usr/local/sbin/jarvis-alsa-store",
}

func TestOutputBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state map[string]string
		want  []string
	}{
		{"calibrated is left alone", baselineState(4, 115, "off", "off"), nil},
		{"a hand-tuned notch down is left alone", baselineState(1, 100, "off", "off"), nil},
		{"Line 0 drifted", baselineState(0, 115, "off", "off"), wantBaseline},
		{"Line DAC 99 drifted", baselineState(4, 99, "off", "off"), wantBaseline},
		{"HP unmuted drifted", baselineState(4, 115, "on", "off"), wantBaseline},
		{"HPCOM unmuted drifted", baselineState(4, 115, "off", "on"), wantBaseline},
		{"codec power-on defaults drifted", baselineState(0, 71, "on", "on"), wantBaseline},
		// HP must have a switch: without one the state is unreadable, not "muted".
		{"HP without a switch is skipped", baselineState(0, 71, "", "off"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAmixer{out: tc.state}
			EnsureOutputBaseline(context.Background(), f.run, quiet)
			if got := f.writes(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("writes =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestOutputBaselineUnreadableIsSkipped(t *testing.T) {
	st := baselineState(0, 71, "on", "on")
	f := &fakeAmixer{out: st, fail: map[string]bool{"amixer -c seeed2micvoicec sget Line DAC": true}}
	EnsureOutputBaseline(context.Background(), f.run, quiet)
	if w := f.writes(); w != nil {
		t.Fatalf("writes = %q, want none", w)
	}
	st["amixer -c seeed2micvoicec sget Line"] = "Simple mixer control 'Line',0\n  Limits: Playback 0 - 118\n"
	f = &fakeAmixer{out: st}
	EnsureOutputBaseline(context.Background(), f.run, quiet)
	if w := f.writes(); w != nil {
		t.Fatalf("no Playback %% line: writes = %q, want none", w)
	}
}

func TestOutputBaselineKeepsGoingPastAFailure(t *testing.T) {
	f := &fakeAmixer{out: baselineState(0, 115, "off", "off"),
		fail: map[string]bool{wantBaseline[2]: true, wantBaseline[len(wantBaseline)-1]: true}}
	EnsureOutputBaseline(context.Background(), f.run, quiet)
	if got := f.writes(); !reflect.DeepEqual(got, wantBaseline) {
		t.Fatalf("writes =\n%q\nwant\n%q", got, wantBaseline)
	}
}

func TestPlaybackLineSwitchIsTheLastToken(t *testing.T) {
	for _, tc := range []struct {
		out   string
		value int
		sw    switchState
	}{
		{"  Front Left: Playback 71 [60%] [-23.50dB]\n", 71, noSwitch},
		{"  Front Left: Playback 0 [0%] [0.00dB] [on]\n", 0, switchOn},
		{"  Mono: Playback 127 [100%] [0.00dB] [off]  \n", 127, switchOff},
		// A switch token that isn't last (here before the dB column) is not the switch.
		{"  Mono: Playback 5 [4%] [on] [-56.50dB]\n", 5, noSwitch},
	} {
		v, sw, ok := parsePlayback(tc.out)
		if !ok || v != tc.value || sw != tc.sw {
			t.Errorf("parsePlayback(%q) = %d, %v, %v; want %d, %v", tc.out, v, sw, ok, tc.value, tc.sw)
		}
	}
}

func TestHealCodecRunsBoth(t *testing.T) {
	st := baselineState(0, 115, "off", "off")
	st[hpfGet] = hpfCget("0")
	f := &fakeAmixer{out: st}
	HealCodec(context.Background(), f.run, quiet)
	want := append([]string{hpfSet}, wantBaseline...)
	if got := f.writes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("writes =\n%q\nwant\n%q", got, want)
	}
}

// pgaSget is amixer sget PGA on the seeed2micvoicec card (N:utils/audio_volume.py's format).
func pgaSget(l, r int) string {
	return "Simple mixer control 'PGA',0\n  Capabilities: cvolume\n  Capture channels: Front Left - Front Right\n" +
		"  Limits: Capture 0 - 119\n" +
		fmt.Sprintf("  Front Left: Capture %d [%d%%] [29.00dB]\n  Front Right: Capture %d [%d%%] [29.00dB]\n", l*119/100, l, r*119/100, r)
}

func TestCapturePGA(t *testing.T) {
	read := "amixer -c seeed2micvoicec sget PGA"
	f := &fakeAmixer{out: map[string]string{read: pgaSget(49, 52)}}
	if pct, ok := CapturePGA(context.Background(), f.run); !ok || pct != 52 {
		t.Fatalf("CapturePGA = %d, %v; want the louder channel, 52", pct, ok)
	}
	f = &fakeAmixer{out: map[string]string{read: "garbage"}}
	if _, ok := CapturePGA(context.Background(), f.run); ok {
		t.Fatal("unparseable output read as a gain")
	}
	f = &fakeAmixer{fail: map[string]bool{read: true}}
	if _, ok := CapturePGA(context.Background(), f.run); ok {
		t.Fatal("a failed amixer read as a gain")
	}
	f = &fakeAmixer{}
	if !SetCapturePGA(context.Background(), f.run, 140) {
		t.Fatal("SetCapturePGA failed")
	}
	if want := []string{"amixer -c seeed2micvoicec sset PGA 100%"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %q, want %q (clamped)", f.calls, want)
	}
	f = &fakeAmixer{fail: map[string]bool{"amixer -c seeed2micvoicec sset PGA 49%": true}}
	if SetCapturePGA(context.Background(), f.run, 49) {
		t.Fatal("a failed sset reported success")
	}
}
