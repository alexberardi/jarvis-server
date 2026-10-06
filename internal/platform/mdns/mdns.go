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
	// Interfaces to advertise on. Nil means every multicast-capable interface.
	Interfaces []net.Interface
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

	srv, err := zeroconf.Register(opts.Instance, ServiceType, Domain, opts.Port, txt, opts.Interfaces)
	if err != nil {
		return nil, fmt.Errorf("mdns: register %s: %w", ServiceType, err)
	}
	log.Info("mdns advertising", "instance", opts.Instance, "type", ServiceType, "port", opts.Port)

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

// DefaultInstance is "Jarvis (<hostname>)", or "Jarvis" if the hostname is unknown.
func DefaultInstance() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "Jarvis"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
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
