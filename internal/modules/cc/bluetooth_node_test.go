package cc

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/bluetooth"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// TestGoNodeBluetoothAgainstTheseRoutes is the Go Pi node's Bluetooth handler
// (internal/agent/bluetooth) against these routes: what jarvisd publishes is what it reads, and
// what it posts back, through the node's own client, is what the app then polls and the status
// page shows.
func TestGoNodeBluetoothAgainstTheseRoutes(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	base := "/api/v0/nodes/n1"
	const mac = "AA:BB:CC:DD:EE:01"

	bus := &radio{devices: map[string]*bluetooth.Device{}, inRange: map[string]bluetooth.Device{
		mac: {Address: mac, Name: "JBL Flip 5", Icon: "audio-card"},
	}}
	st, err := bluetooth.OpenStore(filepath.Join(t.TempDir(), bluetooth.StoreFile))
	if err != nil {
		t.Fatal(err)
	}
	bt := &bluetooth.Node{Bus: bus, Store: st, Log: slog.New(slog.DiscardHandler), ScanFor: time.Millisecond, FindFor: 50 * time.Millisecond, Poll: time.Millisecond}
	cc := node.NewCC(e.srv.URL, node.Creds{NodeID: n.id, NodeKey: n.key})
	deliver := func() string {
		t.Helper()
		pk := c.next()
		sub := strings.TrimPrefix(pk.TopicName, "jarvis/nodes/n1/")
		bt.Handle(context.Background(), cc, sub, pk.Payload)
		return sub
	}

	scan := e.do("POST", base+"/bluetooth-scan/request", nil, bearer(tok)).want(201).json()
	if sub := deliver(); sub != bluetooth.TopicScan {
		t.Fatalf("topic %q", sub)
	}
	p := e.do("GET", base+"/bluetooth-scan/"+scan["id"].(string), nil, bearer(tok)).want(200).json()
	devs, _ := p["devices"].([]any)
	if p["status"] != "completed" || p["progress"] != "scanning" || len(devs) != 1 || devs[0].(map[string]any)["mac_address"] != mac ||
		devs[0].(map[string]any)["device_type"] != "audio_sink" {
		t.Fatalf("scan poll %v", p)
	}

	pair := e.do("POST", base+"/bluetooth/pair", map[string]any{"mac_address": mac}, bearer(tok)).want(201).json()
	if sub := deliver(); sub != bluetooth.TopicPair {
		t.Fatalf("topic %q", sub)
	}
	if p := e.do("GET", base+"/bluetooth/pair/"+pair["id"].(string), nil, bearer(tok)).want(200).json(); p["status"] != "completed" || p["device_name"] != "JBL Flip 5" ||
		p["progress"] != bluetooth.StepConnecting {
		t.Fatalf("pair poll %v", p)
	}
	// The status page shows the pair at once, without another scan.
	status := func() (connected, paired []any) {
		t.Helper()
		s := e.do("GET", base+"/bluetooth/status", nil, bearer(tok)).want(200).json()
		connected, _ = s["connected"].([]any)
		paired, _ = s["paired"].([]any)
		return connected, paired
	}
	if conn, pd := status(); len(conn) != 1 || conn[0].(map[string]any)["mac_address"] != mac || conn[0].(map[string]any)["name"] != "JBL Flip 5" || len(pd) != 0 {
		t.Fatalf("status after the pair: connected %v paired %v", conn, pd)
	}

	// The status page reads the latest scan, which now has the device paired and connected.
	e.do("POST", base+"/bluetooth-scan/request", nil, bearer(tok)).want(201)
	deliver()
	s := e.do("GET", base+"/bluetooth/status", nil, bearer(tok)).want(200).json()
	if conn, _ := s["connected"].([]any); len(conn) != 1 || conn[0].(map[string]any)["mac_address"] != mac {
		t.Fatalf("status %v", s)
	}

	// A failed pair is reported as one. (A bonded device is connected with its bond, #469: this
	// one has lost it.)
	bus.mu.Lock()
	bus.devices[mac].Paired = false
	bus.mu.Unlock()
	bus.pairErr = true
	failed := e.do("POST", base+"/bluetooth/pair", map[string]any{"mac_address": mac}, bearer(tok)).want(201).json()
	deliver()
	if p := e.do("GET", base+"/bluetooth/pair/"+failed["id"].(string), nil, bearer(tok)).want(200).json(); p["status"] != "failed" ||
		!strings.Contains(fmt.Sprint(p["error_message"]), "pairing mode") {
		t.Fatalf("failed pair poll %v", p)
	}
	bus.pairErr = false

	// The fire-and-forget ones reach the radio and the saved devices.
	e.do("POST", base+"/bluetooth/discoverable", nil, bearer(tok)).want(202)
	deliver()
	e.do("POST", base+"/bluetooth/auto-connect", map[string]any{"mac_address": mac, "enabled": false}, bearer(tok)).want(202)
	deliver()
	if r, _ := st.Get(mac); r.AutoConnect {
		t.Fatal("auto-connect still on")
	}
	e.do("POST", base+"/bluetooth/disconnect", map[string]any{"mac_address": strings.ToLower(mac)}, bearer(tok)).want(202)
	deliver()
	if conn, pd := status(); len(conn) != 0 || len(pd) != 1 || pd[0].(map[string]any)["mac_address"] != mac {
		t.Fatalf("status after a disconnect: connected %v paired %v", conn, pd)
	}
	e.do("POST", base+"/bluetooth/release", map[string]any{"mac_address": mac, "forget": true}, bearer(tok)).want(202)
	deliver()
	if _, ok := st.Get(mac); ok {
		t.Fatal("forget kept the record")
	}
	// Forget is gone from the app at once (#433: it stayed listed until the next scan).
	if conn, pd := status(); len(conn)+len(pd) != 0 {
		t.Fatalf("status after forget: connected %v paired %v", conn, pd)
	}
	want := "SetAdapter Powered,SetAdapter Pairable,SetAdapter DiscoverableTimeout=120,SetAdapter Discoverable,Disconnect,Disconnect,Remove"
	if got := strings.Join(bus.tail(7), ","); got != want {
		t.Fatalf("radio calls %s\nwant %s", got, want)
	}
}

