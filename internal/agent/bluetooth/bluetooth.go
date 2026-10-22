// Package bluetooth is the Pi node's Bluetooth (go-pi-node.md §7 item 5.8): the six
// jarvis/nodes/{id}/bluetooth-* topics jarvisd publishes (internal/modules/cc smarthome.go),
// ported from the Python node (N:scripts/mqtt_tts_listener.py _handle_bluetooth_*,
// N:services/bluetooth_scan_handler.py, N:core/platform_abstraction.py PiBluetoothProvider,
// N:services/bluetooth_pair_agent.py and the reconnect loop in N:scripts/main.py). The Python
// node drives bluetoothctl; this talks to BlueZ over D-Bus (Bus, bluez_linux.go) and to
// PulseAudio through pactl (Pulse), so both are fakes in the tests.
//
// Audio: a speaker or headphones the node plays to (role "source") become Pulse's default sink
// while connected, so the node's replies and music go to them, and the node's own sink
// (jarvis_output, the HAT) is the default again when they disconnect or are released (#433;
// the Python node left TTS on the HAT). A phone paired as a source (role "sink": the Pi is its
// speaker) gets a module-loopback into the default sink, unloaded again on disconnect and
// release.
package bluetooth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The sub-topics of jarvis/nodes/{id}/ (internal/modules/cc smarthome_jobs.go: btScanJob,
// btPairJob, handleBTFire).
const (
	TopicScan          = "bluetooth-scan"
	TopicPair          = "bluetooth-pair"
	TopicDisconnect    = "bluetooth-disconnect"
	TopicDiscoverable  = "bluetooth-discoverable"
	TopicRelease       = "bluetooth-release"
	TopicAutoConnect   = "bluetooth-auto-connect"
	defaultDiscoverSec = 120 // jarvisd's handleBTFire default
)

// Topics are the sub-topics Handle answers.
var Topics = []string{TopicScan, TopicPair, TopicDisconnect, TopicDiscoverable, TopicRelease, TopicAutoConnect}

// Errors a Bus returns for BlueZ's own (org.bluez.Error.*).
var (
	ErrNoAdapter     = errors.New("no Bluetooth adapter")
	ErrUnknownDevice = errors.New("device not known to BlueZ")     // no Device1 for the address
	ErrAlreadyExists = errors.New("org.bluez.Error.AlreadyExists") // Pair on a bonded device
	ErrInProgress    = errors.New("org.bluez.Error.InProgress")    // a connect already running
)

// BlueZError is any other org.bluez.Error.*: its D-Bus name and BlueZ's text ("Page Timeout").
type BlueZError struct{ Name, Message string }

func (e BlueZError) Error() string {
	if e.Message == "" {
		return e.Name
	}
	return e.Name + ": " + e.Message
}

// Adapter is the state of the controller (org.bluez.Adapter1).
type Adapter struct {
	Powered bool
}

// Device is one org.bluez.Device1.
type Device struct {
	Address   string // AA:BB:CC:DD:EE:FF
	Name      string // the name it broadcast; "" if none
	Alias     string // BlueZ's display name (the address with dashes when there is no Name)
	Icon      string // e.g. "audio-headset", "phone"
	UUIDs     []string
	Paired    bool
	Connected bool
	Trusted   bool
	RSSI      int16 // set while a discovery sees it (BlueZ drops it when the discovery stops); 0: not seen
}

// Connection is a device's link coming up or going down, whoever started it.
type Connection struct {
	MAC       string
	Connected bool
}

// Bus is BlueZ on the system bus. Addresses are MACs; methods on an address BlueZ has no
// device for return ErrUnknownDevice.
type Bus interface {
	Adapter(ctx context.Context) (Adapter, error)
	// SetAdapter sets one Adapter1 property: Powered, Pairable, Discoverable (bool) or
	// DiscoverableTimeout (uint32).
	SetAdapter(ctx context.Context, prop string, v any) error
	Devices(ctx context.Context) ([]Device, error)
	// Discover starts discovery on one transport ("bredr", "le" or "auto") until stop.
	Discover(ctx context.Context, transport string) (stop func(), err error)
	Remove(ctx context.Context, mac string) error
	Pair(ctx context.Context, mac string) error
	Connect(ctx context.Context, mac string) error
	Disconnect(ctx context.Context, mac string) error
	SetTrusted(ctx context.Context, mac string, on bool) error
	// RegisterAgent registers the node's pairing agent (NoInputNoOutput, accepts everything) as
	// BlueZ's default; again after bluetoothd restarted is fine.
	RegisterAgent(ctx context.Context) error
	// Paired delivers the address of each device that became paired, the phone-initiated
	// pairings (the node discoverable) included.
	Paired() <-chan string
	// Connections delivers each device's Connected changing, the device's own reconnects
	// included (headphones turned back on).
	Connections() <-chan Connection
	// AudioTransport reports whether BlueZ has a media transport (org.bluez.MediaTransport1)
	// under the device: its A2DP or HFP audio profile is connected, whatever Pulse makes of it.
	AudioTransport(ctx context.Context, mac string) (bool, error)
}

// API is the node's command-center client (*node.CC).
type API interface {
	PostBluetoothScanResult(ctx context.Context, rid string, body map[string]any) error
	PostBluetoothPairResult(ctx context.Context, rid string, body map[string]any) error
}

// ProgressAPI is what the node posts while a scan or pair runs (#433): the scan's devices so far
// and the pair's step, so the app can show them. Best effort: an API without it, or a jarvisd
// older than the routes (404), just gets the results.
type ProgressAPI interface {
	PostBluetoothScanProgress(ctx context.Context, rid string, body map[string]any) error
	PostBluetoothPairProgress(ctx context.Context, rid string, body map[string]any) error
}

// The pair's steps, as the app gets them (ProgressAPI).
const (
	StepFinding    = "finding"    // the rescan for a device BlueZ doesn't know
	StepPairing    = "pairing"    // Device1.Pair (and its retry)
	StepConnecting = "connecting" // trust and Connect
	StepAudio      = "audio"      // the wait for the audio profile and the output routing
)

// Node handles the topics. Fill it in, then Start it once.
type Node struct {
	Bus   Bus
	Pulse Pulse  // nil: no audio routing (no Pulse)
	Store *Store // the devices to reconnect; nil keeps none
	Log   *slog.Logger
	// RestartPlayers restarts the music players whose Pulse stream may have died when the default
	// sink moved to or from a speaker (#478: shairport-sync's stayed dead); nil restarts none. An
	// error is logged, never fatal.
	RestartPlayers func(ctx context.Context) error

	// Timings (zero: the Python node's): ScanFor is the most a scan's discovery runs (10 s; N: ran
	// BR/EDR then LE, max(12/2, 10) s each), and QuietFor ends it sooner once no new device has
	// turned up for that long after the first (4 s; #469); FindFor is the rescan for a device BlueZ doesn't
	// know before a pair (12 s); AudioFor and AudioStable are the wait for the audio profile
	// after a connect (a bluez sink or source present for 2 s within 20 s; N: 15 s); BusyFor is the poll
	// for a connect already in progress (10 s); SourceFor is the wait for a phone's a2dp_source
	// before the loopback (5 s); FirstReconnect and ReconnectEvery pace the reconnect loop (30 s
	// after start, then every 10 min).
	// PageRetryAfter is the pause before the one retry of a pair that got a fast Page Timeout (2 s);
	// SinkFor is the wait for a speaker's bluez sink before the output moves to it (10 s).
	ScanFor, QuietFor, FindFor, AudioFor, AudioStable, BusyFor, SourceFor, PageRetryAfter, SinkFor time.Duration
	FirstReconnect, ReconnectEvery                                                                 time.Duration
	ProgressEvery                                                                                  time.Duration // a scan\'s devices so far, at most this often (1 s)
	Poll                                                                                           time.Duration // 500 ms

	op       sync.Mutex // one radio operation at a time
	mu       sync.Mutex
	pairing  map[string]bool // addresses the pair handler is working on
	seen     map[string]bool // request ids taken
	homeSink string          // the default sink before a speaker took the output
	routedTo string          // the speaker's sink the node made the default, until it gives it back
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (n *Node) log() *slog.Logger {
	if n.Log != nil {
		return n.Log
	}
	return slog.Default()
}

var macRe = regexp.MustCompile(`^[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}$`)

// normMAC is mac in BlueZ's form (upper case, colons), or "" if it isn't one.
func normMAC(mac string) string {
	if !macRe.MatchString(mac) {
		return ""
	}
	return strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
}

// take reports whether rid is new (MQTT may deliver a nudge twice).
func (n *Node) take(rid string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	if n.seen[rid] {
		return false
	}
	if len(n.seen) > 256 {
		n.seen = map[string]bool{}
	}
	n.seen[rid] = true
	return true
}

type nudge struct {
	RequestID string `json:"request_id"`
	Role      string `json:"role"`
	MAC       string `json:"mac_address"`
	Timeout   *int   `json:"timeout"`
	Forget    bool   `json:"forget"`
	Enabled   *bool  `json:"enabled"`
}

// Handle answers one message on topic (one of Topics). Results go back through api. There is no
// verify route for Bluetooth requests: a scan or pair result for a request jarvisd didn't issue
// to this node is refused there (404), and the fire-and-forget ones only act on this node's
// own adapter and devices.
func (n *Node) Handle(ctx context.Context, api API, topic string, payload []byte) {
	var p nudge
	if err := json.Unmarshal(payload, &p); err != nil {
		n.log().Warn("bluetooth: invalid JSON", "topic", topic)
		return
	}
	needRID := topic == TopicScan || topic == TopicPair
	if needRID && (p.RequestID == "" || len(p.RequestID) > 64) {
		n.log().Warn("bluetooth: request without a request_id", "topic", topic)
		return
	}
	mac := normMAC(p.MAC)
	if topic != TopicScan && topic != TopicDiscoverable && mac == "" {
		n.log().Warn("bluetooth: request without a valid mac_address", "topic", topic)
		return
	}
	if needRID && !n.take(p.RequestID) {
		return
	}
	role := p.Role
	if role == "" {
		role = "source"
	}
	switch topic {
	case TopicScan:
		n.log().Info("bluetooth: scan requested", "request_id", p.RequestID, "role", role)
		n.scanAndPost(ctx, api, p.RequestID)
	case TopicPair:
		n.log().Info("bluetooth: pair requested", "request_id", p.RequestID, "mac", mac, "role", role)
		n.pairAndPost(ctx, api, p.RequestID, mac, role)
	case TopicDisconnect:
		n.disconnect(ctx, mac)
	case TopicDiscoverable:
		t := defaultDiscoverSec
		if p.Timeout != nil && *p.Timeout >= 0 {
			t = *p.Timeout
		}
		n.discoverable(ctx, t)
	case TopicRelease:
		n.release(ctx, mac, p.Forget)
	case TopicAutoConnect:
		if p.Enabled == nil {
			n.log().Warn("bluetooth: auto-connect without enabled", "mac", mac)
			return
		}
		n.setAutoConnect(mac, *p.Enabled)
	default:
		n.log().Warn("bluetooth: unknown topic", "topic", topic)
	}
}

// available is N:'s is_available: an adapter, powered.
func (n *Node) available(ctx context.Context) bool {
	a, err := n.Bus.Adapter(ctx)
	return err == nil && a.Powered
}

// --- scan ------------------------------------------------------------------------------------

// ScanDevice is one entry of the scan result jarvisd stores (results_json); the app's status
// page reads paired and connected from the latest one.
type ScanDevice struct {
	Name       string `json:"name"`
	MACAddress string `json:"mac_address"`
	DeviceType string `json:"device_type"` // audio_sink | audio_source | phone | unknown
	Paired     bool   `json:"paired"`
	Connected  bool   `json:"connected"`
}

// ScanBody is the scan result jarvisd takes (POST …/bluetooth-scan/{rid}/results): the devices,
// or none and the error.
func ScanBody(devices []ScanDevice, err error) map[string]any {
	if err != nil {
		return map[string]any{"devices": []ScanDevice{}, "error": err.Error()}
	}
	if devices == nil {
		devices = []ScanDevice{}
	}
	return map[string]any{"devices": devices}
}

// progressTimeout bounds a progress post: it must not hold up the radio work.
const progressTimeout = 3 * time.Second

func (n *Node) scanAndPost(ctx context.Context, api API, rid string) {
	var found func([]ScanDevice)
	if pa, ok := api.(ProgressAPI); ok {
		found = func(devices []ScanDevice) {
			pctx, cancel := context.WithTimeout(ctx, progressTimeout)
			defer cancel()
			if err := pa.PostBluetoothScanProgress(pctx, rid, map[string]any{"progress": "scanning", "devices": devices}); err != nil {
				n.log().Debug("bluetooth: scan progress not posted", "request_id", rid, "err", err)
			}
		}
	}
	devices, err := n.scan(ctx, found)
	if err != nil {
		n.log().Error("bluetooth: scan failed", "request_id", rid, "err", err)
	} else {
		n.log().Info("bluetooth: scan complete", "request_id", rid, "devices", len(devices))
	}
	if err := api.PostBluetoothScanResult(ctx, rid, ScanBody(devices, err)); err != nil {
		n.log().Error("bluetooth: scan result not uploaded", "request_id", rid, "err", err)
	}
}

// Scan is N:'s scan: drop the devices BlueZ remembers but isn't paired with (so the list is
// what's in range now), discover, and list the devices with a real name and every paired one.
// Unlike N:, paired and connected are the devices' own, and the discovery is BR/EDR only and
// ends early (#469): N:'s 10 s of LE after 10 s of BR/EDR found nothing the node can pair for
// audio (A2DP and HFP are BR/EDR), and headphones left pairing mode before the 20 s were over.
func (n *Node) Scan(ctx context.Context) ([]ScanDevice, error) { return n.scan(ctx, nil) }

// scan is Scan, calling found (when not nil) with the list so far each time it grows during the
// discovery, at most every ProgressEvery.
func (n *Node) scan(ctx context.Context, found func([]ScanDevice)) ([]ScanDevice, error) {
	n.op.Lock()
	defer n.op.Unlock()
	if !n.available(ctx) {
		return nil, errors.New("Bluetooth hardware not available")
	}
	if old, err := n.Bus.Devices(ctx); err == nil {
		for _, d := range old {
			if !d.Paired && !d.Connected {
				_ = n.Bus.Remove(ctx, d.Address)
			}
		}
	}
	before, _ := n.listed(ctx)
	stop, err := n.Bus.Discover(ctx, "bredr")
	if err != nil {
		n.log().Warn("bluetooth: discovery failed", "err", err)
	} else {
		err = n.discoverFor(ctx, found, len(before))
		stop()
		if err != nil {
			return nil, err
		}
	}
	return n.listed(ctx)
}

// discoverFor waits while a discovery runs: until no new device has turned up for QuietFor
// since the last one (so not before the first), or ScanFor in all. found gets the list so far
// whenever it grew; before is the list's length when the discovery started.
func (n *Node) discoverFor(ctx context.Context, found func([]ScanDevice), before int) error {
	end := time.Now().Add(or(n.ScanFor, 10*time.Second))
	quiet := or(n.QuietFor, 4*time.Second)
	reported, known := 0, before
	var grew time.Time
	for {
		step := min(time.Until(end), or(n.ProgressEvery, time.Second))
		if step <= 0 {
			return nil
		}
		if err := sleep(ctx, step); err != nil {
			return err
		}
		list, err := n.listed(ctx)
		if err != nil {
			continue
		}
		if len(list) > known {
			known, grew = len(list), time.Now()
		}
		if found != nil && len(list) > reported {
			reported = len(list)
			found(list)
		}
		if !grew.IsZero() && time.Since(grew) >= quiet {
			return nil
		}
	}
}

// listed is the devices BlueZ knows that have a real name, and the paired ones whatever their
// name (by the name the node saved, else the address: #469, a bond the app didn't list couldn't
// be forgotten), as the scan reports them.
func (n *Node) listed(ctx context.Context) ([]ScanDevice, error) {
	all, err := n.Bus.Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := []ScanDevice{}
	seen := map[string]bool{}
	for _, d := range all {
		name := displayName(d)
		if name == "" && d.Paired {
			name = d.Address
			if r, ok := n.storeGet(d.Address); ok && r.Name != "" {
				name = r.Name
			}
		}
		if name == "" || seen[d.Address] {
			continue
		}
		seen[d.Address] = true
		out = append(out, ScanDevice{Name: name, MACAddress: d.Address, DeviceType: deviceType(d), Paired: d.Paired, Connected: d.Connected})
	}
	return out, nil
}

// displayName is the device's name, or "" when it broadcast none (BlueZ then uses the address
// as the alias: N: drops those).
func displayName(d Device) string {
	for _, s := range []string{d.Name, d.Alias} {
		s = strings.TrimSpace(s)
		if s != "" && !macRe.MatchString(s) {
			return s
		}
	}
	return ""
}

// Service class UUIDs (Bluetooth assigned numbers) that bluetoothctl info shows by name.
const (
	uuidAudioSource = "0000110a-0000-1000-8000-00805f9b34fb"
	uuidAudioSink   = "0000110b-0000-1000-8000-00805f9b34fb"
	uuidHandsfree   = "0000111e-0000-1000-8000-00805f9b34fb"
	uuidHandsfreeAG = "0000111f-0000-1000-8000-00805f9b34fb"
)

func hasUUID(d Device, u string) bool {
	for _, x := range d.UUIDs {
		if strings.EqualFold(x, u) {
			return true
		}
	}
	return false
}

// deviceType is N:'s _classify_device, in its order: an audio icon or an Audio Sink is
// audio_sink, a phone icon phone, an Audio Source audio_source.
func deviceType(d Device) string {
	icon := strings.ToLower(d.Icon)
	switch {
	case strings.HasPrefix(icon, "audio") || hasUUID(d, uuidAudioSink):
		return "audio_sink"
	case strings.HasPrefix(icon, "phone"):
		return "phone"
	case hasUUID(d, uuidAudioSource):
		return "audio_source"
	}
	return "unknown"
}

// isAudio is N:'s _is_audio_device: the devices whose connect waits for the audio profile.
func isAudio(d Device) bool {
	return strings.HasPrefix(strings.ToLower(d.Icon), "audio") || hasUUID(d, uuidAudioSink) ||
		hasUUID(d, uuidAudioSource) || hasUUID(d, uuidHandsfree) || hasUUID(d, uuidHandsfreeAG)
}

func (n *Node) storeGet(mac string) (Record, bool) {
	if n.Store == nil {
		return Record{}, false
	}
	return n.Store.Get(mac)
}

func (n *Node) device(ctx context.Context, mac string) (Device, bool) {
	all, err := n.Bus.Devices(ctx)
	if err != nil {
		return Device{}, false
	}
	for _, d := range all {
		if d.Address == mac {
			return d, true
		}
	}
	return Device{}, false
}

// --- pair ------------------------------------------------------------------------------------

// PairBody is the pair result jarvisd takes (POST …/bluetooth/pair/{rid}/results). A failure
// that left the device bonded says paired: true, so the app lists it and it can be forgotten.
func PairBody(name string, err error) map[string]any {
	if err != nil {
		body := map[string]any{"success": false, "error": err.Error()}
		if errors.As(err, new(bondedError)) {
			body["paired"] = true
		}
		return body
	}
	return map[string]any{"success": true, "device_name": name}
}

func (n *Node) pairAndPost(ctx context.Context, api API, rid, mac, role string) {
	var step func(string)
	if pa, ok := api.(ProgressAPI); ok {
		step = func(s string) {
			pctx, cancel := context.WithTimeout(ctx, progressTimeout)
			defer cancel()
			if err := pa.PostBluetoothPairProgress(pctx, rid, map[string]any{"progress": s}); err != nil {
				n.log().Debug("bluetooth: pair progress not posted", "request_id", rid, "err", err)
			}
		}
	}
	name, err := n.pair(ctx, mac, role, step)
	if err != nil {
		n.log().Warn("bluetooth: pair failed", "request_id", rid, "mac", mac, "err", err)
	} else {
		n.log().Info("bluetooth: pair complete", "request_id", rid, "device", name)
	}
	if err := api.PostBluetoothPairResult(ctx, rid, PairBody(name, err)); err != nil {
		n.log().Error("bluetooth: pair result not uploaded", "request_id", rid, "err", err)
	}
}

// Pair is N:'s run_bluetooth_pair_and_upload: pair (after a short rescan when BlueZ doesn't
// know the device), trust, connect, route its audio and remember it for the reconnect loop. It
// never tries a plain connect first on a device that isn't bonded: that ends the device's
// pairing mode (bluetooth-pairing-state-2026-05-07.md, failure mode 4). A bonded one is
// connected with its bond first (rebond). It returns the device's name.
func (n *Node) Pair(ctx context.Context, mac, role string) (string, error) {
	return n.pair(ctx, mac, role, nil)
}

// pair is Pair, calling step (when not nil) with each Step* as it starts.
func (n *Node) pair(ctx context.Context, mac, role string, step func(string)) (string, error) {
	if step == nil {
		step = func(string) {}
	}
	n.op.Lock()
	defer n.op.Unlock()
	n.mu.Lock()
	if n.pairing == nil {
		n.pairing = map[string]bool{}
	}
	n.pairing[mac] = true
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.pairing, mac)
		n.mu.Unlock()
	}()
	if !n.available(ctx) {
		return "", errors.New("Bluetooth hardware not available")
	}
	if d, ok := n.device(ctx, mac); ok && d.Paired {
		name, err, again := n.rebond(ctx, mac, role, step)
		if !again {
			return name, err
		}
	}
	// The agent answers BlueZ's questions during the pair; make sure it is there.
	if err := n.Bus.RegisterAgent(ctx); err != nil {
		n.log().Warn("bluetooth: pairing agent not registered", "err", err)
	}
	_ = n.Bus.SetAdapter(ctx, "Pairable", true)
	if _, known := n.device(ctx, mac); !known {
		step(StepFinding)
		if !n.find(ctx, mac) {
			return "", errors.New("Device not found — put it in pairing mode and keep it close")
		}
	}
	step(StepPairing)
	err := n.pairOnce(ctx, mac)
	if err != nil && !errors.Is(err, ErrAlreadyExists) {
		n.log().Warn("bluetooth: pair failed", "mac", mac, "err", err)
		d, _ := n.device(ctx, mac)
		return "", pairError(err, displayName(d))
	}
	step(StepConnecting)
	if err := n.Bus.SetTrusted(ctx, mac, true); err != nil {
		n.log().Warn("bluetooth: trust failed", "mac", mac, "err", err)
	}
	if err := n.connect(ctx, mac, step); err != nil {
		n.log().Warn("bluetooth: connect after pair failed", "mac", mac, "err", err)
		return "", bondedError{errors.New("Paired but failed to connect" + bluezSuffix(err))}
	}
	return n.connected(ctx, mac, role), nil
}

