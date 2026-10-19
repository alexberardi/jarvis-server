package provisioning

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// NetworkManager is the WiFi backend on a Pi (and desktop Linux): NetworkManager over the system
// D-Bus (go-pi-node.md §8 Q7), in place of N:wifi_manager.py's nmcli calls. The hotspot is NM's
// own AP mode (mode=ap, ipv4 shared); hostapd stays a fallback for chips where that fails.
type NetworkManager struct {
	bus   nmBus
	iface string // the Wi-Fi interface, wlan0

	poll        time.Duration // between property reads while waiting
	scanWait    time.Duration // for a requested scan to finish
	connectWait time.Duration // for a join to activate (nmcli up's 45 s in N:)
	apWait      time.Duration // for the hotspot to come up

	mu      sync.Mutex
	cache   []NetworkInfo
	cached  bool
	attempt *nmAttempt
}

// nmAttempt is what the last Connect created, for CommitConnection or RollbackConnection.
type nmAttempt struct {
	path     dbus.ObjectPath // the profile it added
	target   string          // the profile's final name, jarvis-<ssid>
	replaces dbus.ObjectPath // the jarvis-<ssid> profile that existed before, if any
	settings nmSettings      // as added (with the PSK), for the commit's Update
}

// nmBus is the slice of the system bus the backend uses: method calls and property reads on
// org.freedesktop.NetworkManager's objects. The godbus one is in nmbus_linux.go; tests fake it.
type nmBus interface {
	// Call invokes method ("interface.Member") on path and returns the reply's body.
	Call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) ([]any, error)
	// Get reads the property iface.prop of path.
	Get(ctx context.Context, path dbus.ObjectPath, iface, prop string) (any, error)
	Close() error
}

// nmSettings is a connection profile, NM's a{sa{sv}}.
type nmSettings map[string]map[string]dbus.Variant

const (
	nmPath         = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmSettingsPath = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	nmIface        = "org.freedesktop.NetworkManager"
	nmDevice       = nmIface + ".Device"
	nmWireless     = nmIface + ".Device.Wireless"
	nmAccessPoint  = nmIface + ".AccessPoint"
	nmActive       = nmIface + ".Connection.Active"
	nmSettingsI    = nmIface + ".Settings"
	nmConnection   = nmIface + ".Settings.Connection"

	// Profile names as N: makes them (_profile_name_for, _STAGED_SUFFIX).
	nmProfilePrefix = "jarvis-"
	nmStagedSuffix  = "-new"
	// nmAPProfile is the hotspot's (volatile) profile.
	nmAPProfile = "jarvis-setup-ap"
	// The hotspot's address and channel, as N:'s hostapd backend has them: the app and the
	// captive DNS (install-pi.sh's dnsmasq-shared.d file) expect 192.168.4.1.
	nmAPAddress = "192.168.4.1"
	nmAPPrefix  = 24
	nmAPChannel = 6
	// nmCommitPriority wins autoconnect: the recovery watcher relies on NM picking the
	// provisioned network when the hotspot stops (N: commit_connection, install.sh).
	nmCommitPriority = 999

	// NMActiveConnectionState values.
	nmActivated    = 2
	nmDeactivating = 3
	nmDeactivated  = 4
	// NMDeviceState activated.
	nmDeviceActivated = 100
)

// NewNetworkManager connects to the system bus for the Wi-Fi interface iface ("" is wlan0).
func NewNetworkManager(iface string) (*NetworkManager, error) {
	bus, err := dialNMBus()
	if err != nil {
		return nil, fmt.Errorf("networkmanager: %w", err)
	}
	return newNetworkManager(bus, iface), nil
}

func newNetworkManager(bus nmBus, iface string) *NetworkManager {
	if iface == "" {
		iface = "wlan0"
	}
	return &NetworkManager{
		bus: bus, iface: iface,
		poll: 250 * time.Millisecond, scanWait: 15 * time.Second,
		connectWait: 45 * time.Second, apWait: 30 * time.Second,
	}
}

// Close drops the bus connection.
func (n *NetworkManager) Close() error { return n.bus.Close() }

// ScanNetworks answers from the cache once ScanAndCache filled it (the radio can't scan while
// it is the hotspot), else scans now.
func (n *NetworkManager) ScanNetworks(ctx context.Context) ([]NetworkInfo, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cached {
		return slices.Clone(n.cache), nil
	}
	return n.scan(ctx)
}

