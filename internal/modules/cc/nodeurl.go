package cc

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/netaddr"
)

// Node-facing URLs for provisioning. The mobile app hands a new node the server URLs it used
// itself, but a phone can reach the server through a loopback tunnel (USB `adb reverse`), so
// "its" URLs are http://localhost:7703 — which, on the node, is the node. Found live
// 2026-10-09: the node registered against itself, got connection refused and fell back to AP
// mode until it was re-provisioned. POST /provisioning/token therefore also answers with the
// URLs a node on the LAN should use, and the app prefers them (additive: legacy clients ignore
// the fields, and an app talking to the legacy server falls back to its own URLs).

// nodeURLs returns the command-center and config-service base URLs a node on the LAN should
// use, derived from the request the phone made to this server:
//
//   - a non-loopback Host is reachable from the LAN as it is, so the command-center URL is the
//     one the phone used (scheme + Host). The config URL swaps in the config port when Host
//     names a port; without one the phone came through a proxy or tunnel that only it knows,
//     so the config URL is left out.
//   - a loopback Host (localhost, 127.0.0.0/8, ::1, or an unspecified address) is useless to a
//     node, so both URLs use this host's LAN address (lan) and the listeners' ports.
//
// Either is "" when it can't be determined, and the caller omits it.
func nodeURLs(r *http.Request, cfg config.Config, lan func() string) (ccURL, configURL string) {
	host, port := splitHost(r.Host)
	if host == "" {
		return "", ""
	}
	if !isLoopbackHost(host) {
		scheme := "http"
		if r.TLS != nil || (loopbackRemote(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")) {
			scheme = "https"
		}
		ccURL = scheme + "://" + r.Host
		if port != "" {
			configURL = scheme + "://" + net.JoinHostPort(host, strconv.Itoa(listenerPort(cfg, config.ListenerConfig)))
		}
		return ccURL, configURL
	}
	addr := ""
	if lan != nil {
		addr = lan()
	}
	if addr == "" {
		return "", ""
	}
	return "http://" + net.JoinHostPort(addr, strconv.Itoa(listenerPort(cfg, config.ListenerCC))),
		"http://" + net.JoinHostPort(addr, strconv.Itoa(listenerPort(cfg, config.ListenerConfig)))
}

// nodeLANAddr is this host's LAN address (the mDNS / setup-link rules), or "".
func (m *Module) nodeLANAddr() string {
	if m.lanAddr != nil {
		return m.lanAddr()
	}
	return netaddr.LANAddr(m.deps.Config.MDNSInterfaces)
}

// splitHost splits a Host header into its host (lower case, no brackets or trailing dot) and
// port ("" when it names none).
func splitHost(h string) (host, port string) {
	h = strings.TrimSpace(h)
	if hh, p, err := net.SplitHostPort(h); err == nil {
		h, port = hh, p
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(strings.ToLower(h), "."), port
}

// isLoopbackHost reports whether a Host names this machine only: localhost (and *.localhost),
// a loopback IP, or an unspecified one.
func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsUnspecified()
}

// loopbackRemote reports whether the peer is on this machine (a local reverse proxy or tunnel,
// whose X-Forwarded-Proto may be believed).
func loopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// listenerPort is cfg's port for a listener, or its default.
func listenerPort(cfg config.Config, listener string) int {
	if p, ok := cfg.Ports[listener]; ok && p != 0 {
		return p
	}
	return config.DefaultPorts[listener]
}