// radio is a minimal BlueZ: devices appear on discovery, pair and connect.
type radio struct {
	mu      sync.Mutex
	devices map[string]*bluetooth.Device
	inRange map[string]bluetooth.Device
	pairErr bool
	calls   []string
}

func (r *radio) log(s string) { r.mu.Lock(); r.calls = append(r.calls, s); r.mu.Unlock() }
func (r *radio) tail(k int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-k:]
}

func (r *radio) AudioTransport(context.Context, string) (bool, error) { return false, nil }

func (r *radio) Adapter(context.Context) (bluetooth.Adapter, error) {
	return bluetooth.Adapter{Powered: true}, nil
}

func (r *radio) SetAdapter(_ context.Context, prop string, v any) error {
	if prop == "DiscoverableTimeout" {
		prop += "=" + strconv.FormatUint(uint64(v.(uint32)), 10)
	}
	r.log("SetAdapter " + prop)
	return nil
}

func (r *radio) Devices(context.Context) ([]bluetooth.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []bluetooth.Device
	for _, d := range r.devices {
		out = append(out, *d)
	}
	return out, nil
}

func (r *radio) Discover(context.Context, string) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for m, d := range r.inRange {
		if r.devices[m] == nil {
			c := d
			r.devices[m] = &c
		}
	}
	return func() {}, nil
}

func (r *radio) Remove(_ context.Context, mac string) error {
	r.log("Remove")
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.devices, mac)
	return nil
}

func (r *radio) Pair(_ context.Context, mac string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pairErr {
		return bluetooth.ErrInProgress
	}
	r.devices[mac].Paired = true
	return nil
}

func (r *radio) Connect(_ context.Context, mac string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices[mac].Connected = true
	return nil
}

func (r *radio) Disconnect(_ context.Context, mac string) error {
	r.log("Disconnect")
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := r.devices[mac]; d != nil {
		d.Connected = false
	}
	return nil
}

func (r *radio) SetTrusted(context.Context, string, bool) error { return nil }
func (r *radio) RegisterAgent(context.Context) error            { return nil }
func (r *radio) Paired() <-chan string                          { return nil }
func (r *radio) Connections() <-chan bluetooth.Connection       { return nil }