// ScanAndCache scans and keeps the result for ScanNetworks: call it before StartAPMode.
func (n *NetworkManager) ScanAndCache(ctx context.Context) ([]NetworkInfo, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	nets, err := n.scan(ctx)
	if err != nil {
		return nil, err
	}
	n.cache, n.cached = nets, true
	return slices.Clone(nets), nil
}

func (n *NetworkManager) scan(ctx context.Context) ([]NetworkInfo, error) {
	dev, err := n.device(ctx)
	if err != nil {
		return nil, err
	}
	before, _ := n.bus.Get(ctx, dev, nmWireless, "LastScan")
	// NM refuses a scan right after another one; the list it has is fresh enough then.
	if _, err := n.bus.Call(ctx, dev, nmWireless+".RequestScan", map[string]dbus.Variant{}); err == nil {
		_ = n.waitFor(ctx, n.scanWait, func() (bool, error) {
			v, err := n.bus.Get(ctx, dev, nmWireless, "LastScan")
			return err == nil && v != before, nil
		})
	}
	body, err := n.bus.Call(ctx, dev, nmWireless+".GetAllAccessPoints")
	if err != nil {
		return nil, fmt.Errorf("networkmanager: listing access points: %w", err)
	}
	aps, _ := first[[]dbus.ObjectPath](body)
	best := map[string]NetworkInfo{}
	for _, ap := range aps {
		ssid, _ := n.bus.Get(ctx, ap, nmAccessPoint, "Ssid")
		name := string(asBytes(ssid))
		if name == "" { // hidden, or gone since the list
			continue
		}
		strength, _ := n.bus.Get(ctx, ap, nmAccessPoint, "Strength")
		flags, _ := n.bus.Get(ctx, ap, nmAccessPoint, "Flags")
		wpa, _ := n.bus.Get(ctx, ap, nmAccessPoint, "WpaFlags")
		rsn, _ := n.bus.Get(ctx, ap, nmAccessPoint, "RsnFlags")
		pct, _ := strength.(byte)
		info := NetworkInfo{
			SSID: name,
			// N:'s conversion of nmcli's percentage: 100% ≈ -30 dBm, 0% ≈ -90 dBm.
			SignalStrength: -90 + int(pct)*6/10,
			Security:       apSecurity(asUint32(flags), asUint32(wpa), asUint32(rsn)),
		}
		if old, ok := best[name]; !ok || info.SignalStrength > old.SignalStrength {
			best[name] = info
		}
	}
	nets := make([]NetworkInfo, 0, len(best))
	for _, v := range best {
		nets = append(nets, v)
	}
	slices.SortFunc(nets, func(a, b NetworkInfo) int {
		return cmp.Or(cmp.Compare(b.SignalStrength, a.SignalStrength), cmp.Compare(a.SSID, b.SSID))
	})
	return nets, nil
}

// NM80211ApFlags / NM80211ApSecurityFlags bits apSecurity reads.
const (
	apPrivacy      = 0x1
	apKeyPSK       = 0x100
	apKey8021X     = 0x200
	apKeySAE       = 0x400
	apKeyOWE       = 0x800
	apKeyOWETM     = 0x1000
	apKeyEAPSuiteB = 0x2000
)

// apSecurity is nmcli's SECURITY column (the string N: passed on), with "OPEN" for none.
func apSecurity(flags, wpa, rsn uint32) string {
	var s []string
	if flags&apPrivacy != 0 && wpa == 0 && rsn == 0 {
		s = append(s, "WEP")
	}
	if wpa != 0 {
		s = append(s, "WPA1")
	}
	if rsn&(apKeyPSK|apKey8021X) != 0 {
		s = append(s, "WPA2")
	}
	if rsn&(apKeySAE|apKeyEAPSuiteB) != 0 {
		s = append(s, "WPA3")
	}
	if rsn&(apKeyOWE|apKeyOWETM) != 0 {
		s = append(s, "OWE")
	}
	if (wpa|rsn)&apKey8021X != 0 {
		s = append(s, "802.1X")
	}
	if len(s) == 0 {
		return "OPEN"
	}
	return strings.Join(s, " ")
}

// keyMgmtFor is N:'s _key_mgmt_for: sae for a WPA3-only network in the cached scan, else
// wpa-psk (mixed WPA2/WPA3 networks take it).
func (n *NetworkManager) keyMgmtFor(ssid string) string {
	for _, net := range n.cache {
		if net.SSID != ssid {
			continue
		}
		sec := strings.ToUpper(net.Security)
		if strings.Contains(sec, "WPA3") && !strings.Contains(sec, "WPA2") && !strings.Contains(sec, "WPA1") {
			return "sae"
		}
	}
	return "wpa-psk"
}

