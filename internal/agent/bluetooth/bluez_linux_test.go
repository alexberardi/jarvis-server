//go:build linux

package bluetooth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// These run BlueZ (the D-Bus client and the agent) against a fake org.bluez on a private
// dbus-daemon, never the system bus: the names, paths, signatures and errors bluetoothd uses
// (doc/org.bluez.Adapter.rst, Device.rst, AgentManager.rst, Agent.rst in BlueZ 5.x).

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>%s</listen>
  <auth>%s</auth>
  %s
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// privateBus starts a dbus-daemon and returns its address: on a unix socket, or on loopback TCP
// (ANONYMOUS) where a sandbox doesn't allow unix sockets.
func privateBus(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jbt") // a short path: the socket goes in it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if addr, ok := startDaemon(t, dir, "unix:dir="+dir, "EXTERNAL", ""); ok {
		return addr
	}
	addr, ok := startDaemon(t, dir, "tcp:host=127.0.0.1,bind=127.0.0.1,port=0", "ANONYMOUS", "<allow_anonymous/>")
	if !ok {
		t.Fatal("dbus-daemon started on neither a unix socket nor TCP")
	}
	return addr
}

func startDaemon(t *testing.T, dir, listen, auth, extra string) (string, bool) {
	t.Helper()
	cfg := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(cfg, fmt.Appendf(nil, busConfig, listen, auth, extra), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+cfg, "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("no dbus-daemon: %v", err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", false
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return strings.TrimSpace(line), true
}

func dial(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	var opts []dbus.ConnOption
	if strings.HasPrefix(addr, "tcp:") {
		opts = append(opts, dbus.WithAuth(dbus.AuthAnonymous()))
	}
	c, err := dbus.Connect(addr, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

const (
	hci0    = dbus.ObjectPath("/org/bluez/hci0")
	devPath = dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_01")
)

// fakeBlueZ is bluetoothd's objects: hci0 and one speaker.
type fakeBlueZ struct {
	conn  *dbus.Conn
	mu    sync.Mutex
	props map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	calls []string
	agent struct {
		owner string
		path  dbus.ObjectPath
		caps  string
	}
}

func (f *fakeBlueZ) log(s string, a ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(s, a...))
}

func (f *fakeBlueZ) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type objManager struct{ f *fakeBlueZ }

func (o objManager) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	out := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
	for p, ifs := range o.f.props {
		out[p] = map[string]map[string]dbus.Variant{}
		for i, m := range ifs {
			out[p][i] = map[string]dbus.Variant{}
			for k, v := range m {
				out[p][i][k] = v
			}
		}
	}
	return out, nil
}

type properties struct {
	f    *fakeBlueZ
	path dbus.ObjectPath
}

func (p properties) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	return p.f.props[p.path][iface][name], nil
}

func (p properties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	return p.f.props[p.path][iface], nil
}

func (p properties) Set(iface, name string, v dbus.Variant) *dbus.Error {
	p.f.log("Set %s %s %s %v", p.path, iface, name, v)
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	p.f.props[p.path][iface][name] = v
	return nil
}

type adapter1 struct{ f *fakeBlueZ }

func (a adapter1) SetDiscoveryFilter(filter map[string]dbus.Variant) *dbus.Error {
	a.f.log("SetDiscoveryFilter %v", filter)
	return nil
}
func (a adapter1) StartDiscovery() *dbus.Error { a.f.log("StartDiscovery"); return nil }
func (a adapter1) StopDiscovery() *dbus.Error  { a.f.log("StopDiscovery"); return nil }
func (a adapter1) RemoveDevice(dev dbus.ObjectPath) *dbus.Error {
	a.f.log("RemoveDevice %s", dev)
	return nil
}

type device1 struct{ f *fakeBlueZ }

