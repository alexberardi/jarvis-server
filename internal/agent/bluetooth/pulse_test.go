//go:build linux

package bluetooth

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A stand-in pactl on PATH: it logs its arguments and prints pactl's short listings.
const fakePactl = `#!/bin/sh
echo "$*" >>"$PACTL_LOG"
case "$*" in
"list sinks short") printf '0\tjarvis_output\tmodule-alsa-sink.c\ts16le 2ch 48000Hz\tRUNNING\n1\tbluez_sink.AA_BB_CC_DD_EE_01.a2dp_sink\tmodule-bluez5-device.c\ts16le 2ch 44100Hz\tIDLE\n' ;;
"list sources short") printf '0\tjarvis_output.monitor\tmodule-alsa-sink.c\ts16le 2ch 48000Hz\tIDLE\n' ;;
"list modules short") printf '6\tmodule-alsa-sink\tdevice=hw:CARD=seeed2micvoicec,DEV=0 sink_name=jarvis_output\n27\tmodule-loopback\tsource=bluez_source.11_22_33_44_55_66.a2dp_source latency_msec=100\n30\tmodule-native-protocol-unix\t\n' ;;
info) printf 'Server Name: pulseaudio\nDefault Sink: jarvis_output\nDefault Source: jarvis_output.monitor\n' ;;
set-default-sink*|set-card-profile*) ;;
load-module*) echo 31 ;;
unload-module*) ;;
*) echo "unknown" >&2; exit 1 ;;
esac
`

func TestPactl(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pactl"), []byte(fakePactl), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PACTL_LOG", log)
	ctx, p := context.Background(), Pactl{}

	sinks, err := p.List(ctx, "sinks")
	if err != nil || !reflect.DeepEqual(sinks, []string{"jarvis_output", "bluez_sink.AA_BB_CC_DD_EE_01.a2dp_sink"}) {
		t.Fatalf("sinks %v %v", sinks, err)
	}
	if up, err := hasEndpoint(ctx, p, speaker); !up || err != nil {
		t.Fatal("hasEndpoint", err)
	}
	if up, _ := hasEndpoint(ctx, p, phone); up {
		t.Fatal("hasEndpoint: the phone")
	}
	if s, ok, err := btSink(ctx, p, speaker); !ok || err != nil || s != "bluez_sink.AA_BB_CC_DD_EE_01.a2dp_sink" {
		t.Fatalf("btSink %q %v %v", s, ok, err)
	}
	if def, err := p.DefaultSink(ctx); err != nil || def != "jarvis_output" {
		t.Fatalf("default sink %q %v", def, err)
	}
	if err := p.SetDefaultSink(ctx, "bluez_sink.AA_BB_CC_DD_EE_01.a2dp_sink"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetCardProfile(ctx, "bluez_card.AA_BB_CC_DD_EE_01", "a2dp_sink"); err != nil {
		t.Fatal(err)
	}
	mods, err := p.Modules(ctx)
	if err != nil || len(mods) != 3 || mods[1] != (Module{ID: "27", Name: "module-loopback", Args: "source=bluez_source.11_22_33_44_55_66.a2dp_source latency_msec=100"}) || mods[2].Args != "" {
		t.Fatalf("modules %+v %v", mods, err)
	}
	if id, ok := loopbackFor(ctx, p, phoneSource(phone)); !ok || id != "27" {
		t.Fatalf("loopback %q %v", id, ok)
	}
	if id, err := p.LoadModule(ctx, "module-loopback", "source=x latency_msec=100"); err != nil || id != "31" {
		t.Fatalf("load %q %v", id, err)
	}
	if err := p.UnloadModule(ctx, "27"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.List(ctx, "cards"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("a failing pactl: %v", err)
	}
	b, _ := os.ReadFile(log)
	want := "list sinks short\nlist sinks short\nlist sinks short\nlist sources short\nlist sinks short\ninfo\n" +
		"set-default-sink bluez_sink.AA_BB_CC_DD_EE_01.a2dp_sink\nset-card-profile bluez_card.AA_BB_CC_DD_EE_01 a2dp_sink\nlist modules short\nlist modules short\nload-module module-loopback source=x latency_msec=100\nunload-module 27\nlist cards short\n"
	if string(b) != want {
		t.Fatalf("pactl calls:\n%s\nwant\n%s", b, want)
	}
}
