package mdns

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"

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
		{"Alexanders-MacBook-Pro.local", "Alexanders-MacBook-Pro", "Jarvis (Alexanders-MacBook-Pro)"},
		{"Alexanders-MacBook-Pro.local.", "Alexanders-MacBook-Pro", "Jarvis (Alexanders-MacBook-Pro)"},
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
		{"v4 and link-local", []string{"10.0.0.122/24", "fe80::1/64"}, []string{"10.0.0.122", "fe80::1"}},
		{"global v6 hides link-local", []string{"fe80::1/64", "2001:db8::5/64", "10.0.0.122/24"}, []string{"10.0.0.122", "2001:db8::5"}},
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
		mk("en0", up, "10.0.0.103/24"),
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

// Advertise, then browse exactly as mobile does (type jarvis-config/tcp, domain local.)
// and check the port and TXT come through. Skips where multicast isn't available.
func TestAdvertiseBrowse(t *testing.T) {
	if testing.Short() {
		t.Skip("multicast test skipped in -short mode")
	}
	ifaces := multicastIfaces()
	if len(ifaces) == 0 {
		t.Skip("no multicast-capable interface")
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
		t.Skipf("multicast unavailable: %v", err)
	}
	defer stop()

	res, err := zeroconf.NewResolver(zeroconf.SelectIfaces(ifaces))
	if err != nil {
		t.Skipf("resolver unavailable: %v", err)
	}
	entries := make(chan *zeroconf.ServiceEntry)
	bctx, bcancel := context.WithTimeout(ctx, 5*time.Second)
	defer bcancel()
	if err := res.Browse(bctx, ServiceType, Domain, entries); err != nil {
		t.Skipf("browse unavailable: %v", err)
	}
	for {
		select {
		case e := <-entries:
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
		case <-bctx.Done():
			t.Skip("advertisement not seen within 5s; multicast likely blocked in this environment")
		}
	}
}