func (d device1) Pair() *dbus.Error {
	d.f.log("Pair")
	d.f.mu.Lock()
	props := d.f.props[devPath][ifDevice]
	already := props["Paired"].Value() == true
	props["Paired"] = dbus.MakeVariant(true)
	d.f.mu.Unlock()
	if already {
		return &dbus.Error{Name: "org.bluez.Error.AlreadyExists", Body: []any{"Already Exists"}}
	}
	_ = d.f.conn.Emit(devPath, ifProps+".PropertiesChanged", ifDevice, map[string]dbus.Variant{"Paired": dbus.MakeVariant(true)}, []string{})
	return nil
}

func (d device1) Connect() *dbus.Error {
	d.f.log("Connect")
	return &dbus.Error{Name: "org.bluez.Error.InProgress", Body: []any{"In Progress"}}
}

func (d device1) Disconnect() *dbus.Error {
	d.f.log("Disconnect")
	return &dbus.Error{Name: "org.bluez.Error.Failed", Body: []any{"br-connection-unknown"}}
}

type agentManager struct{ f *fakeBlueZ }

func (m agentManager) RegisterAgent(sender dbus.Sender, p dbus.ObjectPath, caps string) *dbus.Error {
	m.f.log("RegisterAgent %s %s", p, caps)
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	if m.f.agent.owner == string(sender) && m.f.agent.path == p {
		return &dbus.Error{Name: "org.bluez.Error.AlreadyExists", Body: []any{"Already Exists"}}
	}
	m.f.agent.owner, m.f.agent.path, m.f.agent.caps = string(sender), p, caps
	return nil
}

func (m agentManager) RequestDefaultAgent(p dbus.ObjectPath) *dbus.Error {
	m.f.log("RequestDefaultAgent %s", p)
	return nil
}

func startFakeBlueZ(t *testing.T, addr string) *fakeBlueZ {
	t.Helper()
	c := dial(t, addr)
	f := &fakeBlueZ{conn: c, props: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		hci0: {ifAdapter: {"Address": dbus.MakeVariant("B8:27:EB:00:00:01"), "Powered": dbus.MakeVariant(true),
			"Discoverable": dbus.MakeVariant(false), "DiscoverableTimeout": dbus.MakeVariant(uint32(180)), "Pairable": dbus.MakeVariant(false)}},
		devPath: {ifDevice: {"Address": dbus.MakeVariant("AA:BB:CC:DD:EE:01"), "Name": dbus.MakeVariant("JBL Flip 5"),
			"Alias": dbus.MakeVariant("JBL Flip 5"), "Icon": dbus.MakeVariant("audio-card"), "Paired": dbus.MakeVariant(false),
			"Connected": dbus.MakeVariant(false), "Trusted": dbus.MakeVariant(false),
			"UUIDs": dbus.MakeVariant([]string{uuidAudioSink, "0000110e-0000-1000-8000-00805f9b34fb"})}},
		"/org/bluez/hci0/dev_0C_0C_0C_0C_0C_0C": {ifDevice: {"Address": dbus.MakeVariant("0C:0C:0C:0C:0C:0C"),
			"Alias": dbus.MakeVariant("0C-0C-0C-0C-0C-0C"), "Paired": dbus.MakeVariant(false), "Connected": dbus.MakeVariant(false),
			"RSSI": dbus.MakeVariant(int16(-61))}},
	}}
	for _, e := range []struct {
		v     any
		path  dbus.ObjectPath
		iface string
	}{
		{objManager{f}, "/", "org.freedesktop.DBus.ObjectManager"},
		{agentManager{f}, "/org/bluez", ifAgentMgr},
		{adapter1{f}, hci0, ifAdapter},
		{properties{f, hci0}, hci0, ifProps},
		{device1{f}, devPath, ifDevice},
		{properties{f, devPath}, devPath, ifProps},
	} {
		if err := c.Export(e.v, e.path, e.iface); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := c.RequestName(bluezName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("org.bluez: %v %v", r, err)
	}
	return f
}