// bondedError is a pair that failed with the device bonded: BlueZ keeps it paired (#469).
type bondedError struct{ error }

func (e bondedError) Unwrap() error { return e.error }

// rebond is the pair of a device BlueZ already has a bond with (#469: the retry of a pair whose
// audio failed removed the bond first, then couldn't find the buds, which had left pairing
// mode). It connects with the bond it has. When the connect itself fails, the device may have
// dropped its side of the bond (a reset, or paired elsewhere): the bond is removed only once a
// discovery sees the device in pairing mode, and again tells the caller to pair it anew.
// Otherwise the bond stays and the error says the device is still paired.
func (n *Node) rebond(ctx context.Context, mac, role string, step func(string)) (name string, err error, again bool) {
	step(StepConnecting)
	if err := n.Bus.SetTrusted(ctx, mac, true); err != nil {
		n.log().Warn("bluetooth: trust failed", "mac", mac, "err", err)
	}
	err = n.connect(ctx, mac, step)
	if err == nil {
		return n.connected(ctx, mac, role), nil, false
	}
	n.log().Info("bluetooth: bonded device didn't connect", "mac", mac, "err", err)
	if errors.Is(err, errNoAudio) {
		return "", bondedError{errors.New("Paired but failed to connect")}, false
	}
	step(StepFinding)
	if !n.inPairingMode(ctx, mac) {
		d, _ := n.device(ctx, mac)
		who := displayName(d)
		if who == "" {
			who = "the device"
		}
		return "", bondedError{errors.New(capitalize(who) + " is paired but didn't connect — turn it on and keep it close, or put it in pairing mode to pair it again" + bluezSuffix(err))}, false
	}
	n.log().Info("bluetooth: bonded device is in pairing mode; pairing it again", "mac", mac)
	if err := n.Bus.Remove(ctx, mac); err != nil && !errors.Is(err, ErrUnknownDevice) {
		n.log().Warn("bluetooth: old bond not removed", "mac", mac, "err", err)
	}
	return "", nil, true
}

