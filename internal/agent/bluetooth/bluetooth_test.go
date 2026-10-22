package bluetooth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	speaker = "AA:BB:CC:DD:EE:01"
	phone   = "11:22:33:44:55:66"
)

func speakerDev() Device {
	return Device{Address: speaker, Name: "JBL Flip 5", Alias: "JBL Flip 5", Icon: "audio-card", UUIDs: []string{uuidAudioSink}}
}

func phoneDev() Device {
	return Device{Address: phone, Name: "Avery's iPhone", Alias: "Avery's iPhone", Icon: "phone", UUIDs: []string{uuidAudioSource, uuidHandsfreeAG}}
}

// rig is a Node on a fake bus and Pulse whose bluez sink (and, for a phone, source) appears
// when a device connects and goes when it disconnects, as PulseAudio's module-bluetooth does.
func rig(t *testing.T) (*Node, *fakeBus, *fakePulse, *fakeAPI) {
	t.Helper()
	bus, pulse := newFakeBus(), &fakePulse{sinks: []string{"jarvis_output"}, def: "jarvis_output"}
	bus.transport = map[string]bool{}
	bus.onConnect = func(mac string) {
		m := macUnderscored(mac)
		bus.mu.Lock()
		bus.transport[mac] = true
		bus.mu.Unlock()
		if mac == phone {
			pulse.add("", "bluez_source."+m+".a2dp_source")
		} else {
			pulse.add("bluez_sink."+m+".a2dp_sink", "bluez_sink."+m+".a2dp_sink.monitor")
		}
	}
	bus.onDisconnect = func(mac string) {
		bus.mu.Lock()
		delete(bus.transport, mac)
		bus.mu.Unlock()
		pulse.drop(mac)
	}
	st, err := OpenStore(filepath.Join(t.TempDir(), StoreFile))
	if err != nil {
		t.Fatal(err)
	}
	ms := time.Millisecond
	n := &Node{Bus: bus, Pulse: pulse, Store: st, Log: slog.New(slog.DiscardHandler), ScanFor: ms, FindFor: 50 * ms, AudioFor: 200 * ms, AudioStable: 5 * ms,
		BusyFor: 50 * ms, SourceFor: 50 * ms, PageRetryAfter: ms, SinkFor: 50 * ms, Poll: ms, FirstReconnect: ms, ReconnectEvery: time.Hour}
	return n, bus, pulse, &fakeAPI{}
}

func send(t *testing.T, n *Node, api API, topic string, payload map[string]any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	n.Handle(context.Background(), api, topic, b)
}

// asJSON is v as jarvisd decodes it.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func TestScanReportsWhatIsInRangeInJarvisdsShape(t *testing.T) {
	n, bus, _, api := rig(t)
	s := speakerDev()
	s.Paired, s.Connected, s.Trusted = true, true, true
	bus.devices[speaker] = &s
	bus.devices["DE:AD:00:00:00:01"] = &Device{Address: "DE:AD:00:00:00:01", Name: "Ghost from last week"} // not paired: pruned
	bus.inRange[phone] = phoneDev()
	bus.inRange["0C:0C:0C:0C:0C:0C"] = Device{Address: "0C:0C:0C:0C:0C:0C", Alias: "0C-0C-0C-0C-0C-0C"} // broadcast no name
	bus.inRange["0D:0D:0D:0D:0D:0D"] = Device{Address: "0D:0D:0D:0D:0D:0D", Name: "Band", UUIDs: []string{"0000180d-0000-1000-8000-00805f9b34fb"}}

	send(t, n, api, TopicScan, map[string]any{"request_id": "r1", "role": "source"})

	want := []any{
		map[string]any{"name": "Band", "mac_address": "0D:0D:0D:0D:0D:0D", "device_type": "unknown", "paired": false, "connected": false},
		map[string]any{"name": "Avery's iPhone", "mac_address": phone, "device_type": "phone", "paired": false, "connected": false},
		map[string]any{"name": "JBL Flip 5", "mac_address": speaker, "device_type": "audio_sink", "paired": true, "connected": true},
	}
	if got := asJSON(t, api.scans["r1"]); !reflect.DeepEqual(got, map[string]any{"devices": want}) {
		t.Fatalf("scan result:\n got %v\nwant %v", got, want)
	}
	if !slices.Equal(bus.discovering, []string{"bredr", "le"}) {
		t.Errorf("discovery: %v, want BR/EDR then LE", bus.discovering)
	}
	if !slices.Contains(bus.Calls(), "Remove DE:AD:00:00:00:01") || slices.Contains(bus.Calls(), "Remove "+speaker) {
		t.Errorf("only the unpaired cache entries are pruned: %v", bus.Calls())
	}
}

