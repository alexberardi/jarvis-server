package provisioning

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeNM is a small NetworkManager behind nmBus: one Wi-Fi device, access points, saved
// profiles and active connections. It records the calls that change something.
type fakeNM struct {
	mu     sync.Mutex
	calls  []string
	props  map[dbus.ObjectPath]map[string]any // path → "Interface.Prop" (short) → value
	conns  map[dbus.ObjectPath]nmSettings
	order  []dbus.ObjectPath // profiles in ListConnections order
	added  []nmSettings      // AddAndActivateConnection[2]'s settings, in order
	opts   []map[string]dbus.Variant
	states []uint32 // the next activation's State, read by read; the last one sticks
	n      int
	addErr error
}

const fakeDev = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/3")

func newFakeNM() *fakeNM {
	f := &fakeNM{
		props: map[dbus.ObjectPath]map[string]any{
			fakeDev: {"Wireless.LastScan": int64(100), "Device.State": uint32(30), "Device.StateReason": []any{uint32(30), uint32(0)},
				"Device.ActiveConnection": dbus.ObjectPath("/"), "Wireless.ActiveAccessPoint": dbus.ObjectPath("/")},
			nmPath: {"NetworkManager.ActiveConnections": []dbus.ObjectPath{}},
		},
		conns:  map[dbus.ObjectPath]nmSettings{},
		states: []uint32{1, 2},
	}
	return f
}

// short is "Device.State" for org.freedesktop.NetworkManager.Device.State.
func short(name string) string {
	s := strings.TrimPrefix(name, nmIface+".")
	if s == nmIface {
		return "NetworkManager"
	}
	return strings.TrimPrefix(s, "Device.")
}

func (f *fakeNM) note(format string, a ...any) { f.calls = append(f.calls, fmt.Sprintf(format, a...)) }

func (f *fakeNM) Calls() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "; ")
}

func (f *fakeNM) addAP(ssid string, strength byte, flags, wpa, rsn uint32) {
	p := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/NetworkManager/AccessPoint/%d", len(f.props)))
	f.props[p] = map[string]any{"AccessPoint.Ssid": []byte(ssid), "AccessPoint.Strength": strength,
		"AccessPoint.Flags": flags, "AccessPoint.WpaFlags": wpa, "AccessPoint.RsnFlags": rsn}
	aps, _ := f.props[fakeDev]["APs"].([]dbus.ObjectPath)
	f.props[fakeDev]["APs"] = append(aps, p)
}

func (f *fakeNM) addProfile(id string) dbus.ObjectPath {
	f.n++
	p := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/NetworkManager/Settings/%d", f.n))
	f.conns[p] = nmSettings{"connection": {"id": dbus.MakeVariant(id)}}
	f.order = append(f.order, p)
	return p
}

func (f *fakeNM) activate(id string) dbus.ObjectPath {
	f.n++
	ac := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/NetworkManager/ActiveConnection/%d", f.n))
	f.props[ac] = map[string]any{"Connection.Active.Id": id}
	f.props[nmPath]["NetworkManager.ActiveConnections"] = append(f.props[nmPath]["NetworkManager.ActiveConnections"].([]dbus.ObjectPath), ac)
	f.props[fakeDev]["Device.ActiveConnection"] = ac
	return ac
}

func (f *fakeNM) Call(_ context.Context, path dbus.ObjectPath, method string, args ...any) ([]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := method[strings.LastIndex(method, ".")+1:]
	switch m {
	case "GetDeviceByIpIface":
		if args[0] != "wlan0" {
			return nil, errors.New("no such device")
		}
		return []any{fakeDev}, nil
	case "RequestScan":
		f.note("RequestScan")
		f.props[fakeDev]["Wireless.LastScan"] = f.props[fakeDev]["Wireless.LastScan"].(int64) + 1
		return nil, nil
	case "GetAllAccessPoints":
		f.note("GetAllAccessPoints")
		aps, _ := f.props[fakeDev]["APs"].([]dbus.ObjectPath)
		return []any{aps}, nil
	case "ListConnections":
		var ps []dbus.ObjectPath
		for _, p := range f.order {
			if _, ok := f.conns[p]; ok {
				ps = append(ps, p)
			}
		}
		return []any{ps}, nil
	case "GetSettings":
		s, ok := f.conns[path]
		if !ok {
			return nil, errors.New("no such profile")
		}
		return []any{map[string]map[string]dbus.Variant(s)}, nil
	case "AddAndActivateConnection", "AddAndActivateConnection2":
		if f.addErr != nil {
			return nil, f.addErr
		}
		s := args[0].(nmSettings)
		f.added = append(f.added, s)
		if len(args) > 3 {
			f.opts = append(f.opts, args[3].(map[string]dbus.Variant))
		}
		id := s["connection"]["id"].Value().(string)
		f.note("%s %s", m, id)
		p := f.addProfile(id)
		f.conns[p] = s
		return []any{p, f.activate(id)}, nil
	case "Delete":
		s, ok := f.conns[path]
		if !ok {
			return nil, errors.New("no such profile")
		}
		f.note("Delete %s", s["connection"]["id"].Value())
		delete(f.conns, path)
		return nil, nil
	case "Update":
		s := args[0].(nmSettings)
		f.note("Update %s", s["connection"]["id"].Value())
		f.conns[path] = s
		return nil, nil
	case "DeactivateConnection":
		ac := args[0].(dbus.ObjectPath)
		f.note("DeactivateConnection %s", f.props[ac]["Connection.Active.Id"])
		var keep []dbus.ObjectPath
		for _, p := range f.props[nmPath]["NetworkManager.ActiveConnections"].([]dbus.ObjectPath) {
			if p != ac {
				keep = append(keep, p)
			}
		}
		f.props[nmPath]["NetworkManager.ActiveConnections"] = keep
		return nil, nil
	}
	return nil, fmt.Errorf("fake: unexpected call %s on %s", method, path)
}

