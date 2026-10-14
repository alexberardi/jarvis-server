package firstrun

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/libp2p/zeroconf/v2"

	"github.com/alexberardi/jarvis-server/internal/platform/mdns"
)

// BrowseMDNS looks for Jarvis Automation servers on the network: the _jarvis-config._tcp
// service jarvisd advertises (the mobile app finds it the same way). Each address of each
// server is reported; the page probes them and lists each server once.
func BrowseMDNS(ctx context.Context, found func(url, name string)) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan *zeroconf.ServiceEntry)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				for _, u := range entryURLs(e.Port, e.AddrIPv4, e.AddrIPv6) {
					found(u, ServerName(e.Instance))
				}
			}
		}
	}()
	// Browse runs until ctx ends (it closes ch then, but not when it fails to start).
	_ = zeroconf.Browse(ctx, mdns.ServiceType, mdns.Domain, ch)
	cancel()
	<-done
}

// entryURLs are an announcement's config service URLs: IPv4 first, then routable IPv6 (a
// link-local address needs a zone a URL can't carry well).
func entryURLs(port int, v4, v6 []net.IP) []string {
	if port <= 0 || port > 65535 {
		return nil
	}
	p := strconv.Itoa(port)
	var out []string
	for _, ip := range v4 {
		out = append(out, "http://"+net.JoinHostPort(ip.String(), p))
	}
	for _, ip := range v6 {
		if ip.IsGlobalUnicast() || ip.IsPrivate() {
			out = append(out, "http://"+net.JoinHostPort(ip.String(), p))
		}
	}
	return out
}

// ServerName is how the page names an advertised server: "Jarvis (kitchen-pc)" reads
// "kitchen-pc".
func ServerName(instance string) string {
	instance = strings.ReplaceAll(instance, `\`, "") // DNS-SD escapes
	if in, ok := strings.CutPrefix(instance, "Jarvis ("); ok && strings.HasSuffix(in, ")") {
		return strings.TrimSuffix(in, ")")
	}
	return instance
}