// #433: the devices are posted as they are found, and the pair's steps as they start, so the app
// shows progress instead of 20 s of nothing.
func TestScanAndPairPostTheirProgress(t *testing.T) {
	n, bus, _, api := rig(t)
	n.ScanFor, n.ProgressEvery = 20*time.Millisecond, 5*time.Millisecond
	bus.inRange[speaker] = speakerDev()
	send(t, n, api, TopicScan, map[string]any{"request_id": "r1"})
	if len(api.progress) != 1 || api.progress[0] != "scan r1 scanning 1 devices" {
		t.Fatalf("scan progress %v (one post: the list grew once)", api.progress)
	}
	if devs := api.scans["r1"]["devices"].([]ScanDevice); len(devs) != 1 {
		t.Fatalf("scan result %v", api.scans["r1"])
	}

	api.progress = nil
	delete(bus.devices, speaker) // BlueZ forgot it: the pair finds it first
	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
	want := []string{"pair p1 finding", "pair p1 pairing", "pair p1 connecting", "pair p1 audio"}
	if !reflect.DeepEqual(api.progress, want) || api.pairs["p1"]["success"] != true {
		t.Fatalf("pair progress %v, result %v", api.progress, api.pairs["p1"])
	}
}

func TestScanWithoutAPoweredAdapterReportsTheError(t *testing.T) {
	for name, set := range map[string]func(*fakeBus){
		"no adapter":  func(b *fakeBus) { b.noAdapter = true },
		"powered off": func(b *fakeBus) { b.adapter.Powered = false },
	} {
		t.Run(name, func(t *testing.T) {
			n, bus, _, api := rig(t)
			set(bus)
			send(t, n, api, TopicScan, map[string]any{"request_id": "r1"})
			want := map[string]any{"devices": []any{}, "error": "Bluetooth hardware not available"}
			if got := asJSON(t, api.scans["r1"]); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestPairANewSpeaker(t *testing.T) {
	n, bus, _, api := rig(t)
	bus.inRange[speaker] = speakerDev() // in pairing mode, not yet known to BlueZ

	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": strings.ToLower(speaker), "role": "source"})

	if got := asJSON(t, api.pairs["p1"]); !reflect.DeepEqual(got, map[string]any{"success": true, "device_name": "JBL Flip 5"}) {
		t.Fatalf("pair result %v", got)
	}
	calls := bus.Calls()
	want := []string{"SetAdapter Pairable true", "Discover bredr", "StopDiscovery bredr", "Pair " + speaker, "SetTrusted " + speaker + " true", "Connect " + speaker}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls:\n got %v\nwant %v", calls, want)
	}
	d := bus.devices[speaker]
	if !d.Paired || !d.Trusted || !d.Connected {
		t.Errorf("device %+v", d)
	}
	r, ok := n.Store.Get(speaker)
	if !ok || !r.AutoConnect || r.Role != "source" || r.Name != "JBL Flip 5" {
		t.Errorf("saved %+v %v", r, ok)
	}
	if bus.agentCount == 0 {
		t.Error("the pairing agent was not registered before the pair")
	}
}

func TestPairABondedDeviceRemovesTheBondFirst(t *testing.T) {
	n, bus, _, api := rig(t)
	s := speakerDev()
	s.Paired = true
	bus.devices[speaker] = &s
	bus.inRange[speaker] = speakerDev()
	if err := n.Store.Paired(speaker, "JBL", "source", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Store.SetAutoConnect(speaker, false); err != nil {
		t.Fatal(err)
	}

	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})

	if api.pairs["p1"]["success"] != true {
		t.Fatalf("pair result %v", api.pairs["p1"])
	}
	calls := bus.Calls()
	if i, j := slices.Index(calls, "Remove "+speaker), slices.Index(calls, "Pair "+speaker); i < 0 || j < i {
		t.Errorf("remove before pair: %v", calls)
	}
	if slices.Index(calls, "Connect "+speaker) < slices.Index(calls, "Pair "+speaker) {
		t.Errorf("a connect before the pair ends the device's pairing mode: %v", calls)
	}
	if r, _ := n.Store.Get(speaker); r.AutoConnect {
		t.Error("a re-pair turned the user's auto-connect switch back on")
	}
}

func TestPairFailures(t *testing.T) {
	cases := map[string]struct {
		setup func(*fakeBus, *fakePulse)
		want  string
	}{
		"not in pairing mode": {func(*fakeBus, *fakePulse) {}, "Device not found — put it in pairing mode and keep it close"},
		"page timeout": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.pairErr[speaker] = BlueZError{Name: "org.bluez.Error.ConnectionAttemptFailed", Message: "Page Timeout"}
		}, "JBL Flip 5 didn't answer — put it in pairing mode and keep it close (org.bluez.Error.ConnectionAttemptFailed: Page Timeout)"},
		"br page timeout, no name": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = Device{Address: speaker, Alias: "AA-BB-CC-DD-EE-01"}
			b.pairErr[speaker] = BlueZError{Name: "org.bluez.Error.Failed", Message: "br-connection-page-timeout"}
		}, "The device didn't answer — put it in pairing mode and keep it close (org.bluez.Error.Failed: br-connection-page-timeout)"},
		"pair refused": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.pairErr[speaker] = BlueZError{Name: "org.bluez.Error.AuthenticationFailed", Message: "Authentication Failed"}
		}, "Pairing was refused by the device (org.bluez.Error.AuthenticationFailed: Authentication Failed)"},
		"pair rejected": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.pairErr[speaker] = BlueZError{Name: "org.bluez.Error.AuthenticationRejected"}
		}, "Pairing was refused by the device (org.bluez.Error.AuthenticationRejected)"},
		"connection attempt failed": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.pairErr[speaker] = BlueZError{Name: "org.bluez.Error.ConnectionAttemptFailed", Message: "Connection Refused"}
		}, "Couldn't connect to JBL Flip 5 to pair — keep it close and try again (org.bluez.Error.ConnectionAttemptFailed: Connection Refused)"},
		"pair times out": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.pairErr[speaker] = context.DeadlineExceeded
		}, "Pairing timed out — make sure the device is in pairing mode"},
		"other bluez error": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.pairErr[speaker] = BlueZError{Name: "org.bluez.Error.Failed", Message: "Resource temporarily unavailable"}
		}, "Failed to pair — make sure the device is in pairing mode (org.bluez.Error.Failed: Resource temporarily unavailable)"},
		"connect fails": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.connectErr[speaker] = BlueZError{Name: "org.bluez.Error.Failed", Message: "br-connection-page-timeout"}
		}, "Paired but failed to connect (org.bluez.Error.Failed: br-connection-page-timeout)"},
		"audio profile never comes up": {func(b *fakeBus, _ *fakePulse) {
			b.inRange[speaker] = speakerDev()
			b.onConnect = nil // ACL up, no A2DP sink
		}, "Paired but failed to connect"},
		"no adapter": {func(b *fakeBus, _ *fakePulse) { b.noAdapter = true }, "Bluetooth hardware not available"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			n, bus, pulse, api := rig(t)
			c.setup(bus, pulse)
			send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
			if got := asJSON(t, api.pairs["p1"]); !reflect.DeepEqual(got, map[string]any{"success": false, "error": c.want}) {
				t.Fatalf("got %v", got)
			}
			if _, ok := n.Store.Get(speaker); ok {
				t.Error("a failed pair was saved for the reconnect loop")
			}
			if name == "page timeout" && strings.Count(strings.Join(bus.Calls(), ","), "Pair "+speaker) != 2 {
				t.Errorf("a Page Timeout was not retried before telling the user: %v", bus.Calls())
			}
			if name == "audio profile never comes up" && !slices.Contains(bus.Calls(), "Disconnect "+speaker) {
				t.Errorf("the half-connected device wasn't disconnected (BlueZ keeps retrying AVDTP): %v", bus.Calls())
			}
		})
	}
}