// profileName is N:'s _profile_name_for: jarvis- and the SSID's first 20 characters.
func profileName(ssid string) string {
	r := []rune(ssid)
	return nmProfilePrefix + string(r[:min(len(r), 20)])
}

// Connect joins ssid through a new jarvis-<ssid> profile with autoconnect off (so NM can't
// race it), or a jarvis-<ssid>-new one beside an existing profile, which is kept until
// CommitConnection. The PSK travels in the D-Bus settings map: no argv, no journal line, so
// N:'s passwd-file is not needed. NM stores it system-owned in its root-only keyfile.
func (n *NetworkManager) Connect(ctx context.Context, ssid, password string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.attempt = nil
	dev, err := n.device(ctx)
	if err != nil {
		return err
	}
	byID, err := n.profiles(ctx)
	if err != nil {
		return err
	}
	a := &nmAttempt{target: profileName(ssid)}
	name := a.target
	if old := byID[a.target]; len(old) > 0 {
		name, a.replaces = a.target+nmStagedSuffix, old[0]
		// A staged profile can only be a failed attempt's leftover, never the user's.
		for _, p := range byID[name] {
			_, _ = n.bus.Call(ctx, p, nmConnection+".Delete")
		}
	}
	a.settings = nmSettings{
		"connection": {
			"id": dbus.MakeVariant(name), "uuid": dbus.MakeVariant(newUUID()),
			"type": dbus.MakeVariant("802-11-wireless"), "interface-name": dbus.MakeVariant(n.iface),
			"autoconnect": dbus.MakeVariant(false),
		},
		"802-11-wireless": {"ssid": dbus.MakeVariant([]byte(ssid)), "mode": dbus.MakeVariant("infrastructure")},
		"ipv4":            {"method": dbus.MakeVariant("auto")},
		"ipv6":            {"method": dbus.MakeVariant("auto")},
	}
	if password != "" {
		a.settings["802-11-wireless"]["security"] = dbus.MakeVariant("802-11-wireless-security")
		a.settings["802-11-wireless-security"] = map[string]dbus.Variant{
			"key-mgmt": dbus.MakeVariant(n.keyMgmtFor(ssid)), "psk": dbus.MakeVariant(password),
			"psk-flags": dbus.MakeVariant(uint32(0)),
		}
	}
	body, err := n.bus.Call(ctx, nmPath, nmIface+".AddAndActivateConnection", a.settings, dev, dbus.ObjectPath("/"))
	if err != nil {
		return fmt.Errorf("networkmanager: adding %s: %w", name, err)
	}
	a.path, _ = first[dbus.ObjectPath](body)
	n.attempt = a
	var active dbus.ObjectPath
	if len(body) > 1 {
		active, _ = body[1].(dbus.ObjectPath)
	}
	if err := n.waitActive(ctx, dev, active, n.connectWait); err != nil {
		return fmt.Errorf("networkmanager: joining %s: %w", ssid, err)
	}
	return nil
}

// CommitConnection turns the last Connect's profile into the permanent jarvis-<ssid> one:
// autoconnect on at priority 999, replacing the profile it was staged beside.
func (n *NetworkManager) CommitConnection(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	a := n.attempt
	n.attempt = nil
	if a == nil {
		return nil
	}
	var errs []error
	if a.replaces != "" {
		if _, err := n.bus.Call(ctx, a.replaces, nmConnection+".Delete"); err != nil {
			errs = append(errs, fmt.Errorf("deleting the old %s: %w", a.target, err))
		}
	}
	c := a.settings["connection"]
	c["id"] = dbus.MakeVariant(a.target)
	c["autoconnect"] = dbus.MakeVariant(true)
	c["autoconnect-priority"] = dbus.MakeVariant(int32(nmCommitPriority))
	// Update replaces the whole profile; the map still holds the PSK, so the secret is kept.
	if _, err := n.bus.Call(ctx, a.path, nmConnection+".Update", a.settings); err != nil {
		errs = append(errs, fmt.Errorf("updating %s: %w", a.target, err))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("networkmanager: committing: %w", err)
	}
	return nil
}