// inPairingMode is a BR/EDR discovery until it sees mac (a bonded device answers an inquiry
// only in pairing mode) or FindFor passes.
func (n *Node) inPairingMode(ctx context.Context, mac string) bool {
	stop, err := n.Bus.Discover(ctx, "bredr")
	if err != nil {
		return false
	}
	defer stop()
	deadline := time.Now().Add(or(n.FindFor, 12*time.Second))
	for {
		if d, ok := n.device(ctx, mac); ok && d.RSSI != 0 {
			return true
		}
		if time.Now().After(deadline) || sleep(ctx, or(n.Poll*2, time.Second)) != nil {
			return false
		}
	}
}

// connected is a pair's end: route the device's audio and save it for the reconnect loop. It
// returns the device's name.
func (n *Node) connected(ctx context.Context, mac, role string) string {
	n.routeAudio(ctx, mac, role)
	name := mac
	if d, ok := n.device(ctx, mac); ok {
		if s := displayName(d); s != "" {
			name = s
		}
	}
	if n.Store != nil {
		if err := n.Store.Paired(mac, name, role, time.Now()); err != nil {
			n.log().Warn("bluetooth: device not saved", "mac", mac, "err", err)
		}
	}
	return name
}

// pairOnce is Device1.Pair, retried once after a Page Timeout that came back fast: right after
// an inquiry (the scan, or find's) the controller's first page often fails within a second or
// two although the device is in pairing mode, and the next one pairs (#433). A device really out
// of range takes the whole page timeout (about 5 s) to fail, and gets no retry.
func (n *Node) pairOnce(ctx context.Context, mac string) error {
	for attempt := 0; ; attempt++ {
		start := time.Now()
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := n.Bus.Pair(pctx, mac)
		cancel()
		if attempt > 0 || !isPageTimeout(err) || time.Since(start) > pageFast {
			return err
		}
		n.log().Info("bluetooth: page timeout right after discovery; retrying the pair", "mac", mac, "after", time.Since(start).Round(time.Millisecond))
		if err := sleep(ctx, or(n.PageRetryAfter, 2*time.Second)); err != nil {
			return err
		}
	}
}