// The first pair right after a scan often gets a Page Timeout in a second or two although the
// device is in pairing mode (#433, jarvis-pi4 with Arctis GameBuds): the node retries once.
func TestAFastPageTimeoutAfterAScanIsRetriedOnce(t *testing.T) {
	n, bus, _, api := rig(t)
	bus.inRange[speaker] = speakerDev()
	send(t, n, api, TopicScan, map[string]any{"request_id": "r1"})
	bus.pairErrOnce = map[string][]error{speaker: {BlueZError{Name: "org.bluez.Error.ConnectionAttemptFailed", Message: "Page Timeout"}}}

	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})

	if got := asJSON(t, api.pairs["p1"]); !reflect.DeepEqual(got, map[string]any{"success": true, "device_name": "JBL Flip 5"}) {
		t.Fatalf("pair result %v", got)
	}
	var pairs int
	for _, c := range bus.Calls() {
		if c == "Pair "+speaker {
			pairs++
		}
	}
	if pairs != 2 {
		t.Errorf("Pair called %d times: %v", pairs, bus.Calls())
	}
}

func TestConnectInProgressIsWaitedFor(t *testing.T) {
	n, bus, _, api := rig(t)
	bus.inRange[speaker] = speakerDev()
	bus.connectErr[speaker] = ErrInProgress
	hook := bus.onConnect
	bus.onConnect = nil
	go func() { // the connect lands a moment later
		time.Sleep(10 * time.Millisecond)
		bus.mu.Lock()
		if d := bus.devices[speaker]; d != nil {
			d.Connected = true
		}
		bus.mu.Unlock()
		hook(speaker)
	}()
	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
	if api.pairs["p1"]["success"] != true {
		t.Fatalf("got %v", api.pairs["p1"])
	}
}

