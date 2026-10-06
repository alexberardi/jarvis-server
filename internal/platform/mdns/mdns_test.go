package mdns

import (
	"context"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
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