// pageFast is how quickly a Page Timeout must come back to be retried.
const pageFast = 4 * time.Second

func isPageTimeout(err error) bool {
	var be BlueZError
	if !errors.As(err, &be) {
		return false
	}
	text := strings.ToLower(be.Message)
	return strings.Contains(text, "page timeout") || strings.Contains(text, "page-timeout")
}

// pairError is the app's message for a failed Device1.Pair: what BlueZ's error means for the
// user, then the BlueZ error itself for support. name is the device's, or "".
func pairError(err error, name string) error {
	if name == "" {
		name = "the device"
	}
	var be BlueZError
	errors.As(err, &be)
	var msg string
	switch {
	case isPageTimeout(err):
		msg = capitalize(name) + " didn't answer — put it in pairing mode and keep it close"
	case be.Name == "org.bluez.Error.AuthenticationFailed", be.Name == "org.bluez.Error.AuthenticationRejected",
		be.Name == "org.bluez.Error.AuthenticationCanceled":
		msg = "Pairing was refused by the device"
	case be.Name == "org.bluez.Error.ConnectionAttemptFailed":
		msg = "Couldn't connect to " + name + " to pair — keep it close and try again"
	case errors.Is(err, context.DeadlineExceeded):
		msg = "Pairing timed out — make sure the device is in pairing mode"
	default:
		msg = "Failed to pair — make sure the device is in pairing mode"
	}
	return errors.New(msg + bluezSuffix(err))
}

