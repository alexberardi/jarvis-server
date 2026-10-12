package mdns

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/zeroconf/v2"

	"github.com/alexberardi/jarvis-server/internal/platform/netaddr"
)

func TestTXTRecords(t *testing.T) {
	got, err := TXTRecords(map[string]string{"version": "0.1.0", "api": "v1", "empty": ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"api=v1", "empty=", "version=0.1.0"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, err := TXTRecords(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil map: %v, %v", got, err)
	}
	for name, m := range map[string]map[string]string{
		"empty key":   {"": "x"},
		"equals":      {"a=b": "x"},
		"non-ascii":   {"clé": "x"},
		"control":     {"a\nb": "x"},
		"too long":    {"k": strings.Repeat("v", 254)},
		"exactly 256": {"ab": strings.Repeat("v", 253)},
	} {
		if _, err := TXTRecords(m); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := TXTRecords(map[string]string{"k": strings.Repeat("v", 253)}); err != nil {
		t.Errorf("255 bytes should fit: %v", err)
	}
}

func TestAdvertiseRejectsBadOptions(t *testing.T) {
	if _, err := Advertise(context.Background(), Options{Port: 70000}); err == nil {
		t.Error("want port error")
	}
	if _, err := Advertise(context.Background(), Options{TXT: map[string]string{"": "x"}}); err == nil {
		t.Error("want TXT error")
	}
}

func TestDefaultInstance(t *testing.T) {
	if s := DefaultInstance(); !strings.HasPrefix(s, "Jarvis") {
		t.Fatalf("DefaultInstance = %q", s)
	}
}

func TestHostLabel(t *testing.T) {
	for _, c := range []struct{ in, label, instance string }{
		{"Example-MacBook-Pro.local", "Example-MacBook-Pro", "Jarvis (Example-MacBook-Pro)"},
		{"Example-MacBook-Pro.local.", "Example-MacBook-Pro", "Jarvis (Example-MacBook-Pro)"},
		{"Mini.LOCAL", "Mini", "Jarvis (Mini)"},
		{"box.Local..", "box", "Jarvis (box)"},
		{"box.local.local", "box", "Jarvis (box)"},
		{"jarvis-desktop", "jarvis-desktop", "Jarvis (jarvis-desktop)"},
		{"jarvis-desktop.", "jarvis-desktop", "Jarvis (jarvis-desktop)"},
		{"host.example.com", "host.example.com", "Jarvis (host)"},
		{"localhost", "localhost", "Jarvis (localhost)"},
		{"mylocal", "mylocal", "Jarvis (mylocal)"},
		{" pi.local ", "pi", "Jarvis (pi)"},
		{".local", "", "Jarvis"},
		{"", "", "Jarvis"},
	} {
		if got := HostLabel(c.in); got != c.label {
			t.Errorf("HostLabel(%q) = %q, want %q", c.in, got, c.label)
		}
		if got := instanceFor(c.in); got != c.instance {
			t.Errorf("instanceFor(%q) = %q, want %q", c.in, got, c.instance)
		}
	}
}

func ipnet(c string) net.Addr {
	ip, n, err := net.ParseCIDR(c)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestAdvertisedIPs(t *testing.T) {
	for _, c := range []struct {
		name  string
		addrs []string
		want  []string
	}{
		{"v4 and link-local", []string{"10.0.0.20/24", "fe80::1/64"}, []string{"10.0.0.20", "fe80::1"}},
		{"global v6 hides link-local", []string{"fe80::1/64", "2001:db8::5/64", "10.0.0.20/24"}, []string{"10.0.0.20", "2001:db8::5"}},
		{"loopback dropped", []string{"127.0.0.1/8", "::1/128"}, nil},
		{"none", nil, nil},
	} {
		var addrs []net.Addr
		for _, a := range c.addrs {
			addrs = append(addrs, ipnet(a))
		}
		if got := advertisedIPs(addrs); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPickInterfaces(t *testing.T) {
	mk := func(name string, flags net.Flags, cidrs ...string) netaddr.Iface {
		ifc := netaddr.Iface{Interface: net.Interface{Name: name, Flags: flags}}
		for _, c := range cidrs {
			ifc.Addrs = append(ifc.Addrs, ipnet(c))
		}
		return ifc
	}
	up := net.FlagUp | net.FlagMulticast
	host := []netaddr.Iface{
		mk("lo", net.FlagUp|net.FlagLoopback|net.FlagMulticast, "127.0.0.1/8"),
		mk("en0", up, "10.0.0.30/24"),
		mk("bridge100", up, "192.168.64.1/24"),
		mk("docker0", up, "172.17.0.1/16"),
		mk("eth9", net.FlagUp, "10.9.9.9/24"), // no multicast
	}
	onlyVirtual := []netaddr.Iface{
		mk("lo", net.FlagUp|net.FlagLoopback|net.FlagMulticast, "127.0.0.1/8"),
		mk("docker0", up, "172.17.0.1/16"),
		mk("eth0", up, "203.0.113.4/24"),
	}
	for _, c := range []struct {
		name  string
		ifs   []netaddr.Iface
		allow []string
		want  []string
		warn  bool
	}{
		{"lan only", host, nil, []string{"en0"}, false},
		{"allow-list", host, []string{"bridge100"}, []string{"bridge100"}, false},
		{"falls back to every multicast interface", onlyVirtual, nil, []string{"lo", "docker0", "eth0"}, true},
		{"allow-list matching nothing falls back", host, []string{"nope0"}, []string{"lo", "en0", "bridge100", "docker0"}, true},
	} {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		var got []string
		for _, ifc := range pickInterfaces(c.ifs, c.allow, log) {
			got = append(got, ifc.Name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if warned := strings.Contains(buf.String(), "level=WARN"); warned != c.warn {
			t.Errorf("%s: warned = %v, want %v (%s)", c.name, warned, c.warn, buf.String())
		}
	}
}

// multicastIfaces returns up, multicast-capable interfaces, or nil.
func multicastIfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 {
			out = append(out, ifi)
		}
	}
	return out
}

// fakeServer records Shutdown calls.
type fakeServer struct{ shutdowns chan struct{} }

func (f *fakeServer) Shutdown() { f.shutdowns <- struct{}{} }

type registration struct {
	instance, service, domain, host string
	port                            int
	ips, txt                        []string
	ifaces                          []string
}

// hermetic returns options that register with a fake instead of the network: the
// interface en0 carries the given addresses, and every registration is sent on regs.
func hermetic(o Options, regs chan<- registration, srv *fakeServer, cidrs ...string) Options {
	o.Interfaces = []net.Interface{{Index: 7, Name: "en0", Flags: net.FlagUp | net.FlagMulticast}}
	o.addrs = func(ifc net.Interface) ([]net.Addr, error) {
		var out []net.Addr
		for _, c := range cidrs {
			out = append(out, ipnet(c))
		}
		return out, nil
	}
	o.register = func(instance, service, domain string, port int, host string, ips, txt []string, ifaces []net.Interface) (server, error) {
		r := registration{instance: instance, service: service, domain: domain, host: host, port: port, ips: ips, txt: txt}
		for _, ifc := range ifaces {
			r.ifaces = append(r.ifaces, ifc.Name)
		}
		regs <- r
		return srv, nil
	}
	o.Logger = slog.New(slog.DiscardHandler)
	return o
}

// What Advertise registers is what mobile browses for: the service type and domain, the
// config-service port, sorted TXT, the host label without ".local", and the interface's
// non-loopback addresses.
func TestAdvertiseRegisters(t *testing.T) {
	regs := make(chan registration, 1)
	srv := &fakeServer{shutdowns: make(chan struct{}, 2)}
	stop, err := Advertise(t.Context(), hermetic(Options{
		Instance: "Jarvis (box)",
		TXT:      map[string]string{"version": "test", "api": "v1"},
		HostName: "box.local.",
	}, regs, srv, "10.0.0.5/24", "127.0.0.1/8", "fe80::1/64"))
	if err != nil {
		t.Fatal(err)
	}
	r := <-regs
	want := registration{
		instance: "Jarvis (box)", service: "_jarvis-config._tcp", domain: "local.", host: "box",
		port: DefaultPort, ips: []string{"10.0.0.5", "fe80::1"}, txt: []string{"api=v1", "version=test"},
		ifaces: []string{"en0"},
	}
	if fmt.Sprint(r) != fmt.Sprint(want) {
		t.Fatalf("registered %+v\nwant       %+v", r, want)
	}
	stop()
	stop() // idempotent
	if n := len(srv.shutdowns); n != 1 {
		t.Fatalf("Shutdown called %d times, want 1", n)
	}
}

// Cancelling ctx withdraws the advertisement; stop afterwards is a no-op.
func TestAdvertiseStopsOnCancel(t *testing.T) {
	regs := make(chan registration, 1)
	srv := &fakeServer{shutdowns: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(t.Context())
	stop, err := Advertise(ctx, hermetic(Options{HostName: "box"}, regs, srv, "10.0.0.5/24"))
	if err != nil {
		t.Fatal(err)
	}
	<-regs
	cancel()
	<-srv.shutdowns
	stop()
	if n := len(srv.shutdowns); n != 0 {
		t.Fatalf("Shutdown called again by stop")
	}
}

func TestAdvertiseNoAddresses(t *testing.T) {
	regs := make(chan registration, 1)
	srv := &fakeServer{shutdowns: make(chan struct{}, 1)}
	if _, err := Advertise(t.Context(), hermetic(Options{HostName: "box"}, regs, srv, "127.0.0.1/8")); err == nil {
		t.Fatal("want an error with only loopback addresses")
	}
	if len(regs) != 0 {
		t.Fatal("registered with no addresses")
	}
}

// multicastTestEnv opts in to the tests that use the host's real multicast network. They
// exist to prove the advertisement round-trips through a real zeroconf browser, which
// depends on the host (CI runners may drop multicast loopback), so they don't run by default.
const multicastTestEnv = "JARVIS_TEST_MULTICAST"

// Advertise, then browse exactly as mobile does (type jarvis-config/tcp, domain local.)
// and check the port and TXT come through. Needs JARVIS_TEST_MULTICAST=1 (real network).
func TestAdvertiseBrowse(t *testing.T) {
	if os.Getenv(multicastTestEnv) != "1" {
		t.Skip("real-multicast round trip; set " + multicastTestEnv + "=1 to run")
	}
	ifaces := multicastIfaces()
	if len(ifaces) == 0 {
		t.Fatal("no multicast-capable interface")
	}
	instance := "jarvis-test-" + time.Now().Format("150405.000000")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := Advertise(ctx, Options{
		Instance:   instance,
		Port:       17700,
		TXT:        map[string]string{"version": "test"},
		Interfaces: ifaces,
	})
	if err != nil {
		t.Fatalf("advertise: %v", err)
	}
	defer stop()

	// zeroconf v2's Browse blocks until bctx is done and sends every entry unconditionally,
	// closing entries when it stops, so run it alongside and keep draining until it returns.
	entries := make(chan *zeroconf.ServiceEntry)
	bctx, bcancel := context.WithTimeout(ctx, 30*time.Second)
	defer bcancel()
	var browseErr error
	browsed := make(chan struct{})
	go func() {
		defer close(browsed)
		browseErr = zeroconf.Browse(bctx, ServiceType, Domain, entries, zeroconf.SelectIfaces(ifaces))
	}()
	defer func() {
		bcancel()
		for {
			select {
			case <-entries:
			case <-browsed:
				return
			}
		}
	}()
	recv := (<-chan *zeroconf.ServiceEntry)(entries)
	for {
		select {
		case <-browsed:
			if browseErr != nil {
				t.Fatalf("browse: %v", browseErr)
			}
			t.Fatal("advertisement not seen within 30s; is multicast blocked on this host?")
		case e, ok := <-recv:
			if !ok {
				recv = nil // closed; Browse is returning
				continue
			}
			if e == nil || e.Instance != instance {
				continue
			}
			if e.Port != 17700 {
				t.Errorf("port = %d", e.Port)
			}
			if !slices.Contains(e.Text, "version=test") {
				t.Errorf("TXT = %v", e.Text)
			}
			if strings.Count(strings.ToLower(e.HostName), ".local") != 1 {
				t.Errorf("host = %q, want exactly one .local", e.HostName)
			}
			if len(e.AddrIPv4)+len(e.AddrIPv6) == 0 {
				t.Errorf("no addresses resolved")
			}
			stop()
			stop() // idempotent
			return
		}
	}
}

// Stopping straight after Advertise must be clean under -race. grandcat/zeroconf v1.0.0
// called WaitGroup.Add inside its receive goroutines, so a Shutdown that ran before they
// were scheduled raced the Add with its Wait (CI flake, run 38101087370; reproduces with
// -count=200 -cpu=1,2,4). libp2p/zeroconf/v2 Adds before starting them. Several rounds
// widen the window; skips where multicast isn't available.
func TestAdvertiseStopImmediately(t *testing.T) {
	if testing.Short() {
		t.Skip("multicast test skipped in -short mode")
	}
	ifaces := multicastIfaces()
	if len(ifaces) == 0 {
		t.Skip("no multicast-capable interface")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for i := range 5 {
		stop, err := Advertise(context.Background(), Options{
			Instance:   fmt.Sprintf("jarvis-stop-test-%d-%s", i, time.Now().Format("150405.000000")),
			Port:       17701,
			Interfaces: ifaces,
			Logger:     log,
		})
		if err != nil {
			t.Skipf("multicast unavailable: %v", err)
		}
		stop()
	}
}