func TestBlueZOverDBus(t *testing.T) {
	addr := privateBus(t)
	f := startFakeBlueZ(t, addr)
	b, err := newBlueZ(dial(t, addr), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if a, err := b.Adapter(ctx); err != nil || !a.Powered {
		t.Fatalf("adapter %+v %v", a, err)
	}
	devs, err := b.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Device{
		{Address: "0C:0C:0C:0C:0C:0C", Alias: "0C-0C-0C-0C-0C-0C", RSSI: -61},
		{Address: speaker, Name: "JBL Flip 5", Alias: "JBL Flip 5", Icon: "audio-card", UUIDs: []string{uuidAudioSink, "0000110e-0000-1000-8000-00805f9b34fb"}},
	}
	if fmt.Sprint(devs) != fmt.Sprint(want) {
		t.Fatalf("devices\n got %+v\nwant %+v", devs, want)
	}

	// Discoverable: the property types bluetoothd checks (DiscoverableTimeout is "u").
	for _, s := range []struct {
		prop string
		v    any
	}{{"Pairable", true}, {"DiscoverableTimeout", uint32(120)}, {"Discoverable", true}} {
		if err := b.SetAdapter(ctx, s.prop, s.v); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	sig := f.props[hci0][ifAdapter]["DiscoverableTimeout"].Signature().String()
	f.mu.Unlock()
	if sig != "u" {
		t.Errorf("DiscoverableTimeout went as %q, want u", sig)
	}

	stop, err := b.Discover(ctx, "le")
	if err != nil {
		t.Fatal(err)
	}
	stop()

	if err := b.Pair(ctx, speaker); err != nil {
		t.Fatalf("pair: %v", err)
	}
	select {
	case mac := <-b.Paired():
		if mac != speaker {
			t.Fatalf("paired %q", mac)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Paired event for the PropertiesChanged signal")
	}
	// The device's own (re)connect and disconnect (#469).
	for _, up := range []bool{true, false} {
		_ = f.conn.Emit(devPath, ifProps+".PropertiesChanged", ifDevice, map[string]dbus.Variant{"Connected": dbus.MakeVariant(up)}, []string{})
		select {
		case c := <-b.Connections():
			if c != (Connection{MAC: speaker, Connected: up}) {
				t.Fatalf("connection %+v, want %v", c, up)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no Connection event for Connected: %v", up)
		}
	}
	if err := b.Pair(ctx, speaker); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second pair: %v, want ErrAlreadyExists", err)
	}
	if err := b.Connect(ctx, speaker); !errors.Is(err, ErrInProgress) {
		t.Fatalf("connect: %v, want ErrInProgress", err)
	}
	// BlueZ's error name reaches Pair's message to the app, not only its text.
	var be BlueZError
	if err := b.Disconnect(ctx, speaker); !errors.As(err, &be) || be != (BlueZError{Name: "org.bluez.Error.Failed", Message: "br-connection-unknown"}) ||
		err.Error() != "org.bluez.Error.Failed: br-connection-unknown" {
		t.Fatalf("disconnect: %#v", err)
	}
	if err := b.SetTrusted(ctx, speaker, true); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, strings.ToLower(speaker)); err != nil {
		t.Fatal(err)
	}
	if err := b.Pair(ctx, "DE:AD:BE:EF:00:00"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("unknown device: %v", err)
	}

	wantCalls := []string{
		"Set /org/bluez/hci0 org.bluez.Adapter1 Pairable true",
		"Set /org/bluez/hci0 org.bluez.Adapter1 DiscoverableTimeout @u 120",
		"Set /org/bluez/hci0 org.bluez.Adapter1 Discoverable true",
		`SetDiscoveryFilter map[Transport:"le"]`,
		"StartDiscovery",
		"StopDiscovery",
		"Pair",
		"Pair",
		"Connect",
		"Disconnect",
		"Set /org/bluez/hci0/dev_AA_BB_CC_DD_EE_01 org.bluez.Device1 Trusted true",
		"RemoveDevice /org/bluez/hci0/dev_AA_BB_CC_DD_EE_01",
	}
	if got := f.Calls(); !slices.Equal(got, wantCalls) {
		t.Fatalf("bluetoothd saw\n %s\nwant\n %s", strings.Join(got, "\n "), strings.Join(wantCalls, "\n "))
	}
}

// AudioTransport is BlueZ's own word on the audio profile: a MediaTransport1 under the device
// (/org/bluez/hci0/dev_…/sep1/fd0, the "sep1/fd0 ready" in bluetoothd's log).
func TestAudioTransportOverDBus(t *testing.T) {
	addr := privateBus(t)
	f := startFakeBlueZ(t, addr)
	b, err := newBlueZ(dial(t, addr), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if up, err := b.AudioTransport(ctx, speaker); err != nil || up {
		t.Fatalf("before the profile: %v %v", up, err)
	}
	f.mu.Lock()
	f.props["/org/bluez/hci0/dev_0C_0C_0C_0C_0C_0C/sep1/fd0"] = map[string]map[string]dbus.Variant{"org.bluez.MediaTransport1": {"State": dbus.MakeVariant("idle")}}
	f.mu.Unlock()
	if up, err := b.AudioTransport(ctx, speaker); err != nil || up {
		t.Fatalf("another device's transport counted: %v %v", up, err)
	}
	f.mu.Lock()
	f.props[devPath+"/sep1/fd0"] = map[string]map[string]dbus.Variant{"org.bluez.MediaTransport1": {"State": dbus.MakeVariant("idle")}}
	f.mu.Unlock()
	if up, err := b.AudioTransport(ctx, speaker); err != nil || !up {
		t.Fatalf("with the transport: %v %v", up, err)
	}
	if _, err := b.AudioTransport(ctx, "DE:AD:00:00:00:09"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("unknown device: %v", err)
	}
}

func TestTheAgentAcceptsWhatBlueZAsks(t *testing.T) {
	addr := privateBus(t)
	f := startFakeBlueZ(t, addr)
	b, err := newBlueZ(dial(t, addr), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.RegisterAgent(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterAgent(ctx); err != nil { // the reconnect loop does it again
		t.Fatalf("again: %v", err)
	}
	f.mu.Lock()
	owner, p, caps := f.agent.owner, f.agent.path, f.agent.caps
	f.mu.Unlock()
	if p != agentPath || caps != "NoInputNoOutput" {
		t.Fatalf("registered %s %s", p, caps)
	}
	if got := f.Calls(); !slices.Equal(got, []string{
		"RegisterAgent /net/jarvisautomation/agent/bluetooth NoInputNoOutput", "RequestDefaultAgent /net/jarvisautomation/agent/bluetooth",
		"RegisterAgent /net/jarvisautomation/agent/bluetooth NoInputNoOutput", "RequestDefaultAgent /net/jarvisautomation/agent/bluetooth",
	}) {
		t.Fatalf("calls %v", got)
	}

	// bluetoothd calling the agent back, as it does during a pairing.
	obj := f.conn.Object(owner, p)
	for _, c := range []struct {
		method string
		args   []any
	}{
		{"RequestConfirmation", []any{devPath, uint32(123456)}},
		{"RequestAuthorization", []any{devPath}},
		{"AuthorizeService", []any{devPath, uuidAudioSink}},
		{"DisplayPasskey", []any{devPath, uint32(1), uint16(0)}},
		{"DisplayPinCode", []any{devPath, "1234"}},
		{"Cancel", nil},
		{"Release", nil},
	} {
		if err := obj.Call(ifAgent+"."+c.method, 0, c.args...).Err; err != nil {
			t.Errorf("%s: %v (rejected)", c.method, err)
		}
	}
	var pin string
	if err := obj.Call(ifAgent+".RequestPinCode", 0, devPath).Store(&pin); err != nil || pin != "0000" {
		t.Errorf("pin %q %v", pin, err)
	}
	var key uint32
	if err := obj.Call(ifAgent+".RequestPasskey", 0, devPath).Store(&key); err != nil {
		t.Errorf("passkey %v", err)
	}
}

func TestMacFromPath(t *testing.T) {
	for p, want := range map[dbus.ObjectPath]string{
		devPath:                                 speaker,
		"/org/bluez/hci1/dev_aa_bb_cc_dd_ee_ff": "AA:BB:CC:DD:EE:FF",
		hci0:                                    "",
		"/org/bluez/hci0/dev_AA_BB/fd0":         "",
	} {
		if got := macFromPath(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
}