// bluezSuffix is " (org.bluez.Error.X: text)" when err is BlueZ's, else "".
func bluezSuffix(err error) string {
	var be BlueZError
	if !errors.As(err, &be) {
		return ""
	}
	return " (" + be.Error() + ")"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// find is N:'s _scan_briefly: BR/EDR discovery until BlueZ knows mac (after a remove it
// doesn't) or FindFor passes.
func (n *Node) find(ctx context.Context, mac string) bool {
	stop, err := n.Bus.Discover(ctx, "bredr")
	if err != nil {
		return false
	}
	defer stop()
	deadline := time.Now().Add(or(n.FindFor, 12*time.Second))
	for {
		if _, ok := n.device(ctx, mac); ok {
			return true
		}
		if time.Now().After(deadline) || sleep(ctx, or(n.Poll*2, time.Second)) != nil {
			return false
		}
	}
}

// connect is N:'s connect: Connect (a connect already in progress is waited for), then for an
// audio device the wait for its audio profile. A profile BlueZ has up (a media transport) is a
// connect even when Pulse has no sink for it yet (#433: the check gave up while bluetoothd had
// the A2DP stream ready). If neither comes up the device is disconnected, which stops BlueZ
// retrying AVDTP (the "click loop", failure mode 2).
func (n *Node) connect(ctx context.Context, mac string, step func(string)) error {
	d, _ := n.device(ctx, mac)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := n.Bus.Connect(cctx, mac)
	cancel()
	if errors.Is(err, ErrInProgress) {
		err = n.waitConnected(ctx, mac)
	}
	if err != nil {
		return err
	}
	if !isAudio(d) || n.Pulse == nil {
		return nil
	}
	if step != nil {
		step(StepAudio)
	}
	pulseUp, bluezUp := n.waitAudio(ctx, mac, deviceType(d) == "audio_sink")
	switch {
	case pulseUp:
	case bluezUp:
		n.log().Warn("bluetooth: audio profile up in BlueZ, but PulseAudio has no sink for it", "mac", mac)
	default:
		_ = n.Bus.Disconnect(ctx, mac)
		return errNoAudio
	}
	return nil
}

var errNoAudio = errors.New("connected, but its audio profile did not come up")

func (n *Node) waitConnected(ctx context.Context, mac string) error {
	deadline := time.Now().Add(or(n.BusyFor, 10*time.Second))
	for time.Now().Before(deadline) {
		if err := sleep(ctx, or(n.Poll*2, time.Second)); err != nil {
			return err
		}
		if d, ok := n.device(ctx, mac); ok && d.Connected {
			return nil
		}
	}
	return ErrInProgress
}

// waitAudio is N:'s _wait_for_audio_sink: one of the device's bluez sinks or sources present
// for AudioStable within AudioFor (BlueZ shows a card on the ACL link, before the profile, and
// a sink can flap while AVDTP discovery times out). pulseUp is that; bluezUp is BlueZ's media
// transport for the device, checked when Pulse doesn't show it. Pulse's error is logged once (it
// used to be swallowed, so an unreachable Pulse looked like a profile that never came up), and
// the wait ends there. For a speaker whose card is there without a sink (its profile off), the
// card is set to a2dp_sink once.
func (n *Node) waitAudio(ctx context.Context, mac string, speaker bool) (pulseUp, bluezUp bool) {
	transport := func() bool {
		up, err := n.Bus.AudioTransport(ctx, mac)
		if err != nil {
			n.log().Debug("bluetooth: media transport not checked", "mac", mac, "err", err)
		}
		return up
	}
	deadline := time.Now().Add(or(n.AudioFor, 20*time.Second))
	var since, cardSince time.Time
	nudged := !speaker
	for time.Now().Before(deadline) {
		up, err := hasEndpoint(ctx, n.Pulse, mac)
		if err != nil {
			n.log().Warn("bluetooth: pactl failed", "mac", mac, "err", err)
			return false, transport()
		}
		if up {
			if since.IsZero() {
				since = time.Now()
			} else if time.Since(since) >= or(n.AudioStable, 2*time.Second) {
				return true, true
			}
		} else {
			since = time.Time{}
			if !nudged {
				if card, ok := btCard(ctx, n.Pulse, mac); !ok {
					cardSince = time.Time{}
				} else if cardSince.IsZero() {
					cardSince = time.Now()
				} else if time.Since(cardSince) >= or(n.AudioStable, 2*time.Second) {
					nudged = true
					if err := n.Pulse.SetCardProfile(ctx, card, "a2dp_sink"); err != nil {
						n.log().Info("bluetooth: card profile not set", "card", card, "err", err)
					} else {
						n.log().Info("bluetooth: card had no sink; profile set to a2dp_sink", "card", card)
					}
				}
			}
		}
		if sleep(ctx, or(n.Poll, 500*time.Millisecond)) != nil {
			return false, false
		}
	}
	return false, transport()
}

// --- disconnect, release, auto-connect, discoverable -------------------------------------------

func (n *Node) disconnect(ctx context.Context, mac string) {
	n.op.Lock()
	defer n.op.Unlock()
	n.unrouteAudio(ctx, mac)
	if err := n.Bus.Disconnect(ctx, mac); err != nil {
		n.log().Warn("bluetooth: disconnect failed", "mac", mac, "err", err)
		return
	}
	n.log().Info("bluetooth: disconnected", "mac", mac)
}

// release is N:'s run_bluetooth_release: disconnect and stop reconnecting it (the pairing
// stays, so the user's phone can have it), or with forget remove the pairing and the record.
func (n *Node) release(ctx context.Context, mac string, forget bool) {
	n.op.Lock()
	defer n.op.Unlock()
	n.unrouteAudio(ctx, mac)
	_ = n.Bus.Disconnect(ctx, mac)
	if forget {
		// Idempotent: a device BlueZ no longer knows is already forgotten there.
		ok := true
		if err := n.Bus.Remove(ctx, mac); err != nil && !errors.Is(err, ErrUnknownDevice) {
			n.log().Warn("bluetooth: remove failed", "mac", mac, "err", err)
			ok = false
		}
		if n.Store != nil {
			if err := n.Store.Delete(mac); err != nil {
				n.log().Warn("bluetooth: record not deleted", "mac", mac, "err", err)
				ok = false
			}
		}
		if ok {
			n.log().Info("bluetooth: device forgotten", "mac", mac)
		}
		return
	}
	if n.Store != nil {
		if _, err := n.Store.SetAutoConnect(mac, false); err != nil {
			n.log().Warn("bluetooth: auto_connect not cleared", "mac", mac, "err", err)
		}
	}
	n.log().Info("bluetooth: device released (auto-reconnect off)", "mac", mac)
}

func (n *Node) setAutoConnect(mac string, on bool) {
	if n.Store == nil {
		return
	}
	found, err := n.Store.SetAutoConnect(mac, on)
	switch {
	case err != nil:
		n.log().Error("bluetooth: auto_connect not saved", "mac", mac, "err", err)
	case !found:
		// The app's switch for a device forgotten (or paired by another node) a moment ago.
		n.log().Debug("bluetooth: auto_connect for a device this node has no record of", "mac", mac)
	default:
		n.log().Info("bluetooth: auto_connect set", "mac", mac, "enabled", on)
	}
}

// discoverable is N:'s set_discoverable, in its order (phones filter their picker on pairable
// and discoverable): power, pairable, the timeout, then discoverable.
func (n *Node) discoverable(ctx context.Context, timeout int) {
	steps := []struct {
		prop string
		v    any
	}{{"Powered", true}, {"Pairable", true}, {"DiscoverableTimeout", uint32(timeout)}, {"Discoverable", true}}
	for _, s := range steps {
		if err := n.Bus.SetAdapter(ctx, s.prop, s.v); err != nil {
			n.log().Warn("bluetooth: discoverable failed", "step", s.prop, "err", err)
			if s.prop == "Discoverable" {
				return
			}
		}
	}
	n.log().Info("bluetooth: discoverable", "timeout_seconds", timeout)
}

// --- the agent and the reconnect loop --------------------------------------------------------

// Start registers the pairing agent, makes the adapter pairable, and runs until ctx ends: the
// trust and connect after a phone-initiated pairing (N:'s agent does it on "Paired: yes"), and
// the reconnect loop for the saved devices (N:scripts/main.py _bt_reconnect), and the audio
// routing of a saved device that reconnects by itself.
func (n *Node) Start(ctx context.Context) {
	n.ensureAgent(ctx)
	go n.watchPaired(ctx)
	go n.watchConnections(ctx)
	go n.reconnectLoop(ctx)
}

// watchConnections routes a saved device's audio when it connects by itself, and hands the
// output back when it goes (#469: headphones turned off and on again reconnected to the Pi, and
// the replies stayed on the HAT until the reconnect loop, which skips connected devices). The
// route waits for the radio lock, so a pair or the reconnect loop routing the same device runs
// first, and this one finds it done (routing is idempotent).
func (n *Node) watchConnections(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-n.Bus.Connections():
			if !ok {
				return
			}
			r, saved := n.storeGet(c.MAC)
			n.mu.Lock()
			ours := n.pairing[c.MAC]
			n.mu.Unlock()
			if n.Pulse == nil || !saved || ours {
				continue
			}
			if !c.Connected {
				n.unrouteAudio(ctx, c.MAC)
				continue
			}
			go func() {
				n.op.Lock()
				defer n.op.Unlock()
				if d, ok := n.device(ctx, c.MAC); !ok || !d.Connected {
					return
				}
				n.log().Info("bluetooth: saved device connected; routing its audio", "mac", c.MAC, "name", r.Name)
				n.routeAudio(ctx, c.MAC, r.Role)
			}()
		}
	}
}

