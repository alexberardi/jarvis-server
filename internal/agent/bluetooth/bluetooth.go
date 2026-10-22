// Package bluetooth is the Pi node's Bluetooth (go-pi-node.md §7 item 5.8): the six
// jarvis/nodes/{id}/bluetooth-* topics jarvisd publishes (internal/modules/cc smarthome.go),
// ported from the Python node (N:scripts/mqtt_tts_listener.py _handle_bluetooth_*,
// N:services/bluetooth_scan_handler.py, N:core/platform_abstraction.py PiBluetoothProvider,
// N:services/bluetooth_pair_agent.py and the reconnect loop in N:scripts/main.py). The Python
// node drives bluetoothctl; this talks to BlueZ over D-Bus (Bus, bluez_linux.go) and to
// PulseAudio through pactl (Pulse), so both are fakes in the tests.
//
// Audio follows the Python node: TTS stays on the node's own sink (jarvis_output, the HAT) and
// music packages pick a connected bluez sink themselves (the SDK's BluetoothAudio); a phone
// paired as a source (role "sink": the Pi is its speaker) gets a module-loopback into the
// default sink, unloaded again on disconnect and release.
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
}

// Bus is BlueZ on the system bus. Addresses are MACs; methods on an address BlueZ has no
// device for return ErrUnknownDevice.
type Bus interface {
	Adapter(ctx context.Context) (Adapter, error)
	// SetAdapter sets one Adapter1 property: Powered, Pairable, Discoverable (bool) or
	// DiscoverableTimeout (uint32).
	SetAdapter(ctx context.Context, prop string, v any) error
	Devices(ctx context.Context) ([]Device, error)
	// Discover starts discovery on one transport ("bredr" or "le") until stop.
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
}

// API is the node's command-center client (*node.CC).
type API interface {
	PostBluetoothScanResult(ctx context.Context, rid string, body map[string]any) error
	PostBluetoothPairResult(ctx context.Context, rid string, body map[string]any) error
}