func TestAPhoneAsSourceIsLoopedIntoTheNodesSpeaker(t *testing.T) {
	n, bus, pulse, api := rig(t)
	bus.inRange[phone] = phoneDev()
	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": phone, "role": "sink"})
	if api.pairs["p1"]["success"] != true {
		t.Fatalf("got %v", api.pairs["p1"])
	}
	src := "bluez_source.11_22_33_44_55_66.a2dp_source"
	want := []Module{{ID: "21", Name: "module-loopback", Args: "source=" + src + " latency_msec=100"}}
	if !reflect.DeepEqual(pulse.modules, want) {
		t.Fatalf("modules %v", pulse.modules)
	}
	n.routeAudio(context.Background(), phone, "sink") // a reconnect: no second loopback
	if len(pulse.modules) != 1 {
		t.Fatalf("loopback loaded twice: %v", pulse.modules)
	}
	send(t, n, api, TopicDisconnect, map[string]any{"mac_address": phone})
	if len(pulse.modules) != 0 || bus.devices[phone].Connected {
		t.Fatalf("after disconnect: modules %v, device %+v", pulse.modules, bus.devices[phone])
	}
}

const speakerSink = "bluez_sink.AA_BB_CC_DD_EE_01.a2dp_sink"

// A paired speaker or headphones take the node's output (#433: "route output to it"), and give
// it back to the node's own sink when they go.
func TestASpeakerBecomesTheDefaultSinkUntilItGoes(t *testing.T) {
	for name, leave := range map[string]map[string]any{
		"disconnect": nil,
		"release":    {"forget": false},
		"forget":     {"forget": true},
	} {
		t.Run(name, func(t *testing.T) {
			n, bus, pulse, api := rig(t)
			bus.inRange[speaker] = speakerDev()
			send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker, "role": "source"})
			if api.pairs["p1"]["success"] != true {
				t.Fatalf("pair %v", api.pairs["p1"])
			}
			if len(pulse.modules) != 0 {
				t.Fatalf("a speaker got modules: %v", pulse.modules)
			}
			if pulse.def != speakerSink {
				t.Fatalf("default sink %q, want the speaker's", pulse.def)
			}
			topic, payload := TopicDisconnect, map[string]any{"mac_address": speaker}
			if leave != nil {
				topic, payload["forget"] = TopicRelease, leave["forget"]
			}
			bus.onDisconnect = nil // Pulse keeps the sink a moment: the node hands the output back itself
			send(t, n, api, topic, payload)
			if pulse.def != "jarvis_output" {
				t.Fatalf("after %s the default sink is %q", name, pulse.def)
			}
		})
	}
}