func (n *Node) ensureAgent(ctx context.Context) {
	if err := n.Bus.RegisterAgent(ctx); err != nil {
		n.log().Warn("bluetooth: pairing agent not registered", "err", err)
		return
	}
	_ = n.Bus.SetAdapter(ctx, "Pairable", true)
}

func (n *Node) watchPaired(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case mac, ok := <-n.Bus.Paired():
			if !ok {
				return
			}
			n.mu.Lock()
			ours := n.pairing[mac]
			n.mu.Unlock()
			if ours {
				continue // the pair handler trusts and connects it itself
			}
			n.log().Info("bluetooth: trust and connect after an incoming pair", "mac", mac)
			n.op.Lock()
			_ = n.Bus.SetTrusted(ctx, mac, true)
			if err := n.Bus.Connect(ctx, mac); err != nil && !errors.Is(err, ErrInProgress) {
				n.log().Warn("bluetooth: connect after an incoming pair failed", "mac", mac, "err", err)
			}
			n.op.Unlock()
		}
	}
}

func (n *Node) reconnectLoop(ctx context.Context) {
	if sleep(ctx, or(n.FirstReconnect, 30*time.Second)) != nil {
		return
	}
	for {
		n.Reconnect(ctx)
		if sleep(ctx, or(n.ReconnectEvery, 10*time.Minute)) != nil {
			return
		}
	}
}

// Reconnect connects the saved devices with auto_connect on that aren't connected now, and
// routes their audio. It also re-registers the agent, which a bluetoothd restart drops.
func (n *Node) Reconnect(ctx context.Context) int {
	if n.Store == nil {
		return 0
	}
	recs := n.Store.All()
	if len(recs) == 0 || !n.available(ctx) {
		return 0
	}
	n.ensureAgent(ctx)
	count := 0
	for _, r := range recs {
		if !r.AutoConnect {
			continue
		}
		n.op.Lock()
		d, known := n.device(ctx, r.MAC)
		if known && !d.Connected {
			if err := n.connect(ctx, r.MAC, nil); err == nil {
				n.routeAudio(ctx, r.MAC, r.Role)
				count++
				n.log().Info("bluetooth: reconnected", "name", r.Name, "mac", r.MAC)
			}
		}
		n.op.Unlock()
	}
	return count
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