// RollbackConnection deletes the profile the last Connect created; one that existed before
// stays.
func (n *NetworkManager) RollbackConnection(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	a := n.attempt
	n.attempt = nil
	if a == nil || a.path == "" {
		return nil
	}
	if _, err := n.bus.Call(ctx, a.path, nmConnection+".Delete"); err != nil {
		return fmt.Errorf("networkmanager: deleting %s: %w", a.path, err)
	}
	return nil
}

// StartAPMode brings up the open setup hotspot ssid on 192.168.4.1 (NM's shared mode: DHCP and
// DNS through NM's dnsmasq), as a volatile profile NM drops once it goes down.
func (n *NetworkManager) StartAPMode(ctx context.Context, ssid string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	dev, err := n.device(ctx)
	if err != nil {
		return err
	}
	byID, err := n.profiles(ctx)
	if err != nil {
		return err
	}
	for _, p := range byID[nmAPProfile] {
		_, _ = n.bus.Call(ctx, p, nmConnection+".Delete")
	}
	s := nmSettings{
		"connection": {
			"id": dbus.MakeVariant(nmAPProfile), "uuid": dbus.MakeVariant(newUUID()),
			"type": dbus.MakeVariant("802-11-wireless"), "interface-name": dbus.MakeVariant(n.iface),
			"autoconnect": dbus.MakeVariant(false),
		},
		"802-11-wireless": {
			"ssid": dbus.MakeVariant([]byte(ssid)), "mode": dbus.MakeVariant("ap"),
			"band": dbus.MakeVariant("bg"), "channel": dbus.MakeVariant(uint32(nmAPChannel)),
		},
		"ipv4": {
			"method": dbus.MakeVariant("shared"),
			"address-data": dbus.MakeVariant([]map[string]dbus.Variant{{
				"address": dbus.MakeVariant(nmAPAddress), "prefix": dbus.MakeVariant(uint32(nmAPPrefix)),
			}}),
		},
		"ipv6": {"method": dbus.MakeVariant("ignore")},
	}
	opts := map[string]dbus.Variant{"persist": dbus.MakeVariant("volatile")}
	body, err := n.bus.Call(ctx, nmPath, nmIface+".AddAndActivateConnection2", s, dev, dbus.ObjectPath("/"), opts)
	if err != nil {
		return fmt.Errorf("networkmanager: starting the hotspot %s: %w", ssid, err)
	}
	var active dbus.ObjectPath
	if len(body) > 1 {
		active, _ = body[1].(dbus.ObjectPath)
	}
	if err := n.waitActive(ctx, dev, active, n.apWait); err != nil {
		return fmt.Errorf("networkmanager: starting the hotspot %s: %w", ssid, err)
	}
	return nil
}

// StopAPMode takes the hotspot down; NM then autoconnects to the best saved profile (the
// committed one, at priority 999). No hotspot is not an error.
func (n *NetworkManager) StopAPMode(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	v, err := n.bus.Get(ctx, nmPath, nmIface, "ActiveConnections")
	if err != nil {
		return fmt.Errorf("networkmanager: active connections: %w", err)
	}
	actives, _ := v.([]dbus.ObjectPath)
	for _, ac := range actives {
		if id, _ := n.bus.Get(ctx, ac, nmActive, "Id"); id != nmAPProfile {
			continue
		}
		if _, err := n.bus.Call(ctx, nmPath, nmIface+".DeactivateConnection", ac); err != nil {
			return fmt.Errorf("networkmanager: stopping the hotspot: %w", err)
		}
	}
	return nil
}

// CurrentSSID is the network the interface has joined as a client, "" when none (or when it
// is the hotspot).
func (n *NetworkManager) CurrentSSID(ctx context.Context) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dev, err := n.device(ctx)
	if err != nil {
		return "", err
	}
	if st, err := n.bus.Get(ctx, dev, nmDevice, "State"); err != nil || asUint32(st) != nmDeviceActivated {
		return "", err
	}
	if v, _ := n.bus.Get(ctx, dev, nmDevice, "ActiveConnection"); v != nil {
		if ac, ok := v.(dbus.ObjectPath); ok && ac != "/" {
			if id, _ := n.bus.Get(ctx, ac, nmActive, "Id"); id == nmAPProfile {
				return "", nil
			}
		}
	}
	ap, err := n.bus.Get(ctx, dev, nmWireless, "ActiveAccessPoint")
	if err != nil {
		return "", err
	}
	p, _ := ap.(dbus.ObjectPath)
	if p == "" || p == "/" {
		return "", nil
	}
	ssid, err := n.bus.Get(ctx, p, nmAccessPoint, "Ssid")
	if err != nil {
		return "", err
	}
	return string(asBytes(ssid)), nil
}