func (f *fakeNM) Get(_ context.Context, path dbus.ObjectPath, iface, prop string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if iface == nmActive && prop == "State" {
		if _, ok := f.props[path]; !ok {
			return nil, errors.New("no such object")
		}
		st := f.states[0]
		if len(f.states) > 1 {
			f.states = f.states[1:]
		}
		return st, nil
	}
	v, ok := f.props[path][short(iface)+"."+prop]
	if !ok {
		return nil, fmt.Errorf("fake: no property %s.%s on %s", iface, prop, path)
	}
	return v, nil
}

func (f *fakeNM) Close() error { return nil }

func newTestNM(f *fakeNM) *NetworkManager {
	n := newNetworkManager(f, "")
	n.poll, n.scanWait, n.connectWait, n.apWait = time.Millisecond, 50*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	return n
}

func setting(s nmSettings, group, key string) any {
	v, ok := s[group][key]
	if !ok {
		return nil
	}
	return v.Value()
}

func TestNMScanMapsSecurityAndSignal(t *testing.T) {
	f := newFakeNM()
	f.addAP("Home", 80, apPrivacy, 0, apKeyPSK)
	f.addAP("Home", 40, apPrivacy, 0, apKeyPSK) // weaker duplicate
	f.addAP("Office", 50, apPrivacy, 0, apKeySAE)
	f.addAP("Mixed", 60, apPrivacy, 0, apKeyPSK|apKeySAE)
	f.addAP("Cafe", 30, 0, 0, 0)
	f.addAP("Old", 20, apPrivacy, apKeyPSK, apKeyPSK)
	f.addAP("", 99, 0, 0, 0) // hidden
	n := newTestNM(f)
	nets, err := n.ScanAndCache(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []NetworkInfo{
		{SSID: "Home", SignalStrength: -42, Security: "WPA2"},
		{SSID: "Mixed", SignalStrength: -54, Security: "WPA2 WPA3"},
		{SSID: "Office", SignalStrength: -60, Security: "WPA3"},
		{SSID: "Cafe", SignalStrength: -72, Security: "OPEN"},
		{SSID: "Old", SignalStrength: -78, Security: "WPA1 WPA2"},
	}
	if !reflect.DeepEqual(nets, want) {
		t.Errorf("scan:\n got %+v\nwant %+v", nets, want)
	}
	if got := f.Calls(); got != "RequestScan; GetAllAccessPoints" {
		t.Errorf("calls %q", got)
	}
	// The hotspot can't scan: once cached, ScanNetworks answers from the cache.
	again, err := n.ScanNetworks(t.Context())
	if err != nil || !reflect.DeepEqual(again, want) || f.Calls() != "RequestScan; GetAllAccessPoints" {
		t.Errorf("cached scan: %v %+v, calls %q", err, again, f.Calls())
	}
}

func TestNMScanWithoutCacheScansLive(t *testing.T) {
	f := newFakeNM()
	f.addAP("Home", 100, apPrivacy, 0, apKeyPSK)
	nets, err := newTestNM(f).ScanNetworks(t.Context())
	if err != nil || len(nets) != 1 || nets[0].SignalStrength != -30 {
		t.Fatalf("%v %+v", err, nets)
	}
}

func TestNMConnectNewProfile(t *testing.T) {
	f := newFakeNM()
	n := newTestNM(f)
	if err := n.Connect(t.Context(), "Home", "hunter22"); err != nil {
		t.Fatal(err)
	}
	s := f.added[0]
	for _, c := range []struct {
		group, key string
		want       any
	}{
		{"connection", "id", "jarvis-Home"},
		{"connection", "type", "802-11-wireless"},
		{"connection", "interface-name", "wlan0"},
		{"connection", "autoconnect", false},
		{"802-11-wireless", "ssid", []byte("Home")},
		{"802-11-wireless-security", "key-mgmt", "wpa-psk"},
		{"802-11-wireless-security", "psk", "hunter22"},
		{"802-11-wireless-security", "psk-flags", uint32(0)},
	} {
		if got := setting(s, c.group, c.key); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s.%s = %#v, want %#v", c.group, c.key, got, c.want)
		}
	}
	if err := n.CommitConnection(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "AddAndActivateConnection jarvis-Home; Update jarvis-Home" {
		t.Errorf("calls %q", got)
	}
	var saved nmSettings
	for _, s := range f.conns {
		saved = s
	}
	if setting(saved, "connection", "autoconnect") != true || setting(saved, "connection", "autoconnect-priority") != int32(999) ||
		setting(saved, "802-11-wireless-security", "psk") != "hunter22" {
		t.Errorf("committed profile %v", saved)
	}
	// Nothing left to commit or roll back.
	if err := n.RollbackConnection(t.Context()); err != nil || strings.Contains(f.Calls(), "Delete") {
		t.Errorf("rollback after commit: %v, %q", err, f.Calls())
	}
}

func TestNMConnectKeyManagement(t *testing.T) {
	f := newFakeNM()
	f.addAP("Office", 50, apPrivacy, 0, apKeySAE)
	f.addAP("Mixed", 50, apPrivacy, 0, apKeyPSK|apKeySAE)
	f.addAP("Cafe", 50, 0, 0, 0)
	n := newTestNM(f)
	if _, err := n.ScanAndCache(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, ssid := range []string{"Office", "Mixed"} {
		if err := n.Connect(t.Context(), ssid, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Connect(t.Context(), "Cafe", ""); err != nil {
		t.Fatal(err)
	}
	if got := setting(f.added[0], "802-11-wireless-security", "key-mgmt"); got != "sae" {
		t.Errorf("WPA3-only: %v", got)
	}
	if got := setting(f.added[1], "802-11-wireless-security", "key-mgmt"); got != "wpa-psk" {
		t.Errorf("WPA2/WPA3: %v", got)
	}
	if _, ok := f.added[2]["802-11-wireless-security"]; ok {
		t.Errorf("open network got a security section: %v", f.added[2])
	}
}

func TestNMConnectFailsWithTheReasonAndRollsBack(t *testing.T) {
	f := newFakeNM()
	f.states = []uint32{1, 1, 4}
	f.props[fakeDev]["Device.StateReason"] = []any{uint32(120), uint32(7)}
	n := newTestNM(f)
	err := n.Connect(t.Context(), "Home", "wrong")
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("err %v", err)
	}
	if err := n.RollbackConnection(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "AddAndActivateConnection jarvis-Home; Delete jarvis-Home" {
		t.Errorf("calls %q", got)
	}
	if len(f.conns) != 0 {
		t.Errorf("profiles left: %v", f.conns)
	}
}

func TestNMConnectTimesOut(t *testing.T) {
	f := newFakeNM()
	f.states = []uint32{1} // activating forever
	n := newTestNM(f)
	err := n.Connect(t.Context(), "Home", "pw")
	if err == nil || !strings.Contains(err.Error(), "not active after") {
		t.Fatalf("err %v", err)
	}
	if err := n.RollbackConnection(t.Context()); err != nil || len(f.conns) != 0 {
		t.Errorf("rollback: %v, left %v", err, f.conns)
	}
}

func TestNMConnectAddRefused(t *testing.T) {
	f := newFakeNM()
	f.addErr = errors.New("org.freedesktop.NetworkManager.PermissionDenied")
	n := newTestNM(f)
	if err := n.Connect(t.Context(), "Home", "pw"); err == nil || !strings.Contains(err.Error(), "PermissionDenied") {
		t.Fatalf("err %v", err)
	}
	if err := n.RollbackConnection(t.Context()); err != nil || f.Calls() != "" {
		t.Errorf("rollback with nothing added: %v %q", err, f.Calls())
	}
}

func TestNMExistingProfileIsKeptOnRollback(t *testing.T) {
	f := newFakeNM()
	old := f.addProfile("jarvis-Home")
	f.addProfile("jarvis-Home-new") // a crashed attempt's leftover
	n := newTestNM(f)
	f.states = []uint32{4}
	if err := n.Connect(t.Context(), "Home", "wrong"); err == nil {
		t.Fatal("connected")
	}
	if err := n.RollbackConnection(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "Delete jarvis-Home-new; AddAndActivateConnection jarvis-Home-new; Delete jarvis-Home-new" {
		t.Errorf("calls %q", got)
	}
	if _, ok := f.conns[old]; !ok || len(f.conns) != 1 {
		t.Errorf("the profile from before is gone: %v", f.conns)
	}
}

func TestNMCommitReplacesTheExistingProfile(t *testing.T) {
	f := newFakeNM()
	old := f.addProfile("jarvis-Home")
	n := newTestNM(f)
	if err := n.Connect(t.Context(), "Home", "newpass"); err != nil {
		t.Fatal(err)
	}
	if err := n.CommitConnection(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "AddAndActivateConnection jarvis-Home-new; Delete jarvis-Home; Update jarvis-Home" {
		t.Errorf("calls %q", got)
	}
	if _, ok := f.conns[old]; ok || len(f.conns) != 1 {
		t.Errorf("profiles %v", f.conns)
	}
	for _, s := range f.conns {
		if setting(s, "connection", "autoconnect-priority") != int32(999) || setting(s, "802-11-wireless-security", "psk") != "newpass" {
			t.Errorf("committed %v", s)
		}
	}
}

func TestNMStartAPMode(t *testing.T) {
	f := newFakeNM()
	f.addProfile(nmAPProfile) // left over
	n := newTestNM(f)
	if err := n.StartAPMode(t.Context(), "jarvis-1234abcd"); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "Delete jarvis-setup-ap; AddAndActivateConnection2 jarvis-setup-ap" {
		t.Errorf("calls %q", got)
	}
	s := f.added[0]
	for _, c := range []struct {
		group, key string
		want       any
	}{
		{"connection", "interface-name", "wlan0"},
		{"connection", "autoconnect", false},
		{"802-11-wireless", "ssid", []byte("jarvis-1234abcd")},
		{"802-11-wireless", "mode", "ap"},
		{"802-11-wireless", "band", "bg"},
		{"802-11-wireless", "channel", uint32(6)},
		{"ipv4", "method", "shared"},
		{"ipv4", "address-data", []map[string]dbus.Variant{{"address": dbus.MakeVariant("192.168.4.1"), "prefix": dbus.MakeVariant(uint32(24))}}},
		{"ipv6", "method", "ignore"},
	} {
		if got := setting(s, c.group, c.key); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s.%s = %#v, want %#v", c.group, c.key, got, c.want)
		}
	}
	if _, ok := s["802-11-wireless-security"]; ok {
		t.Error("the setup hotspot is open, as N:'s hostapd one")
	}
	if got := f.opts[0]["persist"].Value(); got != "volatile" {
		t.Errorf("persist %v", got)
	}
}

func TestNMStartAPModeFails(t *testing.T) {
	f := newFakeNM()
	f.states = []uint32{4}
	f.props[fakeDev]["Device.StateReason"] = []any{uint32(120), uint32(4)}
	err := newTestNM(f).StartAPMode(t.Context(), "jarvis-x")
	if err == nil || !strings.Contains(err.Error(), "configuration failed") {
		t.Fatalf("err %v", err)
	}
}

func TestNMStopAPModeDeactivatesOnlyTheHotspot(t *testing.T) {
	f := newFakeNM()
	f.activate("Wired connection 1")
	f.activate(nmAPProfile)
	n := newTestNM(f)
	if err := n.StopAPMode(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "DeactivateConnection jarvis-setup-ap" {
		t.Errorf("calls %q", got)
	}
	// No hotspot: nothing to do.
	if err := n.StopAPMode(t.Context()); err != nil || f.Calls() != "DeactivateConnection jarvis-setup-ap" {
		t.Errorf("second stop: %v %q", err, f.Calls())
	}
}

func TestNMCurrentSSID(t *testing.T) {
	f := newFakeNM()
	n := newTestNM(f)
	if s, err := n.CurrentSSID(t.Context()); err != nil || s != "" {
		t.Errorf("disconnected: %q %v", s, err)
	}
	f.addAP("Home", 80, apPrivacy, 0, apKeyPSK)
	f.props[fakeDev]["Device.State"] = uint32(nmDeviceActivated)
	f.props[fakeDev]["Wireless.ActiveAccessPoint"] = f.props[fakeDev]["APs"].([]dbus.ObjectPath)[0]
	f.activate("jarvis-Home")
	if s, err := n.CurrentSSID(t.Context()); err != nil || s != "Home" {
		t.Errorf("joined: %q %v", s, err)
	}
	f.activate(nmAPProfile)
	if s, err := n.CurrentSSID(t.Context()); err != nil || s != "" {
		t.Errorf("hotspot: %q %v", s, err)
	}
}

func TestProfileName(t *testing.T) {
	if got := profileName("A very long network name indeed"); got != "jarvis-A very long network " {
		t.Errorf("%q", got)
	}
	if got := profileName("Café"); got != "jarvis-Café" {
		t.Errorf("%q", got)
	}
}