func TestAPhoneAsSourceLeavesTheDefaultSinkAlone(t *testing.T) {
	n, bus, pulse, api := rig(t)
	bus.inRange[phone] = phoneDev()
	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": phone, "role": "sink"})
	if pulse.def != "jarvis_output" {
		t.Fatalf("default sink %q", pulse.def)
	}
}

// #433: every pair ended "connected, but its audio profile did not come up" while bluetoothd
// had the A2DP transport ready. BlueZ's transport now counts: a device whose profile is up is
// never disconnected because Pulse is slow or out of reach, and why Pulse didn't answer is
// logged.
func TestTheAudioProfileUpInBlueZIsAConnect(t *testing.T) {
	t.Run("pulse unreachable", func(t *testing.T) {
		n, bus, pulse, api := rig(t)
		logs := &logRecorder{}
		n.Log = slog.New(logs)
		bus.inRange[speaker] = speakerDev()
		pulse.listErr = errors.New("pactl list sinks short: exit status 1: Connection failure: Connection refused")
		send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
		if got := asJSON(t, api.pairs["p1"]); !reflect.DeepEqual(got, map[string]any{"success": true, "device_name": "JBL Flip 5"}) {
			t.Fatalf("pair %v", got)
		}
		if slices.Contains(bus.Calls(), "Disconnect "+speaker) {
			t.Errorf("a device with its audio profile up was disconnected: %v", bus.Calls())
		}
		var pactl int
		for _, r := range logs.all() {
			if r == "WARN bluetooth: pactl failed" {
				pactl++
			}
		}
		if pactl != 1 {
			t.Errorf("pactl's error logged %d times, want once: %v", pactl, logs.all())
		}
		if !slices.Contains(logs.all(), "WARN bluetooth: audio profile up in BlueZ, but PulseAudio has no sink for it") {
			t.Errorf("logs %v", logs.all())
		}
	})
	t.Run("the card's profile is off", func(t *testing.T) {
		n, bus, pulse, api := rig(t)
		bus.inRange[speaker] = speakerDev()
		bus.onConnect = func(mac string) { // the card, no sink until a profile is chosen
			bus.mu.Lock()
			bus.transport[mac] = true
			bus.mu.Unlock()
			pulse.mu.Lock()
			pulse.cards = append(pulse.cards, "bluez_card.AA_BB_CC_DD_EE_01")
			pulse.mu.Unlock()
		}
		pulse.onProfile = func(card, profile string) {
			if card == "bluez_card.AA_BB_CC_DD_EE_01" && profile == "a2dp_sink" {
				pulse.add(speakerSink, "")
			}
		}
		send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
		if api.pairs["p1"]["success"] != true {
			t.Fatalf("pair %v", api.pairs["p1"])
		}
		if pulse.def != speakerSink {
			t.Fatalf("default sink %q (profiles %v)", pulse.def, pulse.profiles)
		}
	})
}

func TestRelease(t *testing.T) {
	setup := func(t *testing.T) (*Node, *fakeBus, *fakeAPI) {
		n, bus, _, api := rig(t)
		bus.inRange[speaker] = speakerDev()
		send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
		return n, bus, api
	}
	t.Run("keeps the pairing, stops reconnecting", func(t *testing.T) {
		n, bus, api := setup(t)
		send(t, n, api, TopicRelease, map[string]any{"mac_address": speaker, "forget": false})
		d := bus.devices[speaker]
		if d == nil || !d.Paired || d.Connected {
			t.Fatalf("device %+v", d)
		}
		if r, ok := n.Store.Get(speaker); !ok || r.AutoConnect {
			t.Fatalf("record %+v %v", r, ok)
		}
	})
	t.Run("forget removes the pairing and the record", func(t *testing.T) {
		n, bus, api := setup(t)
		send(t, n, api, TopicRelease, map[string]any{"mac_address": speaker, "forget": true})
		if bus.devices[speaker] != nil {
			t.Fatal("still known to BlueZ")
		}
		if _, ok := n.Store.Get(speaker); ok {
			t.Fatal("record kept")
		}
		calls := bus.Calls()
		if slices.Index(calls[6:], "Disconnect "+speaker) > slices.Index(calls[6:], "Remove "+speaker) {
			t.Errorf("disconnect before remove: %v", calls)
		}
	})
}

// logRecorder keeps the records a Node logs at Info and above, as "LEVEL msg".
type logRecorder struct {
	mu   sync.Mutex
	recs []string
}