// Node handles the topics. Fill it in, then Start it once.
type Node struct {
	Bus   Bus
	Pulse Pulse  // nil: no audio routing (no Pulse)
	Store *Store // the devices to reconnect; nil keeps none
	Log   *slog.Logger

	// Timings (zero: the Python node's): ScanFor is each transport's discovery (10 s; N: runs
	// BR/EDR then LE, max(12/2, 10) s each); FindFor is the rescan for a device BlueZ doesn't
	// know before a pair (12 s); AudioFor and AudioStable are the wait for the audio profile
	// after a connect (a bluez sink or source present for 2 s within 15 s); BusyFor is the poll
	// for a connect already in progress (10 s); SourceFor is the wait for a phone's a2dp_source
	// before the loopback (5 s); FirstReconnect and ReconnectEvery pace the reconnect loop (30 s
	// after start, then every 10 min).
	// PageRetryAfter is the pause before the one retry of a pair that got a fast Page Timeout (2 s).
	ScanFor, FindFor, AudioFor, AudioStable, BusyFor, SourceFor, PageRetryAfter time.Duration
	FirstReconnect, ReconnectEvery                                              time.Duration
	Poll                                                                        time.Duration // 500 ms

	op      sync.Mutex // one radio operation at a time
	mu      sync.Mutex
	pairing map[string]bool // addresses the pair handler is working on
	seen    map[string]bool // request ids taken
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

func (n *Node) scanAndPost(ctx context.Context, api API, rid string) {
	devices, err := n.Scan(ctx)
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
// what's in range now), discover on BR/EDR then LE, and list the devices with a real name.
// Unlike N:, paired and connected are the devices' own.
func (n *Node) Scan(ctx context.Context) ([]ScanDevice, error) {
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
	for _, transport := range []string{"bredr", "le"} {
		stop, err := n.Bus.Discover(ctx, transport)
		if err != nil {
			n.log().Warn("bluetooth: discovery failed", "transport", transport, "err", err)
			continue
		}
		err = sleep(ctx, or(n.ScanFor, 10*time.Second))
		stop()
		if err != nil {
			return nil, err
		}
	}
	all, err := n.Bus.Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := []ScanDevice{}
	seen := map[string]bool{}
	for _, d := range all {
		name := displayName(d)
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

// PairBody is the pair result jarvisd takes (POST …/bluetooth/pair/{rid}/results).
func PairBody(name string, err error) map[string]any {
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	return map[string]any{"success": true, "device_name": name}
}

func (n *Node) pairAndPost(ctx context.Context, api API, rid, mac, role string) {
	name, err := n.Pair(ctx, mac, role)
	if err != nil {
		n.log().Warn("bluetooth: pair failed", "request_id", rid, "mac", mac, "err", err)
	} else {
		n.log().Info("bluetooth: pair complete", "request_id", rid, "device", name)
	}
	if err := api.PostBluetoothPairResult(ctx, rid, PairBody(name, err)); err != nil {
		n.log().Error("bluetooth: pair result not uploaded", "request_id", rid, "err", err)
	}
}

// Pair is N:'s run_bluetooth_pair_and_upload: an explicit pair, so an existing bond is removed
// first (re-pairing a bonded device fails AuthenticationFailed), then pair (after a short
// rescan when BlueZ doesn't know the device), trust, connect, route its audio and remember it
// for the reconnect loop. It never tries a plain connect first: that ends the device's pairing
// mode (bluetooth-pairing-state-2026-05-07.md, failure mode 4). It returns the device's name.
func (n *Node) Pair(ctx context.Context, mac, role string) (string, error) {
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
		_ = n.Bus.Remove(ctx, mac)
	}
	// The agent answers BlueZ's questions during the pair; make sure it is there.
	if err := n.Bus.RegisterAgent(ctx); err != nil {
		n.log().Warn("bluetooth: pairing agent not registered", "err", err)
	}
	_ = n.Bus.SetAdapter(ctx, "Pairable", true)
	if _, known := n.device(ctx, mac); !known && !n.find(ctx, mac) {
		return "", errors.New("Device not found — put it in pairing mode and keep it close")
	}
	err := n.pairOnce(ctx, mac)
	if err != nil && !errors.Is(err, ErrAlreadyExists) {
		n.log().Warn("bluetooth: pair failed", "mac", mac, "err", err)
		d, _ := n.device(ctx, mac)
		return "", pairError(err, displayName(d))
	}
	if err := n.Bus.SetTrusted(ctx, mac, true); err != nil {
		n.log().Warn("bluetooth: trust failed", "mac", mac, "err", err)
	}
	if err := n.connect(ctx, mac); err != nil {
		n.log().Warn("bluetooth: connect after pair failed", "mac", mac, "err", err)
		return "", errors.New("Paired but failed to connect" + bluezSuffix(err))
	}
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
	return name, nil
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
// audio device the wait for its audio profile; if that never comes up the device is
// disconnected, which stops BlueZ retrying AVDTP (the "click loop", failure mode 2).
func (n *Node) connect(ctx context.Context, mac string) error {
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
	if isAudio(d) && n.Pulse != nil && !n.waitAudio(ctx, mac) {
		_ = n.Bus.Disconnect(ctx, mac)
		return errors.New("connected, but its audio profile did not come up")
	}
	return nil
}

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
// a sink can flap while AVDTP discovery times out).
func (n *Node) waitAudio(ctx context.Context, mac string) bool {
	deadline := time.Now().Add(or(n.AudioFor, 15*time.Second))
	var since time.Time
	for time.Now().Before(deadline) {
		if hasEndpoint(ctx, n.Pulse, mac) {
			if since.IsZero() {
				since = time.Now()
			} else if time.Since(since) >= or(n.AudioStable, 2*time.Second) {
				return true
			}
		} else {
			since = time.Time{}
		}
		if sleep(ctx, or(n.Poll, 500*time.Millisecond)) != nil {
			return false
		}
	}
	return false
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
// the reconnect loop for the saved devices (N:scripts/main.py _bt_reconnect).
func (n *Node) Start(ctx context.Context) {
	n.ensureAgent(ctx)
	go n.watchPaired(ctx)
	go n.reconnectLoop(ctx)
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
			if err := n.connect(ctx, r.MAC); err == nil {
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