// device is the Wi-Fi interface's device object.
func (n *NetworkManager) device(ctx context.Context) (dbus.ObjectPath, error) {
	body, err := n.bus.Call(ctx, nmPath, nmIface+".GetDeviceByIpIface", n.iface)
	if err != nil {
		return "", fmt.Errorf("networkmanager: no device %s: %w", n.iface, err)
	}
	p, ok := first[dbus.ObjectPath](body)
	if !ok {
		return "", fmt.Errorf("networkmanager: no device %s", n.iface)
	}
	return p, nil
}

// profiles lists the saved profiles by connection.id.
func (n *NetworkManager) profiles(ctx context.Context) (map[string][]dbus.ObjectPath, error) {
	body, err := n.bus.Call(ctx, nmSettingsPath, nmSettingsI+".ListConnections")
	if err != nil {
		return nil, fmt.Errorf("networkmanager: listing profiles: %w", err)
	}
	paths, _ := first[[]dbus.ObjectPath](body)
	byID := map[string][]dbus.ObjectPath{}
	for _, p := range paths {
		b, err := n.bus.Call(ctx, p, nmConnection+".GetSettings")
		if err != nil {
			continue // removed meanwhile
		}
		s, _ := first[map[string]map[string]dbus.Variant](b)
		if id, ok := s["connection"]["id"].Value().(string); ok {
			byID[id] = append(byID[id], p)
		}
	}
	return byID, nil
}

// waitActive waits for the active connection to reach ACTIVATED; its deactivation (or its
// object vanishing) is a failure with the device's reason.
func (n *NetworkManager) waitActive(ctx context.Context, dev, active dbus.ObjectPath, timeout time.Duration) error {
	var failed bool
	err := n.waitFor(ctx, timeout, func() (bool, error) {
		v, err := n.bus.Get(ctx, active, nmActive, "State")
		switch st := asUint32(v); {
		case err != nil || st == nmDeactivating || st == nmDeactivated:
			failed = true
			return true, nil
		default:
			return st == nmActivated, nil
		}
	})
	if failed {
		return fmt.Errorf("activation failed: %s", n.reason(ctx, dev))
	}
	if errors.Is(err, errWaitTimeout) {
		return fmt.Errorf("not active after %s", timeout)
	}
	return err
}

// reason is the device's last state change reason, readable.
func (n *NetworkManager) reason(ctx context.Context, dev dbus.ObjectPath) string {
	v, err := n.bus.Get(ctx, dev, nmDevice, "StateReason")
	if err != nil {
		return "unknown reason"
	}
	var code uint32
	switch r := v.(type) {
	case []any:
		if len(r) == 2 {
			code = asUint32(r[1])
		}
	case []uint32:
		if len(r) == 2 {
			code = r[1]
		}
	}
	if s, ok := deviceReasons[code]; ok {
		return s
	}
	return fmt.Sprintf("reason %d", code)
}

// deviceReasons names the NMDeviceStateReason values a failed join usually has.
var deviceReasons = map[uint32]string{
	4:  "configuration failed",
	5:  "no IP address (DHCP)",
	7:  "secrets required (wrong password?)",
	8:  "supplicant disconnected (wrong password?)",
	9:  "supplicant configuration failed",
	10: "supplicant failed",
	11: "supplicant timed out",
	17: "DHCP failed",
	36: "removed",
	53: "network not found",
}

var errWaitTimeout = errors.New("timed out")

// waitFor polls done every n.poll until it says true, it errs, ctx ends or timeout passes.
func (n *NetworkManager) waitFor(ctx context.Context, timeout time.Duration, done func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(n.poll)
	defer t.Stop()
	for {
		ok, err := done()
		if ok || err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return errWaitTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func first[T any](body []any) (T, bool) {
	var zero T
	if len(body) == 0 {
		return zero, false
	}
	v, ok := body[0].(T)
	return v, ok
}

func asUint32(v any) uint32 {
	switch x := v.(type) {
	case uint32:
		return x
	case byte:
		return uint32(x)
	}
	return 0
}

func asBytes(v any) []byte {
	b, _ := v.([]byte)
	return b
}

// newUUID is a random (v4) UUID for a new profile's connection.uuid.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