func (l *logRecorder) Enabled(_ context.Context, lv slog.Level) bool { return lv >= slog.LevelInfo }
func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r.Level.String()+" "+r.Message)
	return nil
}
func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logRecorder) WithGroup(string) slog.Handler      { return l }
func (l *logRecorder) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.recs)
}

func TestForgetIsIdempotent(t *testing.T) {
	n, bus, _, api := rig(t)
	logs := &logRecorder{}
	n.Log = slog.New(logs)
	bus.inRange[speaker] = speakerDev()
	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
	send(t, n, api, TopicRelease, map[string]any{"mac_address": speaker, "forget": true})
	send(t, n, api, TopicRelease, map[string]any{"mac_address": speaker, "forget": true}) // BlueZ no longer knows it
	// The app may still send its auto-connect switch for it.
	send(t, n, api, TopicAutoConnect, map[string]any{"mac_address": speaker, "enabled": true})

	if bus.devices[speaker] != nil {
		t.Fatal("still known to BlueZ")
	}
	if _, ok := n.Store.Get(speaker); ok {
		t.Fatal("record kept")
	}
	var forgotten int
	for _, r := range logs.all() {
		if strings.HasPrefix(r, "WARN") || strings.HasPrefix(r, "ERROR") {
			t.Errorf("a repeated forget logged %q", r)
		}
		if r == "INFO bluetooth: device forgotten" {
			forgotten++
		}
	}
	if forgotten != 2 {
		t.Errorf("forgotten logged %d times: %v", forgotten, logs.all())
	}
}

func TestAFailedRemoveIsNotForgotten(t *testing.T) {
	n, bus, _, api := rig(t)
	logs := &logRecorder{}
	n.Log = slog.New(logs)
	bus.inRange[speaker] = speakerDev()
	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker})
	bus.removeErr = BlueZError{Name: "org.bluez.Error.Failed", Message: "Operation failed"}
	send(t, n, api, TopicRelease, map[string]any{"mac_address": speaker, "forget": true})
	got := logs.all()
	if slices.Contains(got, "INFO bluetooth: device forgotten") || !slices.Contains(got, "WARN bluetooth: remove failed") {
		t.Fatalf("logs %v", got)
	}
}

func TestAutoConnectSwitch(t *testing.T) {
	n, _, _, api := rig(t)
	if err := n.Store.Paired(speaker, "JBL", "source", time.Now()); err != nil {
		t.Fatal(err)
	}
	send(t, n, api, TopicAutoConnect, map[string]any{"mac_address": speaker, "enabled": false})
	if r, _ := n.Store.Get(speaker); r.AutoConnect {
		t.Fatal("still on")
	}
	send(t, n, api, TopicAutoConnect, map[string]any{"mac_address": speaker, "enabled": true})
	if r, _ := n.Store.Get(speaker); !r.AutoConnect {
		t.Fatal("still off")
	}
	send(t, n, api, TopicAutoConnect, map[string]any{"mac_address": phone, "enabled": true})
	if _, ok := n.Store.Get(phone); ok {
		t.Fatal("a device the node never paired got a record")
	}
	send(t, n, api, TopicAutoConnect, map[string]any{"mac_address": speaker}) // no enabled: ignored
	if r, _ := n.Store.Get(speaker); !r.AutoConnect {
		t.Fatal("a payload without enabled changed it")
	}
}

func TestDiscoverableSetsTheAdapterInOrder(t *testing.T) {
	for name, c := range map[string]struct {
		payload map[string]any
		timeout uint32
	}{"jarvisd's default": {map[string]any{"timeout": 120}, 120}, "no timeout": {map[string]any{}, 120}, "60 s": {map[string]any{"timeout": 60}, 60}} {
		t.Run(name, func(t *testing.T) {
			n, bus, _, api := rig(t)
			send(t, n, api, TopicDiscoverable, c.payload)
			want := []string{"SetAdapter Powered true", "SetAdapter Pairable true", "SetAdapter DiscoverableTimeout " + itoa(c.timeout), "SetAdapter Discoverable true"}
			if got := bus.Calls(); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v", got)
			}
			if v, ok := bus.props["DiscoverableTimeout"].(uint32); !ok || v != c.timeout {
				t.Fatalf("DiscoverableTimeout %#v: BlueZ's type is uint32", bus.props["DiscoverableTimeout"])
			}
		})
	}
}

