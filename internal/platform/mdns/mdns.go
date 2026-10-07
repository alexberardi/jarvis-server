// Package mdns advertises jarvisd on the LAN as _jarvis-config._tcp (PLAN §3.1), so the
// mobile app finds the server without a manual URL or a /24 sweep.
//
// What clients rely on (jarvis-node-mobile src/services/configDiscoveryService.ts): they
// browse type "jarvis-config", protocol "tcp", domain "local.", take the resolved port and
// addresses, then probe http://<addr>:<port>/info for service == "jarvis-config-service".
// So the advertised port must be the config-service listener (7700). Clients read no TXT
// records today; TXT is informational.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/grandcat/zeroconf"

	"github.com/alexberardi/jarvis-server/internal/platform/netaddr"
)

const (
	// ServiceType is the DNS-SD service type mobile browses for.
	ServiceType = "_jarvis-config._tcp"
	// Domain is the mDNS domain.
	Domain = "local."
	// DefaultPort is the config-service listener mobile probes.
	DefaultPort = 7700
)

// Options configures an advertisement.
type Options struct {
	// Instance is the human-readable instance name. Default "Jarvis (<hostname>)".
	Instance string
	// Port is the advertised port. Default DefaultPort.
	Port int
	// TXT records, encoded as sorted key=value strings.
	TXT map[string]string
	// Interfaces to advertise on. Nil means the LAN interfaces (netaddr.LAN with Allow), or
	// every multicast-capable one when there are none.
	Interfaces []net.Interface
	// Allow names the interfaces to advertise on (JARVIS_MDNS_INTERFACES), overriding the
	// LAN filter. Ignored when Interfaces is set.
	Allow []string
	// HostName is the host the SRV record points at; ".local" is stripped (see HostLabel).
	// Default: os.Hostname().
	HostName string
	// Logger; nil uses slog.Default.
	Logger *slog.Logger
}

// Advertise registers the service and keeps answering queries until stop is called or ctx
// is done. stop is idempotent and safe to call after ctx is done.
func Advertise(ctx context.Context, opts Options) (stop func(), err error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "mdns")
	if opts.Instance == "" {
		opts.Instance = DefaultInstance()
	}
	if opts.Port == 0 {
		opts.Port = DefaultPort
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return nil, fmt.Errorf("mdns: invalid port %d", opts.Port)
	}
	txt, err := TXTRecords(opts.TXT)
	if err != nil {
		return nil, err
	}

	if opts.HostName == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("mdns: hostname: %w", err)
		}
		opts.HostName = h
	}
	host := HostLabel(opts.HostName)
	if host == "" {
		return nil, fmt.Errorf("mdns: unusable hostname %q", opts.HostName)
	}

	ifaces := opts.Interfaces
	if ifaces == nil {
		ifaces = pickInterfaces(netaddr.List(), opts.Allow, log)
	}
	var ips, names []string
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		ips = append(ips, advertisedIPs(addrs)...)
		names = append(names, ifc.Name)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("mdns: no addresses to advertise on %v", names)
	}

	// RegisterProxy rather than Register: Register takes os.Hostname() as is and appends
	// ".local." even when it already ends in ".local" (macOS), and answers with every address.
	srv, err := zeroconf.RegisterProxy(opts.Instance, ServiceType, Domain, opts.Port, host, ips, txt, ifaces)
	if err != nil {
		return nil, fmt.Errorf("mdns: register %s: %w", ServiceType, err)
	}
	log.Info("mdns advertising", "instance", opts.Instance, "type", ServiceType, "port", opts.Port,
		"host", host+"."+Domain, "interfaces", names, "addrs", ips)

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
		case <-done:
		}
		srv.Shutdown()
		log.Info("mdns stopped", "instance", opts.Instance)
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}, nil
}

// pickInterfaces is the LAN interfaces of all (honouring allow), or, when there are none,
// every up multicast-capable interface as before the filter existed, with a warning.
func pickInterfaces(all []netaddr.Iface, allow []string, log *slog.Logger) []net.Interface {
	lan := netaddr.LAN(all, allow)
	if len(lan) == 0 {
		var fallback []net.Interface
		for _, ifc := range all {
			if ifc.Flags&net.FlagUp != 0 && ifc.Flags&net.FlagMulticast != 0 {
				fallback = append(fallback, ifc.Interface)
			}
		}
		log.Warn("mdns: no LAN interface found; advertising on every multicast interface, "+
			"which may include unreachable bridge addresses (set JARVIS_MDNS_INTERFACES to choose)",
			"allow", allow)
		return fallback
	}
	out := make([]net.Interface, 0, len(lan))
	for _, ifc := range lan {
		out = append(out, ifc.Interface)
	}
	return out
}

// advertisedIPs picks an interface's addresses as zeroconf's Register does: every
// non-loopback IPv4 and global IPv6, and link-local IPv6 only when there is no global one.
func advertisedIPs(addrs []net.Addr) []string {
	var v4, v6, v6local []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() {
			continue
		}
		switch {
		case n.IP.To4() != nil:
			v4 = append(v4, n.IP.String())
		case n.IP.IsGlobalUnicast():
			v6 = append(v6, n.IP.String())
		case n.IP.IsLinkLocalUnicast():
			v6local = append(v6local, n.IP.String())
		}
	}
	if len(v6) == 0 {
		v6 = v6local
	}
	return append(v4, v6...)
}

// HostLabel is the hostname to advertise under ".local.": h without trailing dots or a
// ".local" suffix (any case). macOS's os.Hostname() ends in ".local", and zeroconf appends
// ".local." regardless, which advertised "host.local.local.".
func HostLabel(h string) string {
	h = strings.TrimSpace(h)
	for {
		t := strings.TrimSuffix(h, ".")
		if l := len(t) - len(".local"); l >= 0 && strings.EqualFold(t[l:], ".local") {
			t = t[:l]
		}
		if t == h {
			return h
		}
		h = t
	}
}

// DefaultInstance is "Jarvis (<hostname>)", or "Jarvis" if the hostname is unknown.
func DefaultInstance() string {
	h, _ := os.Hostname()
	return instanceFor(h)
}

// instanceFor is the instance name for hostname h: its first label, so a macOS
// "Alexanders-MacBook-Pro.local" reads "Jarvis (Alexanders-MacBook-Pro)".
func instanceFor(h string) string {
	h = HostLabel(h)
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	if h == "" || h[0] == '.' {
		return "Jarvis"
	}
	return "Jarvis (" + h + ")"
}

// TXTRecords encodes m as DNS-SD TXT strings ("key=value"), sorted by key so the record is
// deterministic. Keys must be non-empty printable ASCII without '='; each string is
// limited to 255 bytes (RFC 6763 §6).
func TXTRecords(m map[string]string) ([]string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if err := validTXTKey(k); err != nil {
			return nil, err
		}
		s := k + "=" + m[k]
		if len(s) > 255 {
			return nil, fmt.Errorf("mdns: TXT %q: longer than 255 bytes", k)
		}
		out = append(out, s)
	}
	return out, nil
}

func validTXTKey(k string) error {
	if k == "" {
		return errors.New("mdns: empty TXT key")
	}
	for i := 0; i < len(k); i++ {
		if c := k[i]; c < 0x20 || c > 0x7e || c == '=' {
			return fmt.Errorf("mdns: TXT key %q: must be printable ASCII without '='", k)
		}
	}
	return nil
}