func itoa(v uint32) string { b, _ := json.Marshal(v); return string(b) }

func TestReconnectConnectsTheSavedDevicesThatWantIt(t *testing.T) {
	n, bus, pulse, _ := rig(t)
	off := "AA:BB:CC:DD:EE:02"
	for _, d := range []Device{speakerDev(), phoneDev(), {Address: off, Name: "Off", Icon: "audio-headphones"}} {
		d.Paired = true
		bus.devices[d.Address] = &d
	}
	ctx := context.Background()
	_ = n.Store.Paired(speaker, "JBL", "source", time.Now())
	_ = n.Store.Paired(phone, "iPhone", "sink", time.Now())
	_ = n.Store.Paired(off, "Off", "source", time.Now())
	_, _ = n.Store.SetAutoConnect(off, false)

	if got := n.Reconnect(ctx); got != 2 {
		t.Fatalf("reconnected %d, want 2", got)
	}
	if !bus.devices[speaker].Connected || !bus.devices[phone].Connected || bus.devices[off].Connected {
		t.Fatalf("connected: speaker %v phone %v off %v", bus.devices[speaker].Connected, bus.devices[phone].Connected, bus.devices[off].Connected)
	}
	if len(pulse.modules) != 1 {
		t.Errorf("the phone's loopback after a reconnect: %v", pulse.modules)
	}
	before := len(bus.Calls())
	if got := n.Reconnect(ctx); got != 0 {
		t.Fatalf("connected devices were connected again (%d)", got)
	}
	for _, c := range bus.Calls()[before:] {
		if strings.HasPrefix(c, "Connect") {
			t.Fatalf("connected devices were connected again: %v", c)
		}
	}
}

func TestStartRegistersTheAgentAndReconnects(t *testing.T) {
	n, bus, _, _ := rig(t)
	s := speakerDev()
	s.Paired = true
	bus.devices[speaker] = &s
	_ = n.Store.Paired(speaker, "JBL", "source", time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.Start(ctx)
	waitFor(t, func() bool {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		return bus.devices[speaker].Connected
	})
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.agentCount == 0 || bus.props["Pairable"] != true {
		t.Fatalf("agent %d, pairable %v", bus.agentCount, bus.props["Pairable"])
	}
}

func TestAPhoneInitiatedPairIsTrustedAndConnected(t *testing.T) {
	n, bus, _, _ := rig(t)
	d := phoneDev()
	d.Paired = true // the phone paired with the node while it was discoverable
	bus.devices[phone] = &d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.Start(ctx)
	bus.paired <- phone
	waitFor(t, func() bool {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		return bus.devices[phone].Trusted && bus.devices[phone].Connected
	})
}

func TestMalformedRequestsAreIgnored(t *testing.T) {
	n, bus, _, api := rig(t)
	bus.inRange[speaker] = speakerDev()
	for _, c := range []struct {
		topic   string
		payload string
	}{
		{TopicScan, `{}`},
		{TopicScan, `not json`},
		{TopicScan, `{"request_id": "` + strings.Repeat("x", 65) + `"}`},
		{TopicPair, `{"request_id": "p1"}`},
		{TopicPair, `{"request_id": "p1", "mac_address": "11"}`},
		{TopicDisconnect, `{"mac_address": "nope"}`},
		{TopicRelease, `{}`},
	} {
		n.Handle(context.Background(), api, c.topic, []byte(c.payload))
	}
	if len(api.scans)+len(api.pairs) != 0 || len(bus.Calls()) != 0 {
		t.Fatalf("acted on a malformed request: scans %v pairs %v calls %v", api.scans, api.pairs, bus.Calls())
	}
}

func TestARepeatedNudgeRunsOnce(t *testing.T) {
	n, bus, _, api := rig(t)
	send(t, n, api, TopicScan, map[string]any{"request_id": "r1"})
	send(t, n, api, TopicScan, map[string]any{"request_id": "r1"})
	if got := len(bus.discovering); got != 2 {
		t.Fatalf("discoveries %d, want one scan (2 transports)", got)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", StoreFile)
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	if err := st.Paired(speaker, "JBL", "source", at); err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Record{{MAC: speaker, Name: "JBL", Role: "source", AutoConnect: true, LastConnected: at}}
	if got := again.All(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
